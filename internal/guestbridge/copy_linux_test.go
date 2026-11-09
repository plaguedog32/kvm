//go:build linux

package guestbridge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCopyClipboardRequiresNewSequence(t *testing.T) {
	for _, mode := range []string{"changed", "unchanged", "locked", "changed-again"} {
		t.Run(mode, func(t *testing.T) {
			var sequence atomic.Uint32
			sequence.Store(10)
			var reads, keys atomic.Int32
			manager, _ := newTestManager(t, func(_ context.Context, r Request) Response {
				response := Response{ID: r.ID, Unlocked: mode != "locked", ClipboardSequence: sequence.Load()}
				if r.Method == "copy_clipboard" {
					reads.Add(1)
					response.HasText = true
					response.Text = "new selection"
					if mode == "changed-again" {
						response.ClipboardSequence++
					}
				}
				return response
			})
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			result, err := manager.CopyClipboard(ctx, func(context.Context) error {
				keys.Add(1)
				if mode != "unchanged" {
					sequence.Add(1)
				}
				return nil
			})
			switch mode {
			case "changed":
				if err != nil || result.Text != "new selection" || reads.Load() != 1 {
					t.Fatalf("copy failed: %+v %v", result, err)
				}
			case "locked":
				if !errors.Is(err, ErrLocked) || keys.Load() != 0 || reads.Load() != 0 {
					t.Fatal("locked copy sent input or read text", err)
				}
			case "unchanged":
				if err == nil || reads.Load() != 0 || result.Text != "" {
					t.Fatal("no-op copy exported stale text", err)
				}
			case "changed-again":
				if err == nil || result.Text != "" {
					t.Fatal("concurrent clipboard change exported text")
				}
			}
		})
	}
}

func TestNoOpCopyAndCancelledPollKeepSession(t *testing.T) {
	for _, cancelDuringPoll := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-op", true: "cancel-mid-poll"}[cancelDuringPoll], func(t *testing.T) {
			var polling atomic.Bool
			entered := make(chan struct{}, 1)
			manager, _ := newTestManager(t, func(_ context.Context, r Request) Response {
				if r.Method == "clipboard_state" && polling.Load() {
					select {
					case entered <- struct{}{}:
					default:
					}
					time.Sleep(20 * time.Millisecond)
				}
				return Response{ID: r.ID, Unlocked: true, ClipboardSequence: 7}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelDuringPoll {
				go func() { <-entered; cancel() }()
			}
			_, err := manager.CopyClipboard(ctx, func(context.Context) error { polling.Store(true); return nil })
			if cancelDuringPoll {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrNoClipboardChange) {
				t.Fatal(err)
			}
			if !manager.Status().Connected {
				t.Fatal("no-op/cancel closed USB session")
			}
			if _, err := manager.GetClipboard(context.Background()); err != nil {
				t.Fatal("subsequent request failed", err)
			}
		})
	}
}

func TestOldCompanionKeepsLegacyClipboardWithoutClaimingCopy(t *testing.T) {
	manager, _ := newTestManager(t, func(_ context.Context, r Request) Response { return Response{ID: r.ID, Unlocked: true} }, false)
	if manager.Status().NativeCopy {
		t.Fatal("old companion claimed native shortcuts")
	}
	sent := false
	if _, err := manager.CopyClipboard(context.Background(), func(context.Context) error { sent = true; return nil }); err == nil || sent {
		t.Fatal("unsupported copy sent input")
	}
	if _, err := manager.GetClipboard(context.Background()); err != nil || !manager.Status().Connected {
		t.Fatal("legacy get stopped working", err)
	}
}

func TestInteractiveShortcutDoesNotWaitBehindLongJob(t *testing.T) {
	manager, _ := newTestManager(t, func(_ context.Context, r Request) Response { return Response{ID: r.ID, Unlocked: true} })
	manager.gate <- struct{}{}
	sent := false
	started := time.Now()
	_, err := manager.CopyClipboard(InteractiveContext(context.Background()), func(context.Context) error { sent = true; return nil })
	if !errors.Is(err, ErrInteractiveBusy) || time.Since(started) > time.Second {
		t.Fatal("interactive shortcut waited too long", err)
	}
	<-manager.gate
	if sent {
		t.Fatal("busy shortcut sent delayed input")
	}
	if _, err := manager.GetClipboard(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sent {
		t.Fatal("shortcut ran after gate was released")
	}
}

func TestBrowserReadAndPasteCancellationDrainWithoutReconnecting(t *testing.T) {
	for _, method := range []string{"get_clipboard", "set_clipboard"} {
		t.Run(method, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			var delayed atomic.Bool
			manager, _ := newTestManager(t, func(_ context.Context, r Request) Response {
				if r.Method == method && delayed.CompareAndSwap(false, true) {
					entered <- struct{}{}
					time.Sleep(25 * time.Millisecond)
				}
				return Response{ID: r.ID, Unlocked: true}
			})
			manager.mu.Lock()
			original := manager.current
			manager.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { <-entered; cancel() }()
			var err error
			sent := false
			if method == "get_clipboard" {
				_, err = manager.GetClipboard(ctx)
			} else {
				err = manager.WithClipboard(ctx, "test", func(context.Context) error { sent = true; return nil })
			}
			if !errors.Is(err, context.Canceled) || sent {
				t.Fatal("cancelled operation applied paste input", err)
			}
			manager.mu.Lock()
			same := manager.current == original
			manager.mu.Unlock()
			if !same || !manager.Status().Connected {
				t.Fatal("cancelled browser operation replaced USB session")
			}
			if _, err := manager.GetClipboard(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
