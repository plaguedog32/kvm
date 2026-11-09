// Package guestbridge implements the paired USB clipboard channel. It does not
// provide command execution or automatic clipboard synchronization.
package guestbridge

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	ReportSize = 64
	MaxText    = 256 * 1024
	// Clipboard text is base64 on the wire, bounding expansion to 4/3.
	MaxWire           = 512 * 1024
	reportHeader      = 20
	resetKind    byte = 4
	requestKind  byte = 16
	responseKind byte = 17
)

var ErrAsleep = errors.New("target is asleep; wake the host explicitly before guest operations")

var ErrProtocol = errors.New("invalid guest channel message")
var ErrLocked = errors.New("guest clipboard requires an unlocked physical console session")

type Reports interface {
	ReadReport(context.Context) ([ReportSize]byte, error)
	WriteReport(context.Context, [ReportSize]byte) error
	Close() error
}

// Framer serializes whole messages while permitting unrelated wake reports.
type Framer struct {
	IO      Reports
	writeMu sync.Mutex
	readMu  sync.Mutex
	next    uint32
}

func (f *Framer) Write(ctx context.Context, data []byte) error {
	if len(data) == 0 || len(data) > MaxWire {
		return ErrProtocol
	}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	f.next++
	if f.next == 0 {
		f.next++
	}
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		var report [ReportSize]byte
		copy(report[:4], "JKG1")
		report[4] = 1
		n := min(ReportSize-reportHeader, len(data)-offset)
		binary.BigEndian.PutUint16(report[6:8], uint16(n))
		binary.BigEndian.PutUint32(report[8:12], f.next)
		binary.BigEndian.PutUint32(report[12:16], uint32(offset))
		binary.BigEndian.PutUint32(report[16:20], uint32(len(data)))
		copy(report[reportHeader:], data[offset:offset+n])
		if err := f.IO.WriteReport(ctx, report); err != nil {
			return err
		}
		offset += n
	}
	return nil
}

func (f *Framer) Read(ctx context.Context, limit int) ([]byte, error) {
	f.readMu.Lock()
	defer f.readMu.Unlock()
	if limit <= 0 || limit > MaxWire {
		return nil, ErrProtocol
	}
	var data []byte
	var id, total uint32
	var last time.Time
	for {
		readCtx := ctx
		cancel := func() {}
		if data != nil {
			readCtx, cancel = context.WithDeadline(ctx, last.Add(2*time.Second))
		}
		report, err := f.IO.ReadReport(readCtx)
		cancel()
		if err != nil {
			return nil, err
		}
		// The pre-existing vendor wake interface uses a one-byte boolean report.
		if report[0] <= 1 && bytes.Equal(report[1:], make([]byte, ReportSize-1)) {
			continue
		}
		if string(report[:4]) != "JKG1" || report[4] != 1 || report[5] != 0 {
			return nil, ErrProtocol
		}
		n := int(binary.BigEndian.Uint16(report[6:8]))
		rid := binary.BigEndian.Uint32(report[8:12])
		offset := binary.BigEndian.Uint32(report[12:16])
		rtotal := binary.BigEndian.Uint32(report[16:20])
		if rid == 0 || n <= 0 || n > ReportSize-reportHeader || rtotal == 0 || uint64(rtotal) > uint64(limit) {
			return nil, ErrProtocol
		}
		if data == nil {
			if offset != 0 {
				return nil, ErrProtocol
			}
			id, total = rid, rtotal
			data = make([]byte, 0, int(total))
		}
		if rid != id || rtotal != total || uint64(offset) != uint64(len(data)) || uint64(len(data))+uint64(n) > uint64(total) {
			return nil, ErrProtocol
		}
		if receiver, ok := f.IO.(interface {
			Acknowledge(context.Context, [ReportSize]byte) error
		}); ok {
			if err := receiver.Acknowledge(ctx, report); err != nil {
				return nil, err
			}
		}
		data = append(data, report[reportHeader:reportHeader+n]...)
		last = time.Now()
		if len(data) == int(total) {
			return data, nil
		}
	}
}

func proof(key []byte, role string, pieces ...[]byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("JetKVM guest v1\x00" + role + "\x00"))
	for _, piece := range pieces {
		h.Write(piece)
	}
	return h.Sum(nil)
}

type Secure struct {
	frame                         *Framer
	sendCipher, receiveCipher     cipher.AEAD
	sendKind, receiveKind         byte
	sendMu, receiveMu             sync.Mutex
	sendSequence, receiveSequence uint64
}

// Each direction has a separate encryption key; the sequence number is a
// unique GCM nonce under that key. A channel must be discarded after an error.
func newSecure(f *Framer, sessionKey []byte, server bool) (*Secure, error) {
	requestKey := proof(sessionKey, "request encryption")
	responseKey := proof(sessionKey, "response encryption")
	requestBlock, err := aes.NewCipher(requestKey)
	if err != nil {
		return nil, err
	}
	responseBlock, err := aes.NewCipher(responseKey)
	if err != nil {
		return nil, err
	}
	requestCipher, err := cipher.NewGCM(requestBlock)
	if err != nil {
		return nil, err
	}
	responseCipher, err := cipher.NewGCM(responseBlock)
	if err != nil {
		return nil, err
	}
	s := &Secure{frame: f, sendKind: responseKind, receiveKind: requestKind, sendCipher: responseCipher, receiveCipher: requestCipher}
	if server {
		s.sendKind, s.receiveKind = requestKind, responseKind
		s.sendCipher, s.receiveCipher = requestCipher, responseCipher
	}
	return s, nil
}

