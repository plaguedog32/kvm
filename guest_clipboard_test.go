package kvm

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jetkvm/kvm/internal/guestassets"
	"github.com/jetkvm/kvm/internal/guestbridge"
)

func TestGuestPairingPreparationUnlocksBeforeInstallerStreaming(t *testing.T) {
	if !guestassets.Available() {
		t.Skip("companion not embedded in this test build")
	}
	access, err := guestbridge.NewAccess(filepath.Join(t.TempDir(), "guest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.Rotate(); err != nil {
		t.Fatal(err)
	}
	oldConfig := config
	config = nil
	defer func() { config = oldConfig }()
	service := &deviceGuestService{access: access}
	pairing, code, err := service.preparePairing()
	if err != nil || code != 200 {
		t.Fatal("pairing preparation failed", code, err)
	}
	defer clear(pairing)
	if len(pairing) == 0 {
		t.Fatal("missing installer pairing")
	}
	// Network streaming begins after preparePairing returns. Revocation can
	// acquire the lock immediately, even if no ZIP bytes have been consumed.
	if !service.changeMu.TryLock() {
		t.Fatal("installer download retained pairing lock")
	}
	defer service.changeMu.Unlock()
	if err := access.Disable(); err != nil {
		t.Fatal(err)
	}
	if access.Enabled() {
		t.Fatal("installer stream prevented revocation")
	}
}

func TestGuestClipboardViewerIsolationBoundsAndRevocation(t *testing.T) {
	access, err := guestbridge.NewAccess(filepath.Join(t.TempDir(), "guest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.Rotate(); err != nil {
		t.Fatal(err)
	}
	oldService := getGuestService()
	oldCtx := appCtx
	guestService = &deviceGuestService{access: access, manager: guestbridge.NewManager(access, nil)}
	appCtx = context.Background()
	t.Cleanup(func() { guestService = oldService; appCtx = oldCtx })
	viewer := &Session{done: make(chan struct{})}
	defer close(viewer.done)
	other := &Session{done: make(chan struct{})}
	defer close(other.done)
	start, err := rpcGuestClipboard(viewer, guestClipboardArgs{Action: "start_set"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rpcGuestClipboard(other, guestClipboardArgs{Action: "upload", ID: start.ID, Data: "QQ=="}); err == nil {
		t.Fatal("another viewer accessed transfer")
	}
	for _, args := range []guestClipboardArgs{
		{Action: "upload", ID: start.ID, Offset: 1, Data: "QQ=="},
		{Action: "upload", ID: start.ID, Data: strings.Repeat("A", guestChunkSize+1)},
		{Action: "upload", ID: start.ID, Data: "secret\n"},
	} {
		if _, err = rpcGuestClipboard(viewer, args); err == nil {
			t.Fatal("invalid upload accepted")
		}
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("日本語 🐕"))
	if _, err = rpcGuestClipboard(viewer, guestClipboardArgs{Action: "upload", ID: start.ID, Data: encoded}); err != nil {
		t.Fatal(err)
	}
	if err = access.Disable(); err != nil {
		t.Fatal(err)
	}
	if _, err = rpcGuestClipboard(viewer, guestClipboardArgs{Action: "poll", ID: start.ID}); err == nil {
		t.Fatal("revoked pairing exposed transfer")
	}
	if !rpcHandlers["guestClipboard"].Sensitive {
		t.Fatal("clipboard RPC logging enabled")
	}
}

func TestGuestClipboardExportDigestIsScopedToViewerAndPairing(t *testing.T) {
	pairing, cancel := context.WithCancel(context.Background())
	defer cancel()
	otherPairing, otherCancel := context.WithCancel(context.Background())
	defer otherCancel()
	a := &guestClipboardExport{key: [32]byte{1}}
	b := &guestClipboardExport{key: [32]byte{2}}
	a.remember("selection", pairing)
	if !a.matches("selection", pairing) || a.matches("changed", pairing) || a.matches("selection", otherPairing) || b.matches("selection", pairing) {
		t.Fatal("export digest crossed viewer/pairing or text boundary")
	}
	cancel()
	if a.matches("selection", pairing) {
		t.Fatal("revoked pairing matched export")
	}
}

func TestGuestClipboardSyncChangesOnlyAfterDeliveryOrSet(t *testing.T) {
	pairing, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &guestClipboardExport{key: [32]byte{1}}
	e.remember("old local\r\ntext", pairing)
	e.offer("new remote text", "delivery", pairing)
	if !e.matches("old local\ntext", pairing) || e.matches("new remote text", pairing) {
		t.Fatal("undelivered copy/get changed sync digest")
	}
	if !e.confirm("delivery", pairing) || !e.matches("new remote text", pairing) {
		t.Fatal("confirmed delivery did not synchronize")
	}
	e.offer("unconfirmed second read", "second", pairing)
	e.remember("new local paste", pairing)
	if e.confirm("second", pairing) || !e.matches("new local paste", pairing) {
		t.Fatal("old delivery overrode a later paste")
	}
}

func TestGuestDeliveryConfirmationSurvivesTransferCleanup(t *testing.T) {
	access, err := guestbridge.NewAccess(filepath.Join(t.TempDir(), "guest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.Rotate(); err != nil {
		t.Fatal(err)
	}
	old := getGuestService()
	guestService = &deviceGuestService{access: access, manager: guestbridge.NewManager(access, nil)}
	defer func() { guestService = old }()
	key, pairing := access.Snapshot()
	clear(key)
	e := &guestClipboardExport{key: [32]byte{1}}
	e.offer("remote text", "one-time-delivery", pairing)
	viewer := &Session{guestClipboardExport: e}
	if _, err = rpcGuestClipboard(&Session{}, guestClipboardArgs{Action: "confirm", ID: "one-time-delivery"}); err == nil {
		t.Fatal("another viewer confirmed delivery")
	}
	if _, err = rpcGuestClipboard(viewer, guestClipboardArgs{Action: "confirm", ID: "one-time-delivery"}); err != nil {
		t.Fatal(err)
	}
	if !e.matches("remote text", pairing) {
		t.Fatal("confirmation did not survive job cleanup")
	}
	if _, err = rpcGuestClipboard(viewer, guestClipboardArgs{Action: "confirm", ID: "one-time-delivery"}); err == nil {
		t.Fatal("delivery confirmation replayed")
	}
}

func TestGuestNativeCopyStartsReadOnlyJob(t *testing.T) {
	access, err := guestbridge.NewAccess(filepath.Join(t.TempDir(), "guest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.Rotate(); err != nil {
		t.Fatal(err)
	}
	oldService, oldCtx := getGuestService(), appCtx
	guestService = &deviceGuestService{access: access, manager: guestbridge.NewManager(access, nil)}
	appCtx = context.Background()
	defer func() { guestService = oldService; appCtx = oldCtx }()
	viewer := &Session{done: make(chan struct{})}
	defer close(viewer.done)
	start, err := rpcGuestClipboard(viewer, guestClipboardArgs{Action: "start_copy"})
	if err != nil {
		t.Fatal(err)
	}
	if start.ID == "" || !viewer.guestClipboard.started {
		t.Fatal("copy job did not start")
	}
	if _, err := rpcGuestClipboard(viewer, guestClipboardArgs{Action: "upload", ID: start.ID, Data: "QQ=="}); err == nil {
		t.Fatal("copy accepted upload")
	}
	// Wait for the unavailable simulated device result before restoring globals.
	viewer.guestClipboard.mu.Lock()
	done := viewer.guestClipboard.done
	viewer.guestClipboard.mu.Unlock()
	for !done {
		time.Sleep(time.Millisecond)
		viewer.guestClipboard.mu.Lock()
		done = viewer.guestClipboard.done
		viewer.guestClipboard.mu.Unlock()
	}
	if _, err := rpcGuestClipboard(viewer, guestClipboardArgs{Action: "cancel", ID: start.ID}); err != nil {
		t.Fatal(err)
	}
}
