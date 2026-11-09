package guestbridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Model f_hid SET_REPORT's single slot rather than a lossless fake queue.
type singleSlotLink struct {
	control    chan [ReportSize]byte
	input      chan [ReportSize]byte
	done       chan struct{}
	once       sync.Once
	overwrites atomic.Int64
}
type slotReports struct {
	link *singleSlotLink
	host bool
}

func newSlotLink() (*singleSlotLink, Reports, Reports) {
	l := &singleSlotLink{control: make(chan [ReportSize]byte, 1), input: make(chan [ReportSize]byte, 512), done: make(chan struct{})}
	return l, slotReports{l, true}, slotReports{l, false}
}
func (r slotReports) Close() error { r.link.once.Do(func() { close(r.link.done) }); return nil }
func (r slotReports) ReadReport(ctx context.Context) ([ReportSize]byte, error) {
	queue := r.link.control
	if r.host {
		queue = r.link.input
	}
	select {
	case value := <-queue:
		return value, nil
	case <-ctx.Done():
		return [ReportSize]byte{}, ctx.Err()
	case <-r.link.done:
		return [ReportSize]byte{}, io.ErrClosedPipe
	}
}
func (r slotReports) WriteReport(ctx context.Context, value [ReportSize]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.host {
		select {
		case r.link.control <- value:
			return nil
		default:
		}
		select {
		case <-r.link.control:
			r.link.overwrites.Add(1)
		default:
		}
		select {
		case r.link.control <- value:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-r.link.done:
			return io.ErrClosedPipe
		}
	}
	select {
	case r.link.input <- value:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.link.done:
		return io.ErrClosedPipe
	}
}

func TestSingleSlotRequiresOutputAcknowledgements(t *testing.T) {
	link, host, device := newSlotLink()
	defer host.Close()
	if err := (&Framer{IO: host}).Write(context.Background(), bytes.Repeat([]byte{3}, 100)); err != nil {
		t.Fatal(err)
	}
	if link.overwrites.Load() == 0 {
		t.Fatal("test transport did not model overwrite")
	}
	if _, err := (&Framer{IO: device}).Read(context.Background(), MaxWire); !errors.Is(err, ErrProtocol) {
		t.Fatal("missing report was not detected", err)
	}
}

func TestAcknowledgedHandshakeAndWorstCaseBulkOnSingleSlot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	link, rawHost, rawDevice := newSlotLink()
	host := flowControlledOutputReports(rawHost)
	defer host.Close()
	device := AcknowledgeOutputReports(rawDevice)
	defer device.Close()
	key := bytes.Repeat([]byte{9}, 32)
	type result struct {
		secure *Secure
		err    error
	}
	established := make(chan result, 1)
	go func() {
		secure, err := ServerHandshake(ctx, &Framer{IO: device}, key)
		established <- result{secure, err}
	}()
	client, err := ClientHandshake(ctx, &Framer{IO: host}, key)
	if err != nil {
		t.Fatal(err)
	}
	server := <-established
	if server.err != nil {
		t.Fatal(server.err)
	}
	// Control characters, Unicode and line endings must stay bounded on the wire.
	want := Response{ID: 7, Unlocked: true, HasText: true, Text: strings.Repeat("\x01", MaxText-16) + "日本語\r\n🐕"}
	received := make(chan result, 1)
	var got Response
	go func() { received <- result{err: server.secure.Receive(ctx, &got)} }()
	if err := client.Send(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := (<-received).err; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("bulk clipboard content changed")
	}
	if count := link.overwrites.Load(); count != 0 {
		t.Fatalf("overwrote %d control reports", count)
	}
}

func TestMissingAcknowledgementCancelsAndClosesTransport(t *testing.T) {
	_, rawHost, _ := newSlotLink()
	host := flowControlledOutputReports(rawHost)
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := (&Framer{IO: host}).Write(ctx, []byte("no receiver")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := host.ReadReport(context.Background()); err == nil {
		t.Fatal("unacknowledged transport remained usable")
	}
}

func TestResetImmediatelyReplacesInitDiscardedByStaleSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, rawHost, rawDevice := newSlotLink()
	host := flowControlledOutputReports(rawHost)
	defer host.Close()
	device := AcknowledgeOutputReports(rawDevice)
	defer device.Close()
	key := bytes.Repeat([]byte{11}, 32)
	done := make(chan error, 1)
	go func() {
		frame := &Framer{IO: device}
		// The old authenticated receiver consumed this new client's init before
		// noticing the old session was gone. Recovery must not wait five seconds.
		if _, err := frame.Read(ctx, 128); err != nil {
			done <- err
			return
		}
		_, err := ServerHandshake(ctx, frame, key)
		done <- err
	}()
	if _, err := ClientHandshake(ctx, &Framer{IO: host}, key); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
