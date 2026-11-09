package kvm

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jetkvm/kvm/internal/guestbridge"
)

const guestChunkSize = 16384

// Track the last observed local clipboard with a per-viewer keyed digest.
// Reads offer a separate one-time delivery confirmation; they never advance
// synchronization until the browser confirms a successful local clipboard write.
type guestClipboardExport struct {
	pendingID      string
	pendingDigest  []byte
	pendingPairing context.Context
	pendingUntil   time.Time
	mu             sync.Mutex
	key            [32]byte
	digest         []byte
	pairingCtx     context.Context
}

func (e *guestClipboardExport) sum(text string) []byte {
	h := hmac.New(sha256.New, e.key[:])
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	h.Write([]byte(text))
	return h.Sum(nil)
}
func (e *guestClipboardExport) remember(text string, pairing context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.digest = e.sum(text)
	e.pairingCtx = pairing
	e.pendingID = ""
	e.pendingDigest = nil
	e.pendingPairing = nil
}
func (e *guestClipboardExport) offer(text, id string, pairing context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingID = id
	e.pendingDigest = e.sum(text)
	e.pendingPairing = pairing
	e.pendingUntil = time.Now().Add(2 * time.Minute)
}
func (e *guestClipboardExport) confirm(id string, pairing context.Context) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == "" || id != e.pendingID || pairing != e.pendingPairing || pairing.Err() != nil || time.Now().After(e.pendingUntil) {
		return false
	}
	e.digest = e.pendingDigest
	e.pairingCtx = pairing
	e.pendingDigest = nil
	e.pendingID = ""
	e.pendingPairing = nil
	return true
}
func (e *guestClipboardExport) matches(text string, pairing context.Context) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pairingCtx == pairing && pairing.Err() == nil && hmac.Equal(e.digest, e.sum(text))
}

// Jobs belong to one authenticated viewer and expire even if its UI disappears.
// Only explicit get requests read the remote clipboard; no history is retained.
type guestClipboardJob struct {
	busy           bool
	interactive    bool
	preferExisting bool
	export         *guestClipboardExport
	unchanged      bool
	pairingCtx     context.Context
	mu             sync.Mutex
	id             string
	ctx            context.Context
	cancel         context.CancelFunc
	method         string
	data           string
	started        bool
	done           bool
	failed         bool
	hasText        bool
}
type guestClipboardArgs struct {
	Interactive    bool   `json:"interactive"`
	PreferExisting bool   `json:"prefer_existing"`
	Action         string `json:"action"`
	ID             string `json:"id"`
	Offset         int    `json:"offset"`
	Data           string `json:"data"`
}
type guestClipboardResult struct {
	Busy      bool   `json:"busy,omitempty"`
	Unchanged bool   `json:"unchanged,omitempty"`
	ID        string `json:"id,omitempty"`
	Done      bool   `json:"done"`
	Failed    bool   `json:"failed"`
	Data      string `json:"data,omitempty"`
	Length    int    `json:"length"`
	HasText   bool   `json:"has_text"`
}

