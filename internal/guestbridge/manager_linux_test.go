//go:build linux

package guestbridge

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type deadlineReports struct {
	Reports
	deadlines chan time.Duration
}

func (r deadlineReports) WriteReport(ctx context.Context, report [ReportSize]byte) error {
	if deadline, ok := ctx.Deadline(); ok {
		select {
		case r.deadlines <- time.Until(deadline):
		default:
		}
	} else {
		select {
		case r.deadlines <- 0:
		default:
		}
	}
	return r.Reports.WriteReport(ctx, report)
}

func TestRequestsHaveDeadlineEvenWithBackgroundCaller(t *testing.T) {
	for _, method := range []string{"status", "get_clipboard"} {
		t.Run(method, func(t *testing.T) {
			a, b := newQueue(), newQueue()
			seen := make(chan time.Duration, 1)
			raw := deadlineReports{duplexReports{a, b}, seen}
			peer := duplexReports{b, a}
			key := bytes.Repeat([]byte{8}, 32)
			root, err := newSecure(&Framer{IO: raw}, key, true)
			if err != nil {
				t.Fatal(err)
			}
			client, err := newSecure(&Framer{IO: peer}, key, false)
			if err != nil {
				t.Fatal(err)
			}
			session := newDeviceSession(context.Background(), raw, root)
			defer session.close(context.Canceled)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var request Request
				if err := client.Receive(ctx, &request); err != nil {
					done <- err
					return
				}
				done <- client.Send(ctx, Response{ID: request.ID, Unlocked: true})
			}()
			if _, err := session.request(context.Background(), method, ""); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			budget := <-seen
			if budget <= 0 || budget > 120*time.Second {
				t.Fatalf("missing or excessive independent request deadline: %v", budget)
			}
			if method == "get_clipboard" && budget <= 15*time.Second {
				t.Fatal("large response has no transfer allowance")
			}
		})
	}
}

type testReportLink struct {
	in, out *reportQueue
	done    chan struct{}
	once    *sync.Once
}

func (r testReportLink) Close() error { r.once.Do(func() { close(r.done) }); return nil }
func (r testReportLink) ReadReport(ctx context.Context) ([ReportSize]byte, error) {
	select {
	case value := <-r.in.queue:
		return value, nil
	case <-ctx.Done():
		return [ReportSize]byte{}, ctx.Err()
	case <-r.done:
		return [ReportSize]byte{}, ErrUnavailable
	}
}
func (r testReportLink) WriteReport(ctx context.Context, value [ReportSize]byte) error {
	select {
	case r.out.queue <- value:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return ErrUnavailable
	}
}

func newTestManager(t *testing.T, respond func(context.Context, Request) Response, supportsCopy ...bool) (*Manager, *Access) {
	t.Helper()
	access, err := NewAccess(filepath.Join(t.TempDir(), "guest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.Rotate(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	manager := NewManager(access, func(ctx context.Context) (Reports, error) {
		a, b := newQueue(), newQueue()
		closed := make(chan struct{})
		once := &sync.Once{}
		root, peer := testReportLink{a, b, closed, once}, testReportLink{b, a, closed, once}
		key, _ := access.Snapshot()
		go func() {
			defer clear(key)
			channel, err := ClientHandshake(ctx, &Framer{IO: peer}, key)
			if err != nil {
				return
			}
			for {
				var request Request
				if channel.Receive(ctx, &request) != nil {
					return
				}
				var response Response
				if request.Method == "clipboard_capabilities" {
					response = Response{ID: request.ID, NativeCopy: true}
					if len(supportsCopy) > 0 && !supportsCopy[0] {
						response.NativeCopy = false
						response.Error = "unsupported guest clipboard operation"
					}
				} else {
					response = respond(ctx, request)
				}
				if channel.Send(ctx, response) != nil {
					return
				}
			}
		}()
		return root, nil
	})
	go func() { defer close(done); manager.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("guest manager failed to stop")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for !manager.Status().Connected && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !manager.Status().Connected {
		t.Fatal("simulated guest did not authenticate")
	}
	return manager, access
}

func TestManagerClipboardPasteTransactionAndCancellation(t *testing.T) {
	var mu sync.Mutex
	clipboard := ""
	manager, _ := newTestManager(t, func(_ context.Context, request Request) Response {
		mu.Lock()
		defer mu.Unlock()
		response := Response{ID: request.ID, Unlocked: true, User: "DOMAIN\\user"}
		if request.Method == "set_clipboard" {
			clipboard = request.Text
		}
		if request.Method == "get_clipboard" {
			response.Text = clipboard
			response.HasText = true
		}
		return response
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- manager.WithClipboard(ctx, "日本語 🐕", func(context.Context) error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	short, shortCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	err := manager.SetClipboard(short, "must not interleave")
	shortCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("queued operation was not canceled", err)
	}
	mu.Lock()
	got := clipboard
	mu.Unlock()
	if got != "日本語 🐕" {
		t.Fatal("clipboard changed during paste transaction")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	response, err := manager.GetClipboard(ctx)
	if err != nil || !response.HasText || response.Text != got {
		t.Fatal("clipboard round trip", err)
	}
	if strings.Contains(manager.Status().Message, got) {
		t.Fatal("clipboard cached in status")
	}
}

func TestManagerRevocationCancelsPendingCall(t *testing.T) {
	started := make(chan struct{})
	manager, access := newTestManager(t, func(ctx context.Context, request Request) Response {
		close(started)
		<-ctx.Done()
		return Response{ID: request.ID}
	})
	done := make(chan error, 1)
	go func() { done <- manager.SetClipboard(context.Background(), "pending") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	if err := access.Disable(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked operation succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel operation")
	}
	if state := manager.Status(); state.Enabled || state.Connected || state.User != "" {
		t.Fatal("disabled status retained access")
	}
}

func TestManagerLockedAndUntrustedErrors(t *testing.T) {
	manager, _ := newTestManager(t, func(_ context.Context, request Request) Response {
		return Response{ID: request.ID, Error: ErrLocked.Error()}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := manager.GetClipboard(ctx); err == nil || !strings.Contains(err.Error(), "unlocked") {
		t.Fatal("locked clipboard exposed", err)
	}
	if state := manager.CheckStatus(ctx); !state.Connected || state.Unlocked {
		t.Fatal("incorrect locked status")
	}
	untrusted, _ := newTestManager(t, func(_ context.Context, request Request) Response {
		return Response{ID: request.ID, Error: "sensitive target data"}
	})
	if _, err := untrusted.GetClipboard(ctx); err == nil || strings.Contains(err.Error(), "sensitive target") {
		t.Fatal("untrusted error propagated", err)
	}
}

func TestManagerRevocationRejectsAlreadyPublishedSessionImmediately(t *testing.T) {
	manager, access := newTestManager(t, func(_ context.Context, r Request) Response { return Response{ID: r.ID, Unlocked: true} })
	manager.mu.Lock()
	old := manager.current
	manager.mu.Unlock()
	if err := access.Disable(); err != nil {
		t.Fatal(err)
	}
	// Simulate replacement access becoming enabled before asynchronous cleanup
	// catches up. The old generation itself must reject requests and status.
	if _, err := access.Rotate(); err != nil {
		t.Fatal(err)
	}
	old.cancel(context.Canceled)
	if !old.revoked() {
		t.Fatal("old generation survived replacement")
	}
	if _, err := old.request(context.Background(), "get_clipboard", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("revoked session request: %v", err)
	}
}
