//go:build linux

package guestbridge

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPairingRotationPersistenceAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "guest.json")
	a, err := NewAccess(path)
	if err != nil || a.Enabled() {
		t.Fatalf("default enabled: %v", err)
	}
	first, err := a.Rotate()
	if err != nil || len(first) != 32 || !a.Enabled() {
		t.Fatalf("enable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("secret file mode: %o", info.Mode().Perm())
	}
	loaded, err := NewAccess(path)
	if err != nil {
		t.Fatal(err)
	}
	key, ctx := a.Snapshot()
	if !bytes.Equal(key, first) {
		t.Fatal("snapshot mismatch")
	}
	reloaded, _ := loaded.Snapshot()
	if !bytes.Equal(reloaded, first) {
		t.Fatal("persisted key mismatch")
	}
	first[0] ^= 1
	if bytes.Equal(first, key) {
		t.Fatal("returned secret aliases stored secret")
	}
	second, err := a.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("rotation left old session alive")
	}
	if bytes.Equal(second, key) {
		t.Fatal("rotation reused key")
	}
	_, ctx = a.Snapshot()
	if err := a.Disable(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil || a.Enabled() {
		t.Fatal("disable left access active")
	}
	loaded, err = NewAccess(path)
	if err != nil || loaded.Enabled() {
		t.Fatalf("disabled key survived restart: %v", err)
	}
}

func TestCorruptPairingCanBeRecovered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guest.json")
	if err := os.WriteFile(path, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := NewAccess(path)
	if err == nil || a == nil || a.Enabled() {
		t.Fatal("corrupt configuration did not fail closed")
	}
	if _, err := a.Rotate(); err != nil {
		t.Fatal("cannot recover pairing", err)
	}
	if !a.Enabled() {
		t.Fatal("recovery did not enable pairing")
	}
}

func TestFailedPairingPersistenceRevokesMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guest.json")
	a, err := NewAccess(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Rotate(); err != nil {
		t.Fatal(err)
	}
	_, ctx := a.Snapshot()
	// A nonempty directory cannot be replaced or removed as a credential file.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "obstruction"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Rotate(); err == nil {
		t.Fatal("rotation unexpectedly succeeded")
	}
	if a.Enabled() || ctx.Err() == nil {
		t.Fatal("failed rotation kept old access active")
	}
	if err := a.Disable(); err == nil {
		t.Fatal("disable unexpectedly persisted")
	}
	if a.Enabled() {
		t.Fatal("failed disable enabled access")
	}
}
