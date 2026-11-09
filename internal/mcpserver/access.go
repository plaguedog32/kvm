package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Access owns a device-local bearer credential, separate from browser sessions.
// Only its SHA-256 hash is persisted. Rotating/disabling cancels in-flight calls.
type Access struct {
	mu     sync.Mutex
	path   string
	hash   string
	ctx    context.Context
	cancel context.CancelFunc
}

func NewAccess(path string) (*Access, error) {
	a := &Access{path: path}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	var saved struct {
		Hash string `json:"token_hash"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return a, err
	}
	if saved.Hash != "" {
		decoded, err := hex.DecodeString(saved.Hash)
		if err != nil || len(decoded) != sha256.Size {
			return a, fmt.Errorf("invalid MCP token hash")
		}
	}
	a.hash = saved.Hash
	return a, nil
}

func (a *Access) Enabled() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.hash != "" }

func (a *Access) save(hash string) error {
	if err := os.MkdirAll(filepath.Dir(a.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Hash string `json:"token_hash"`
	}{hash})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(a.path), ".mcp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), a.path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(a.path))
}

func syncDirectory(path string) error {
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

func (a *Access) revokeLocked() {
	a.cancel()
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.hash = ""
}

func (a *Access) removeCredentialLocked() error {
	if err := os.Remove(a.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(filepath.Dir(a.path))
}

func (a *Access) Rotate() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Fail closed even if entropy or storage fails during rotation. Persist
	// removal before writing the replacement so power loss cannot revive it.
	a.revokeLocked()
	if err := a.removeCredentialLocked(); err != nil {
		return "", err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	token := hex.EncodeToString(random)
	hash := sha256.Sum256([]byte(token))
	if err := a.save(hex.EncodeToString(hash[:])); err != nil {
		return "", err
	}
	a.hash = hex.EncodeToString(hash[:])
	return token, nil
}

func (a *Access) Disable() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revokeLocked()
	return a.removeCredentialLocked()
}

// SameOrigin rejects cross-site browser requests. Native MCP clients omit Origin.
func SameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
}

func (a *Access) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !SameOrigin(r) {
			http.Error(w, "cross-origin MCP request denied", http.StatusForbidden)
			return
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		credential := ""
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			credential = parts[1]
		}
		hash := sha256.Sum256([]byte(credential))
		a.mu.Lock()
		valid := a.hash != "" && len(credential) == 64 && subtle.ConstantTimeCompare([]byte(hex.EncodeToString(hash[:])), []byte(a.hash)) == 1
		generation := a.ctx
		a.mu.Unlock()
		if !valid {
			w.Header().Set("WWW-Authenticate", `Bearer realm="JetKVM MCP"`)
			http.Error(w, "MCP bearer token required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(generation, cancel)
		defer stop()
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