func rpcGuestStatus() guestbridge.Status { return getGuestService().manager.Status() }
func rpcGuestClipboard(session *Session, args guestClipboardArgs) (guestClipboardResult, error) {
	if session == nil {
		return guestClipboardResult{}, errors.New("viewer session required")
	}
	session.guestClipboardMu.Lock()
	defer session.guestClipboardMu.Unlock()
	if args.Action == "confirm" {
		if !getGuestService().access.Enabled() || session.guestClipboardExport == nil {
			return guestClipboardResult{}, guestbridge.ErrUnavailable
		}
		key, pairing := getGuestService().access.Snapshot()
		clear(key)
		if !session.guestClipboardExport.confirm(args.ID, pairing) {
			return guestClipboardResult{}, errors.New("clipboard delivery confirmation expired")
		}
		return guestClipboardResult{Done: true}, nil
	}
	if args.Action == "start_get" || args.Action == "start_copy" || args.Action == "start_set" || args.Action == "start_paste" || args.Action == "start_paste_existing" {
		if !getGuestService().access.Enabled() {
			return guestClipboardResult{}, guestbridge.ErrUnavailable
		}
		if session.guestClipboard != nil {
			session.guestClipboard.cancel()
		}
		ctx, cancel := context.WithTimeout(appCtx, 3*time.Minute)
		key, pairingCtx := getGuestService().access.Snapshot()
		clear(key)
		stop := context.AfterFunc(pairingCtx, cancel)
		if session.guestClipboardExport == nil {
			e := &guestClipboardExport{}
			if _, err := rand.Read(e.key[:]); err != nil {
				cancel()
				stop()
				return guestClipboardResult{}, err
			}
			session.guestClipboardExport = e
		}
		job := &guestClipboardJob{interactive: args.Interactive, preferExisting: args.PreferExisting, export: session.guestClipboardExport, id: uuid.NewString(), ctx: ctx, cancel: cancel, method: args.Action, pairingCtx: pairingCtx}
		session.guestClipboard = job
		go func() {
			select {
			case <-session.done:
				cancel()
			case <-ctx.Done():
			}
			<-ctx.Done()
			stop()
			job.mu.Lock()
			job.data = ""
			job.mu.Unlock()
		}()
		if args.Action == "start_get" || args.Action == "start_copy" || args.Action == "start_paste_existing" {
			job.started = true
			go job.run("")
		}
		return guestClipboardResult{ID: job.id}, nil
	}
	job := session.guestClipboard
	if job == nil || args.ID != job.id || job.ctx.Err() != nil || job.pairingCtx.Err() != nil {
		return guestClipboardResult{}, errors.New("clipboard transfer expired")
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	switch args.Action {
	case "cancel":
		job.cancel()
		job.data = ""
		session.guestClipboard = nil
		return guestClipboardResult{Done: true}, nil
	case "upload":
		if job.started || args.Offset != len(job.data) || len(args.Data) > guestChunkSize || len(job.data)+len(args.Data) > base64.StdEncoding.EncodedLen(guestbridge.MaxText) {
			return guestClipboardResult{}, errors.New("invalid clipboard upload")
		}
		for _, c := range args.Data {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=') {
				return guestClipboardResult{}, errors.New("invalid clipboard upload")
			}
		}
		job.data += args.Data
	case "commit":
		if job.started {
			return guestClipboardResult{}, errors.New("clipboard transfer already started")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(job.data)
		job.data = ""
		if err != nil || guestbridge.ValidateText(string(data)) != nil {
			return guestClipboardResult{}, errors.New("invalid clipboard text")
		}
		job.started = true
		go job.run(string(data))
		clear(data)
	case "poll":
	case "read":
		if !job.done || job.failed || (job.method != "start_get" && job.method != "start_copy") || args.Offset < 0 || args.Offset > len(job.data) {
			return guestClipboardResult{}, errors.New("invalid clipboard read")
		}
		end := args.Offset + guestChunkSize
		if end > len(job.data) {
			end = len(job.data)
		}
		return guestClipboardResult{ID: job.id, Done: true, Data: job.data[args.Offset:end], Length: len(job.data), HasText: job.hasText}, nil
	default:
		return guestClipboardResult{}, errors.New("invalid clipboard transfer action")
	}
	return guestClipboardResult{Busy: job.busy, Unchanged: job.unchanged, ID: job.id, Done: job.done, Failed: job.failed, Length: len(job.data), HasText: job.hasText}, nil
}
func (job *guestClipboardJob) run(text string) {
	manager := getGuestService().manager
	ctx := job.ctx
	if job.interactive {
		ctx = guestbridge.InteractiveContext(ctx)
	}
	var data string
	var hasText bool
	var err error
	switch job.method {
	case "start_get", "start_copy":
		var response guestbridge.Response
		if job.method == "start_copy" {
			if err = mcpHIDReady(); err == nil {
				response, err = manager.CopyClipboard(ctx, func(ctx context.Context) error { return guestShortcut(ctx, 0x06) })
			}
		} else {
			response, err = manager.GetClipboard(ctx)
		}
		if err == nil {
			data = base64.StdEncoding.EncodeToString([]byte(response.Text))
			hasText = response.HasText
			if hasText {
				job.export.offer(response.Text, job.id, job.pairingCtx)
			}
		}
	case "start_set":
		err = manager.SetClipboard(ctx, text)
	case "start_paste":
		err = mcpHIDReady()
		if err == nil {
			paste := func(ctx context.Context) error { return guestShortcut(ctx, 0x19) }
			if job.preferExisting || job.export.matches(text, job.pairingCtx) {
				err = manager.WithExistingClipboard(ctx, paste)
			} else {
				err = manager.WithClipboard(ctx, text, paste)
			}
		}
	case "start_paste_existing":
		if err = mcpHIDReady(); err == nil {
			err = manager.WithExistingClipboard(ctx, func(ctx context.Context) error { return guestShortcut(ctx, 0x19) })
		}
	}
	if err == nil && (job.method == "start_set" || job.method == "start_paste") {
		job.export.remember(text, job.pairingCtx)
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.ctx.Err() != nil || job.pairingCtx.Err() != nil {
		return
	}
	job.done = true
	job.busy = errors.Is(err, guestbridge.ErrInteractiveBusy)
	job.unchanged = errors.Is(err, guestbridge.ErrNoClipboardChange)
	job.failed = err != nil && !job.unchanged
	job.data = data
	job.hasText = hasText
}

func guestShortcut(ctx context.Context, key byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer gadget.KeyboardReport(0, []byte{0, 0, 0, 0, 0, 0})
	if err := gadget.KeyboardReport(1, []byte{key, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
