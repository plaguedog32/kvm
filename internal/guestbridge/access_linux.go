//go:build linux

package guestbridge

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Access stores the pairing secret only on the KVM. Status responses must use
// Enabled, never serialize a Snapshot or the on-disk configuration. The Windows
// service protects its copy with machine DPAPI and SYSTEM/Administrator ACLs.
type Access struct {
	mu     sync.Mutex
	path   string
	key    []byte
	ctx    context.Context
	cancel context.CancelFunc
}

type savedAccess struct {
	Version int    `json:"version"`
	Key     string `json:"pairing_key"`
}

func NewAccess(path string) (*Access, error) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Access{path: path, ctx: ctx, cancel: cancel}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	var saved savedAccess
	if err := json.Unmarshal(data, &saved); err != nil {
		return a, fmt.Errorf("invalid guest pairing configuration: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(saved.Key)
	if err != nil || saved.Version != 1 || len(key) != 32 {
		return a, fmt.Errorf("invalid guest pairing configuration")
	}
	a.key = key
	return a, nil
}

func (a *Access) Enabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.key) == 32
}

// Snapshot returns a private copy and a context canceled when pairing changes.
// The context belongs to Access; callers must not cancel it themselves.
func (a *Access) Snapshot() ([]byte, context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]byte(nil), a.key...), a.ctx
}

func (a *Access) revokeLocked() {
	a.cancel()
	clear(a.key)
	a.key = nil
	a.ctx, a.cancel = context.WithCancel(context.Background())
}

func syncAccessDirectory(path string) error {
	dir, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (a *Access) removeLocked() error {
	if err := os.Remove(a.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncAccessDirectory(filepath.Dir(a.path))
}

func (a *Access) Disable() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revokeLocked()
	return a.removeLocked()
}

// Rotate returns a new secret once for provisioning the Windows service. It
// revokes existing sessions first and stays disabled on any persistence error.
func (a *Access) Rotate() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revokeLocked()
	if err := a.removeLocked(); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0700); err != nil {
		return nil, err
	}
	data, err := json.Marshal(savedAccess{Version: 1, Key: base64.StdEncoding.EncodeToString(key)})
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(a.path), ".guest-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(f.Name(), a.path); err != nil {
		return nil, err
	}
	if err = syncAccessDirectory(filepath.Dir(a.path)); err != nil {
		// A failed enable is never active in this process. Remove the staged
		// credential as well when the filesystem still permits it.
		_ = a.removeLocked()
		return nil, err
	}
	a.key = key
	return append([]byte(nil), key...), nil
}
