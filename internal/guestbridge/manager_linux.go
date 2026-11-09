//go:build linux

package guestbridge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrUnavailable = errors.New("paired Windows guest is not connected")

type Status struct {
	NativeCopy   bool   `json:"native_copy"`
	Enabled      bool   `json:"enabled"`
	Connected    bool   `json:"connected"`
	Unlocked     bool   `json:"unlocked"`
	User         string `json:"user,omitempty"`
	Message      string `json:"message,omitempty"`
	MaxTextBytes int    `json:"max_text_bytes"`
}

type Manager struct {
	asleep  func() bool
	access  *Access
	open    func(context.Context) (Reports, error)
	gate    chan struct{}
	mu      sync.Mutex
	current *deviceSession
	last    Response
}

func NewManager(access *Access, open func(context.Context) (Reports, error)) *Manager {
	return NewManagerWithSleepCheck(access, open, nil)
}

// The sleep callback is configured before Run and never changed.
func NewManagerWithSleepCheck(access *Access, open func(context.Context) (Reports, error), asleep func() bool) *Manager {
	return &Manager{access: access, open: open, gate: make(chan struct{}, 1), asleep: asleep}
}
func (m *Manager) isAsleep() bool { return m.asleep != nil && m.asleep() }

func (m *Manager) Status() Status {
	enabled := m.access.Enabled()
	m.mu.Lock()
	defer m.mu.Unlock()
	state := Status{Enabled: enabled, MaxTextBytes: MaxText}
	if !enabled {
		state.Message = "Guest integration is disabled"
		return state
	}
	if m.isAsleep() {
		state.Message = ErrAsleep.Error()
		return state
	}
	if m.current == nil || m.current.ctx.Err() != nil || m.current.revoked() {
		state.Message = "Waiting for the paired Windows guest"
		return state
	}
	state.Connected = true
	state.NativeCopy = m.current.nativeCopy
	state.Unlocked = m.last.Unlocked
	state.User = m.last.User
	state.Message = m.last.Error
	return state
}

func (m *Manager) Run(ctx context.Context) {
	defer m.disconnect(nil)
	for ctx.Err() == nil {
		key, pairingCtx := m.access.Snapshot()
		if len(key) != 32 {
			select {
			case <-ctx.Done():
				return
			case <-pairingCtx.Done():
				continue
			}
		}
		connectionCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(pairingCtx, cancel)
		m.runPairing(connectionCtx, pairingCtx, key)
		cancel()
		stop()
		clear(key)
	}
}