func frameNonce(sequence uint64) []byte {
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], sequence)
	return nonce[:]
}

func frameAAD(header []byte) []byte {
	return append([]byte("JetKVM guest v1 frame\x00"), header...)
}

func ClientHandshake(ctx context.Context, f *Framer, key []byte) (*Secure, error) {
	if len(key) != 32 {
		return nil, ErrProtocol
	}
	for attempt := 0; attempt < 5; attempt++ {
		nonce := make([]byte, 32)
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		init := append([]byte{1}, nonce...)
		init = append(init, proof(key, "client", nonce)...)
		if err := f.Write(ctx, init); err != nil {
			return nil, err
		}
		for stale := 0; stale < 5; stale++ {
			hello, err := f.Read(ctx, 128)
			if err != nil {
				return nil, err
			}
			if len(hello) == 1 && hello[0] == resetKind {
				break
			} // New attempt, fresh nonce and init.
			if len(hello) != 65 || hello[0] != 2 {
				return nil, ErrProtocol
			}
			// A hello for the init sent just before reset can already be queued.
			if !hmac.Equal(hello[33:], proof(key, "server", nonce, hello[1:33])) {
				continue
			}
			sessionKey := proof(key, "session", nonce, hello[1:33])
			if err := f.Write(ctx, append([]byte{3}, proof(sessionKey, "ready")...)); err != nil {
				return nil, err
			}
			return newSecure(f, sessionKey, false)
		}
	}
	return nil, ErrProtocol
}

func ServerHandshake(ctx context.Context, f *Framer, key []byte) (*Secure, error) {
	return serverHandshake(ctx, f, key, true)
}

func serverHandshake(ctx context.Context, f *Framer, key []byte, reset bool) (*Secure, error) {
	if len(key) != 32 {
		return nil, ErrProtocol
	}
	// A firmware restart can preserve the USB gadget while losing session
	// keys. Prompt an existing client to reauthenticate, without periodic
	// heartbeats that would wake an idle target. Reset is never accepted as
	// authenticated data; a new client tolerates it while awaiting our hello.
	if reset {
		if err := f.Write(ctx, []byte{resetKind}); err != nil {
			return nil, err
		}
	}
	init, err := f.Read(ctx, 128)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 5; attempt++ {
		if len(init) != 65 || init[0] != 1 || !hmac.Equal(init[33:], proof(key, "client", init[1:33])) {
			return nil, ErrProtocol
		}
		nonce := make([]byte, 32)
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		hello := append([]byte{2}, nonce...)
		hello = append(hello, proof(key, "server", init[1:33], nonce)...)
		if err := f.Write(ctx, hello); err != nil {
			return nil, err
		}
		sessionKey := proof(key, "session", init[1:33], nonce)
		ready, err := f.Read(ctx, 128)
		if err != nil {
			return nil, err
		}
		// Reset may cross an init in flight. Accept the client's fresh replacement
		// init instead of discarding it and waiting through both retry timeouts.
		if len(ready) == 65 && ready[0] == 1 {
			init = ready
			continue
		}
		if len(ready) != 33 || ready[0] != 3 || !hmac.Equal(ready[1:], proof(sessionKey, "ready")) {
			return nil, ErrProtocol
		}
		return newSecure(f, sessionKey, true)
	}
	return nil, ErrProtocol
}

func (s *Secure) Send(ctx context.Context, value any) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if encoded.Len() > MaxWire-9-s.sendCipher.Overhead() || s.sendSequence >= math.MaxUint32 {
		return ErrProtocol
	}
	s.sendSequence++
	data := make([]byte, 9, 9+encoded.Len()+s.sendCipher.Overhead())
	data[0] = s.sendKind
	binary.BigEndian.PutUint64(data[1:], s.sendSequence)
	data = s.sendCipher.Seal(data, frameNonce(s.sendSequence), encoded.Bytes(), frameAAD(data))
	return s.frame.Write(ctx, data)
}

func (s *Secure) Receive(ctx context.Context, value any) error {
	s.receiveMu.Lock()
	defer s.receiveMu.Unlock()
	data, err := s.frame.Read(ctx, MaxWire)
	if err != nil {
		return err
	}
	if len(data) < 9+s.receiveCipher.Overhead() || data[0] != s.receiveKind || s.receiveSequence >= math.MaxUint32 || binary.BigEndian.Uint64(data[1:9]) != s.receiveSequence+1 {
		return ErrProtocol
	}
	payload, err := s.receiveCipher.Open(nil, frameNonce(s.receiveSequence+1), data[9:], frameAAD(data[:9]))
	if err != nil {
		return ErrProtocol
	}
	if !utf8.Valid(payload) {
		return ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("guest JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrProtocol
	}
	s.receiveSequence++
	return nil
}

type Request struct {
	ID     uint64 `json:"id"`
	Method string `json:"method"`
	Text   string `json:"text,omitempty"`
}

type Response struct {
	NativeCopy        bool   `json:"native_copy,omitempty"`
	ClipboardSequence uint32 `json:"clipboard_sequence,omitempty"`
	ID                uint64 `json:"id"`
	Error             string `json:"error,omitempty"`
	Text              string `json:"text,omitempty"`
	HasText           bool   `json:"has_text,omitempty"`
	User              string `json:"user,omitempty"`
	Unlocked          bool   `json:"unlocked"`
}

func ValidateText(text string) error {
	if len(text) > MaxText {
		return fmt.Errorf("clipboard text exceeds %d UTF-8 bytes", MaxText)
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return errors.New("clipboard text must be valid Unicode without NUL characters")
	}
	return nil
}
