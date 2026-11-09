//go:build linux

package guestbridge

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// persistentSlot models f_hid: the SET_REPORT slot and IN queue survive the
// KVM closing and reopening /dev/hidg3 (f_hidg_open/release do not touch it).
type persistentSlot struct {
	control chan [ReportSize]byte
	input   chan [ReportSize]byte
}

type slotEnd struct {
	s    *persistentSlot
	host bool
	done chan struct{}
	once sync.Once
}

func (e *slotEnd) Close() error { e.once.Do(func() { close(e.done) }); return nil }
func (e *slotEnd) ReadReport(ctx context.Context) ([ReportSize]byte, error) {
	q := e.s.control
	if e.host {
		q = e.s.input
	}
	select {
	case v := <-q:
		return v, nil
	case <-ctx.Done():
		return [ReportSize]byte{}, ctx.Err()
	case <-e.done:
		return [ReportSize]byte{}, io.ErrClosedPipe
	}
}
func (e *slotEnd) WriteReport(ctx context.Context, v [ReportSize]byte) error {
	select {
	case <-e.done:
		return io.ErrClosedPipe
	default:
	}
	if e.host {
		select {
		case <-e.s.control: // overwrite
		default:
		}
		e.s.control <- v
		return nil
	}
	select {
	case e.s.input <- v:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.done:
		return io.ErrClosedPipe
	}
}

func TestCanceledGetRecoversAcrossPersistentKernelSlot(t *testing.T) {
	access, err := NewAccess(filepath.Join(t.TempDir(), "guest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.Rotate(); err != nil {
		t.Fatal(err)
	}
	key, _ := access.Snapshot()
	link := &persistentSlot{control: make(chan [ReportSize]byte, 1), input: make(chan [ReportSize]byte, 4096)}
	ctx, cancel := context.WithCancel(context.Background())
	manager := NewManager(access, func(context.Context) (Reports, error) {
		return AcknowledgeOutputReports(&slotEnd{s: link, done: make(chan struct{})}), nil
	})
	var workers sync.WaitGroup
	workers.Add(2)
	t.Cleanup(func() {
		cancel()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("recovery fixture did not stop")
		}
		clear(key)
	})
	go func() { defer workers.Done(); manager.Run(ctx) }()
	clip := strings.Repeat("a", 4096)
	// Mirror service_windows.go run(): reconnect loop with 1s retry.
	go func() {
		defer workers.Done()
		for ctx.Err() == nil {
			host := flowControlledOutputReports(&slotEnd{s: link, host: true, done: make(chan struct{})})
			hctx, hcancel := context.WithTimeout(ctx, 5*time.Second)
			channel, err := ClientHandshake(hctx, &Framer{IO: host}, key)
			hcancel()
			if err == nil {
				for {
					var request Request
					if err := channel.Receive(ctx, &request); err != nil {
						break
					}
					time.Sleep(300 * time.Millisecond) // worker latency
					response := Response{ID: request.ID, Unlocked: true}
					if request.Method == "get_clipboard" {
						response.Text, response.HasText = clip, true
					}
					if err := channel.Send(ctx, response); err != nil {
						break
					}
				}
			}
			host.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	waitConnected := func(limit time.Duration) time.Duration {
		start := time.Now()
		for time.Since(start) < limit {
			if manager.Status().Connected {
				return time.Since(start)
			}
			time.Sleep(10 * time.Millisecond)
		}
		return -1
	}
	if d := waitConnected(5 * time.Second); d < 0 {
		t.Fatal("initial connect failed")
	}
	// Viewer closes the popover / MCP client gives up during a get.
	short, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err = manager.GetClipboard(short)
	shortCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	start := time.Now()
	for time.Since(start) < 10*time.Second {
		gctx, gcancel := context.WithTimeout(context.Background(), 2*time.Second)
		if manager.Status().Connected {
			r, err := manager.GetClipboard(gctx)
			gcancel()
			if err == nil && r.Text == clip {
				t.Logf("next successful get after cancellation: %v (4 KiB clipboard = ~%d reports)", time.Since(start), (len(clip)+40)/44)
				return
			}
		} else {
			gcancel()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("guest channel still unusable %v after one canceled 4 KiB get", time.Since(start))
}