func (m *Manager) runPairing(ctx, pairingCtx context.Context, key []byte) {
	reset := true
	for ctx.Err() == nil {
		if m.isAsleep() {
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		reports, err := m.open(ctx)
		if err == nil {
			stop := context.AfterFunc(ctx, func() { reports.Close() })
			handshakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			frame := &Framer{IO: reports}
			var channel *Secure
			var handshakeErr error
			if reset {
				handshakeErr = frame.Write(handshakeCtx, []byte{resetKind})
				if handshakeErr == nil {
					reset = false
				}
			}
			if handshakeErr == nil {
				channel, handshakeErr = serverHandshake(handshakeCtx, frame, key, false)
			}
			// A single reset prompts a surviving client after firmware restart.
			// Failed handshakes thereafter wait passively and never keep waking
			// a sleeping host merely because the companion is not installed.
			cancel()
			if handshakeErr == nil {
				session := newDeviceSession(ctx, reports, channel)
				session.pairingCtx = pairingCtx
				capabilities, probeErr := session.request(ctx, "clipboard_capabilities", "")
				session.nativeCopy = probeErr == nil && capabilities.Error == "" && capabilities.NativeCopy
				m.mu.Lock()
				m.current = session
				m.last = Response{}
				m.mu.Unlock()
				<-session.ctx.Done()
				m.disconnect(session)
				reset = true
			}
			stop()
			reports.Close()
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *Manager) disconnect(expected *deviceSession) {
	m.mu.Lock()
	current := m.current
	if expected != nil && current != expected {
		m.mu.Unlock()
		return
	}
	m.current = nil
	m.last = Response{}
	m.mu.Unlock()
	if current != nil {
		current.close(context.Canceled)
	}
}

type interactiveContextKey struct{}

var ErrInteractiveBusy = errors.New("guest clipboard is busy; try again")

func InteractiveContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveContextKey{}, true)
}

func (m *Manager) operation(ctx context.Context, fn func(context.Context, *deviceSession) error) error {
	if m.isAsleep() {
		return ErrAsleep
	}
	var wait <-chan time.Time
	var admissionDeadline time.Time
	if interactive, _ := ctx.Value(interactiveContextKey{}).(bool); interactive {
		admissionDeadline = time.Now().Add(200 * time.Millisecond)
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		wait = timer.C
	}
	select {
	case m.gate <- struct{}{}:
	case <-wait:
		return ErrInteractiveBusy
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.gate }()
	if !admissionDeadline.IsZero() && time.Now().After(admissionDeadline) {
		return ErrInteractiveBusy
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.isAsleep() {
		return ErrAsleep
	}
	m.mu.Lock()
	session := m.current
	m.mu.Unlock()
	if !m.access.Enabled() || session == nil || session.ctx.Err() != nil || session.revoked() {
		return ErrUnavailable
	}
	bound, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.ctx, cancel)
	defer stop()
	defer cancel()
	err := fn(bound, session)
	if session.revoked() {
		return ErrUnavailable
	}
	return err
}

func (m *Manager) response(session *deviceSession, response Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == session {
		// Clipboard content is returned to its caller only; never cache it in
		// manager status or retain a clipboard history on the KVM.
		m.last = Response{Unlocked: response.Unlocked, User: response.User, Error: response.Error}
	}
}

func (m *Manager) CheckStatus(ctx context.Context) Status {
	err := m.operation(ctx, func(ctx context.Context, session *deviceSession) error {
		response, err := session.request(ctx, "status", "")
		if err != nil {
			return err
		}
		m.response(session, response)
		return nil
	})
	state := m.Status()
	if err != nil && state.Connected {
		state.Unlocked = false
		state.Message = "Guest status could not be refreshed"
	}
	return state
}

func (m *Manager) GetClipboard(ctx context.Context) (Response, error) {
	var result Response
	err := m.operation(ctx, func(ctx context.Context, session *deviceSession) error {
		response, err := session.request(session.ctx, "get_clipboard", "")
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		m.response(session, response)
		if response.Error != "" {
			return errors.New(response.Error)
		}
		if !response.Unlocked {
			return ErrLocked
		}
		result = response
		return nil
	})
	return result, err
}

func (m *Manager) SetClipboard(ctx context.Context, text string) error {
	return m.WithClipboard(ctx, text, nil)
}

// WithClipboard keeps one transaction across the acknowledged clipboard write
// and optional HID paste. It never retries or falls back to typing on failure.
func (m *Manager) WithClipboard(ctx context.Context, text string, paste func(context.Context) error) error {
	if err := ValidateText(text); err != nil {
		return err
	}
	return m.operation(ctx, func(ctx context.Context, session *deviceSession) error {
		response, err := session.request(session.ctx, "set_clipboard", text)
		if err != nil {
			return fmt.Errorf("clipboard update did not complete; clipboard may have changed: %w", err)
		}
		m.response(session, response)
		if response.Error != "" {
			return errors.New(response.Error)
		}
		if !response.Unlocked {
			return ErrLocked
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if session.revoked() {
			return ErrUnavailable
		}
		if paste != nil {
			if err := paste(ctx); err != nil {
				return fmt.Errorf("clipboard was updated; paste input may be incomplete: %w", err)
			}
		}
		return nil
	})
}

type deviceSession struct {
	nativeCopy    bool
	pairingCtx    context.Context
	ctx           context.Context
	cancel        context.CancelCauseFunc
	reports       Reports
	channel       *Secure
	mu            sync.Mutex
	next          uint64
	pendingID     uint64
	pendingMethod string
	pending       chan Response
}

func newDeviceSession(ctx context.Context, reports Reports, channel *Secure) *deviceSession {
	ctx, cancel := context.WithCancelCause(ctx)
	s := &deviceSession{ctx: ctx, cancel: cancel, reports: reports, channel: channel}
	context.AfterFunc(ctx, func() { reports.Close() })
	go s.receive()
	return s
}

func (s *deviceSession) revoked() bool { return s.pairingCtx != nil && s.pairingCtx.Err() != nil }

func (s *deviceSession) close(err error) { s.cancel(err); s.reports.Close() }

func (s *deviceSession) receive() {
	for {
		var response Response
		if err := s.channel.Receive(s.ctx, &response); err != nil {
			s.close(err)
			return
		}
		if ValidateText(response.Text) != nil || len(response.User) > 256 || !utf8.ValidString(response.User) || strings.ContainsRune(response.User, 0) || len(response.Error) > 1024 || !utf8.ValidString(response.Error) {
			s.close(ErrProtocol)
			return
		}
		s.mu.Lock()
		pending := s.pending
		if pending == nil || response.ID != s.pendingID || (s.pendingMethod != "get_clipboard" && s.pendingMethod != "copy_clipboard" && (response.HasText || response.Text != "")) || (!response.HasText && response.Text != "") {
			s.mu.Unlock()
			s.close(ErrProtocol)
			return
		}
		// Errors cross a trust boundary as well. Never put arbitrary target
		// data into KVM logs or status messages disguised as an error string.
		if response.Error != "" && response.Error != ErrLocked.Error() && response.Error != "clipboard is busy" {
			response.Error = "Guest clipboard operation failed"
			if s.pendingMethod == "set_clipboard" {
				response.Error += "; clipboard may have changed"
			}
		}
		s.pending = nil
		s.pendingID = 0
		s.mu.Unlock()
		pending <- response
	}
}

func (s *deviceSession) request(ctx context.Context, method, text string) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, guestRequestTimeout(method, len(text)))
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if s.ctx.Err() != nil || s.revoked() {
		return Response{}, ErrUnavailable
	}
	reply := make(chan Response, 1)
	s.mu.Lock()
	if s.pending != nil {
		s.mu.Unlock()
		return Response{}, errors.New("guest request already in progress")
	}
	s.next++
	id := s.next
	s.pendingID = id
	s.pendingMethod = method
	s.pending = reply
	s.mu.Unlock()
	if err := s.channel.Send(ctx, Request{ID: id, Method: method, Text: text}); err != nil {
		s.close(err)
		return Response{}, err
	}
	select {
	case response := <-reply:
		if s.revoked() {
			return Response{}, ErrUnavailable
		}
		return response, nil
	case <-ctx.Done():
		s.close(ctx.Err())
		return Response{}, ctx.Err()
	case <-s.ctx.Done():
		return Response{}, context.Cause(s.ctx)
	}
}

// Budget one ACK round trip per report, conservatively allowing 10 ms each
// (full-speed polling), plus host UI work. Base64 fixes expansion at 4/3.
func guestRequestTimeout(method string, size int) time.Duration {
	if method == "get_clipboard" || method == "copy_clipboard" {
		size = MaxText
	}
	reports := (base64.StdEncoding.EncodedLen(size) + 1024 + ReportSize - reportHeader - 1) / (ReportSize - reportHeader)
	return 15*time.Second + time.Duration(reports)*10*time.Millisecond
}
