//go:build linux

package guestbridge

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestSuspendedTargetDoesNotOpenHandshakeOrAcceptOperations(t *testing.T) {
	access, err := NewAccess(filepath.Join(t.TempDir(), "guest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.Rotate(); err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	manager := NewManagerWithSleepCheck(access, func(context.Context) (Reports, error) { opens.Add(1); return nil, ErrUnavailable }, func() bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); manager.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	if opens.Load() != 0 {
		t.Fatal("suspended target opened a handshake")
	}
	if _, err := manager.GetClipboard(context.Background()); !errors.Is(err, ErrAsleep) {
		t.Fatal("sleeping guest operation did not fail fast", err)
	}
	if status := manager.CheckStatus(context.Background()); status.Connected || status.Message != ErrAsleep.Error() {
		t.Fatal("sleep status attempted to refresh the guest", status)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sleeping manager did not stop")
	}
}

func TestEverySuspendedWriteIncludingAcknowledgementsIsBlocked(t *testing.T) {
	link, host, device := newSlotLink()
	defer host.Close()
	guarded := WhileAwakeReports(device, func() bool { return true })
	if err := guarded.WriteReport(context.Background(), [ReportSize]byte{}); !errors.Is(err, ErrAsleep) {
		t.Fatal(err)
	}
	frame := &Framer{IO: host}
	if err := frame.Write(context.Background(), []byte("request")); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Framer{IO: AcknowledgeOutputReports(guarded)}).Read(context.Background(), 128); !errors.Is(err, ErrAsleep) {
		t.Fatal("ACK bypassed sleep guard", err)
	}
	if len(link.input) != 0 {
		t.Fatal("wrote to suspended target")
	}
}
