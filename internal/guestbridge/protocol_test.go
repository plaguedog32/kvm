package guestbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

type reportQueue struct {
	queue chan [ReportSize]byte
}

func newQueue() *reportQueue        { return &reportQueue{queue: make(chan [ReportSize]byte, 32768)} }
func (q *reportQueue) Close() error { return nil }
func (q *reportQueue) ReadReport(ctx context.Context) ([ReportSize]byte, error) {
	select {
	case report := <-q.queue:
		return report, nil
	case <-ctx.Done():
		return [ReportSize]byte{}, ctx.Err()
	}
}
func (q *reportQueue) WriteReport(ctx context.Context, r [ReportSize]byte) error {
	select {
	case q.queue <- r:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestFramingBoundsAndWakeReports(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	q := newQueue()
	f := &Framer{IO: q}
	q.queue <- [ReportSize]byte{1}
	want := bytes.Repeat([]byte("Unicode: 日本語 🚀\n"), 10000)
	if err := f.Write(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := f.Read(ctx, MaxWire)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("round trip: %v", err)
	}
	if err := f.Write(ctx, make([]byte, MaxWire+1)); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
	if err := f.Write(ctx, nil); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
	if err := f.Write(ctx, []byte("too long")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read(ctx, 2); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
}

func TestMalformedReports(t *testing.T) {
	for _, field := range []string{"magic", "version", "reserved", "size", "offset", "total", "id"} {
		t.Run(field, func(t *testing.T) {
			q := newQueue()
			f := &Framer{IO: q}
			if err := f.Write(context.Background(), []byte("test")); err != nil {
				t.Fatal(err)
			}
			r := <-q.queue
			switch field {
			case "magic":
				r[0]++
			case "version":
				r[4]++
			case "reserved":
				r[5]++
			case "size":
				binary.BigEndian.PutUint16(r[6:8], 45)
			case "offset":
				binary.BigEndian.PutUint32(r[12:16], 1)
			case "total":
				binary.BigEndian.PutUint32(r[16:20], MaxWire+1)
			case "id":
				binary.BigEndian.PutUint32(r[8:12], 0)
			}
			q.queue <- r
			if _, err := f.Read(context.Background(), MaxWire); !errors.Is(err, ErrProtocol) {
				t.Fatal(err)
			}
		})
	}
}

type duplexReports struct{ in, out *reportQueue }

func (d duplexReports) Close() error { return nil }
func (d duplexReports) ReadReport(ctx context.Context) ([ReportSize]byte, error) {
	return d.in.ReadReport(ctx)
}
func (d duplexReports) WriteReport(ctx context.Context, r [ReportSize]byte) error {
	return d.out.WriteReport(ctx, r)
}

func TestMutualHandshakeAndUnicode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a, b := newQueue(), newQueue()
	key := bytes.Repeat([]byte{7}, 32)
	serverResult := make(chan *Secure, 1)
	errors := make(chan error, 1)
	go func() {
		s, err := ServerHandshake(ctx, &Framer{IO: duplexReports{a, b}}, key)
		if err != nil {
			errors <- err
			return
		}
		serverResult <- s
	}()
	client, err := ClientHandshake(ctx, &Framer{IO: duplexReports{b, a}}, key)
	if err != nil {
		t.Fatal(err)
	}
	var server *Secure
	select {
	case server = <-serverResult:
	case err := <-errors:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	want := Request{ID: 1, Method: "set_clipboard", Text: "こんにちは 👋\r\nsecond line"}
	if err := server.Send(ctx, want); err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := client.Receive(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v", got)
	}
	response := Response{ID: 1, Unlocked: true, User: "DOMAIN\\user"}
	if err := client.Send(ctx, response); err != nil {
		t.Fatal(err)
	}
	var received Response
	if err := server.Receive(ctx, &received); err != nil || received != response {
		t.Fatalf("response: %+v, %v", received, err)
	}
}

func TestWrongPairingKeyRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, b := newQueue(), newQueue()
	done := make(chan error, 1)
	go func() {
		_, err := ClientHandshake(ctx, &Framer{IO: duplexReports{b, a}}, bytes.Repeat([]byte{1}, 32))
		done <- err
	}()
	if _, err := ServerHandshake(ctx, &Framer{IO: duplexReports{a, b}}, bytes.Repeat([]byte{2}, 32)); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("unpaired client authenticated")
	}
}

func TestEncryptionTamperingReplayAndDirection(t *testing.T) {
	ctx := context.Background()
	key := bytes.Repeat([]byte{3}, 32)
	q := newQueue()
	frame := &Framer{IO: q}
	server, err := newSecure(frame, key, true)
	if err != nil {
		t.Fatal(err)
	}
	secret := "private clipboard content"
	if err := server.Send(ctx, Request{ID: 1, Method: "set_clipboard", Text: secret}); err != nil {
		t.Fatal(err)
	}
	wire, err := frame.Read(ctx, MaxWire)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte(secret)) {
		t.Fatal("clipboard sent in plaintext")
	}
	for _, attack := range []string{"ciphertext", "tag", "kind", "sequence", "reflection", "wrong key"} {
		t.Run(attack, func(t *testing.T) {
			data := bytes.Clone(wire)
			readKey := bytes.Clone(key)
			isServer := false
			switch attack {
			case "ciphertext":
				data[9] ^= 1
			case "tag":
				data[len(data)-1] ^= 1
			case "kind":
				data[0] = responseKind
			case "sequence":
				binary.BigEndian.PutUint64(data[1:9], 2)
			case "reflection":
				isServer = true
			case "wrong key":
				readKey[0] ^= 1
			}
			q := newQueue()
			f := &Framer{IO: q}
			client, err := newSecure(f, readKey, isServer)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Write(ctx, data); err != nil {
				t.Fatal(err)
			}
			if err := client.Receive(ctx, new(Request)); !errors.Is(err, ErrProtocol) {
				t.Fatal(err)
			}
		})
	}
	client, err := newSecure(frame, key, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Write(ctx, wire); err != nil {
		t.Fatal(err)
	}
	if err := client.Receive(ctx, new(Request)); err != nil {
		t.Fatal(err)
	}
	if err := frame.Write(ctx, wire); err != nil {
		t.Fatal(err)
	}
	if err := client.Receive(ctx, new(Request)); !errors.Is(err, ErrProtocol) {
		t.Fatal("replay accepted", err)
	}
	server.sendSequence = math.MaxUint32
	if err := server.Send(ctx, Request{}); !errors.Is(err, ErrProtocol) {
		t.Fatal("nonce budget exceeded", err)
	}
}

func TestClipboardTextValidation(t *testing.T) {
	for _, invalid := range []string{"embedded\x00nul", string([]byte{0xff}), strings.Repeat("a", MaxText+1)} {
		if ValidateText(invalid) == nil {
			t.Fatal("invalid clipboard accepted")
		}
	}
	for _, valid := range []string{"", "日本語 🐕", strings.Repeat("a", MaxText)} {
		if err := ValidateText(valid); err != nil {
			t.Fatal(err)
		}
	}
}
