package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGuestClipboardBulkUnicodeAndPaste(t *testing.T) {
	var clipboard string
	_, recording, session := guestFixture(t, func(backend *Backend) {
		backend.GuestStatus = func(context.Context) (any, error) { return map[string]any{"connected": true, "unlocked": true}, nil }
		backend.WithClipboard = func(ctx context.Context, text string, paste func(context.Context) error) error {
			clipboard = text
			if paste != nil {
				return paste(ctx)
			}
			return nil
		}
		backend.GetClipboard = func(context.Context) (*Clipboard, error) {
			return &Clipboard{Text: clipboard, HasText: true, User: "DOMAIN\\user"}, nil
		}
	})
	text := strings.Repeat("日本語 🐕\r\n", 8000)
	if call(t, session, "set_clipboard", map[string]any{"text": text}).IsError {
		t.Fatal("bulk clipboard write failed")
	}
	if clipboard != text {
		t.Fatal("bulk Unicode changed")
	}
	if len(recording.keys) != 0 {
		t.Fatal("setting clipboard unexpectedly typed")
	}
	if call(t, session, "get_clipboard", nil).IsError {
		t.Fatal("clipboard read failed")
	}
	if call(t, session, "paste_text", map[string]any{"text": "こんにちは"}).IsError {
		t.Fatal("paste failed")
	}
	recording.mu.Lock()
	defer recording.mu.Unlock()
	if len(recording.keys) != 2 || recording.keys[0].modifier != 1 || recording.keys[0].keys[0] != 0x19 || recording.keys[1] != (report{}) {
		t.Fatal("paste was not Ctrl+V followed by release", recording.keys)
	}
}

func TestGuestFailureNeverFallsBackToTyping(t *testing.T) {
	_, recording, session := guestFixture(t, func(backend *Backend) {
		backend.WithClipboard = func(context.Context, string, func(context.Context) error) error {
			return errors.New("guest disconnected")
		}
	})
	if !call(t, session, "paste_text", map[string]any{"text": "do not type this"}).IsError {
		t.Fatal("disconnected paste succeeded")
	}
	if !call(t, session, "powershell", map[string]any{"script": "Write-Output 'test'", "use_clipboard": true}).IsError {
		t.Fatal("disconnected script submission succeeded")
	}
	recording.mu.Lock()
	defer recording.mu.Unlock()
	if len(recording.keys) != 0 {
		t.Fatal("guest failure fell back to keyboard input")
	}
}

func TestPowerShellClipboardNeverPressesEnter(t *testing.T) {
	var clipboard string
	_, recording, session := guestFixture(t, func(backend *Backend) {
		backend.WithClipboard = func(ctx context.Context, text string, paste func(context.Context) error) error {
			clipboard = text
			return paste(ctx)
		}
	})
	if call(t, session, "powershell", map[string]any{"script": "Write-Output '日本語'", "use_clipboard": true}).IsError {
		t.Fatal("clipboard script failed")
	}
	if !strings.HasPrefix(clipboard, "powershell.exe ") || !strings.Contains(clipboard, "-EncodedCommand ") {
		t.Fatal("script was not encoded safely")
	}
	recording.mu.Lock()
	defer recording.mu.Unlock()
	if len(recording.keys) != 2 || recording.keys[0].modifier != 1 || recording.keys[0].keys[0] != 0x19 || recording.keys[1] != (report{}) {
		t.Fatal("incorrect submission input", recording.keys)
	}
}
