package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

type recordingBackend struct {
	mu                sync.Mutex
	keys              []report
	mouse             [][3]int
	caps              bool
	mediaCalls        int
	screenshotStarted chan struct{}
}

func (r *recordingBackend) backend() Backend {
	return Backend{
		Status: func(context.Context) (any, error) { return map[string]any{"browser_sessions": 1}, nil },
		Screenshot: func(ctx context.Context) (*Image, error) {
			return &Image{Data: []byte{0xff, 0xd8, 0xff, 0xd9}, Width: 1920, Height: 1080}, nil
		},
		Keyboard: func(mod byte, keys [6]byte) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.keys = append(r.keys, report{mod, keys})
			return nil
		},
		CapsLock: func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.caps },
		Mouse: func(x, y int, b byte) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.mouse = append(r.mouse, [3]int{x, y, int(b)})
			return nil
		},
		Wheel:     func(int8, int8) error { return nil },
		ListMedia: func() (any, error) { return []string{"windows.iso"}, nil },
		MountMedia: func(context.Context, string, string, string) (any, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.mediaCalls++
			return map[string]bool{"mounted": true}, nil
		},
		UnmountMedia: func() error { return nil },
	}
}

func fixture(t *testing.T) (*Access, *recordingBackend, *mcp.ClientSession) {
	return guestFixture(t, nil)
}

func guestFixture(t *testing.T, customize func(*Backend)) (*Access, *recordingBackend, *mcp.ClientSession) {
	t.Helper()
	access, err := NewAccess(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := access.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	recording := &recordingBackend{}
	backend := recording.backend()
	if customize != nil {
		customize(&backend)
	}
	server := httptest.NewServer(NewHandler(access, backend))
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: &http.Client{Transport: bearerTransport{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return access, recording, session
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDiscoveryScreenshotsAndPrompt(t *testing.T) {
	_, _, session := fixture(t)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 16 {
		t.Fatal(len(tools.Tools))
	}
	for _, name := range []string{"guest_status", "get_clipboard", "set_clipboard", "paste_text"} {
		found := false
		for _, tool := range tools.Tools {
			if tool.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing guest tool %s", name)
		}
	}
	res := call(t, session, "screenshot", nil)
	if res.IsError {
		t.Fatal(res)
	}
	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/jpeg" || len(img.Data) != 4 {
		t.Fatal(res)
	}
	status := call(t, session, "get_status", nil)
	if status.IsError || !strings.Contains(status.Content[0].(*mcp.TextContent).Text, `"browser_sessions":1`) {
		t.Fatal(status)
	}
	prompt, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "windows_setup"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.Messages[0].Content.(*mcp.TextContent).Text, "partition policy") {
		t.Fatal(prompt)
	}
}

func TestInputReportsAndValidation(t *testing.T) {
	_, r, s := fixture(t)
	if call(t, s, "press_key", map[string]any{"key": "Delete", "modifiers": []string{"Control", "Alt"}}).IsError {
		t.Fatal("chord failed")
	}
	r.mu.Lock()
	if r.keys[0].modifier != 5 || r.keys[0].keys[0] != 76 || r.keys[1] != (report{}) {
		t.Fatal(r.keys)
	}
	r.mu.Unlock()
	if call(t, s, "type_text", map[string]any{"text": "Az!\n", "delay_ms": 5}).IsError {
		t.Fatal("typing failed")
	}
	if call(t, s, "mouse", map[string]any{"x": 1, "y": 0.5, "button": "right"}).IsError {
		t.Fatal("click failed")
	}
	r.mu.Lock()
	if r.mouse[0] != [3]int{32767, 16384, 2} || r.mouse[1][2] != 0 {
		t.Fatal(r.mouse)
	}
	count := len(r.keys)
	r.mu.Unlock()
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"type_text", map[string]any{"text": "abc\u00e9"}},
		{"mouse", map[string]any{"x": 2, "y": 0}},
		{"press_key", map[string]any{"key": "toString"}},
		{"mount_media", map[string]any{"url": "https://example.test/a.iso", "filename": "x"}},
		{"mount_media", map[string]any{"url": "file:///etc/passwd"}},
		{"powershell", map[string]any{"script": strings.Repeat("x", 3000)}},
		{"type_text", map[string]any{"text": strings.Repeat("a", 1000), "delay_ms": 100}},
	} {
		if !call(t, s, tc.name, tc.args).IsError {
			t.Fatal("accepted", tc)
		}
	}
	r.mu.Lock()
	if len(r.keys) != count || r.mediaCalls != 0 {
		t.Fatal("invalid request caused side effects")
	}
	r.caps = true
	r.mu.Unlock()
	if !call(t, s, "type_text", map[string]any{"text": "abc"}).IsError {
		t.Fatal("Caps Lock was ignored")
	}
}

func TestStorageFailureRevokesBeforePersistenceAndCorruptFileRecovers(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(map[bool]string{false: "disable", true: "rotate"}[rotate], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "access.json")
			a, err := NewAccess(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Rotate(); err != nil {
				t.Fatal(err)
			}
			generation := a.ctx
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "block"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if rotate {
				_, err = a.Rotate()
			} else {
				err = a.Disable()
			}
			if err == nil {
				t.Fatal("expected storage error")
			}
			if a.Enabled() || generation.Err() == nil {
				t.Fatal("storage failure left live access active")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "access.json")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := NewAccess(path)
	if err == nil || a == nil || a.Enabled() {
		t.Fatal("corrupt state did not fail closed")
	}
	if _, err := a.Rotate(); err != nil {
		t.Fatal("could not recover corrupt state", err)
	}
}

func TestTwoListenersShareTokenRevocationAndInputGate(t *testing.T) {
	a, err := NewAccess(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	r := &recordingBackend{}
	handler := NewHandler(a, r.backend())
	first := httptest.NewServer(handler)
	defer first.Close()
	second := httptest.NewTLSServer(handler)
	defer second.Close()
	clients := []*mcp.ClientSession{}
	for i, endpoint := range []string{first.URL, second.URL} {
		httpClient := &http.Client{Transport: bearerTransport{token: token}}
		if i == 1 {
			transport := second.Client().Transport
			httpClient.Transport = tokenTransport{token: token, inner: transport}
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "shared-test", Version: "1"}, nil)
		session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		clients = append(clients, session)
	}
	var group sync.WaitGroup
	for i, s := range clients {
		group.Add(1)
		go func(i int, s *mcp.ClientSession) {
			defer group.Done()
			_, _ = s.CallTool(context.Background(), &mcp.CallToolParams{Name: "type_text", Arguments: map[string]any{"text": strings.Repeat(string(rune('a'+i)), 5), "delay_ms": 10}})
		}(i, s)
	}
	group.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	var pressed []byte
	for _, report := range r.keys {
		if report.keys[0] != 0 {
			pressed = append(pressed, report.keys[0])
		}
	}
	if len(pressed) != 10 {
		t.Fatal(pressed)
	}
	for i := 1; i < 5; i++ {
		if pressed[i] != pressed[0] || pressed[i+5] != pressed[5] {
			t.Fatal("cross-listener input interleaved", pressed)
		}
	}
	if err := a.Disable(); err != nil {
		t.Fatal(err)
	}
	for _, s := range clients {
		if _, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_status"}); err == nil {
			t.Fatal("revoked token accepted")
		}
	}
}

type tokenTransport struct {
	token string
	inner http.RoundTripper
}

func (b tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.inner.RoundTrip(r)
}

func TestCancelAndRevocationStopTypingAndReleaseKeys(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel tool", true: "token revocation"}[revoke], func(t *testing.T) {
			a, r, s := fixture(t)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = s.CallTool(context.Background(), &mcp.CallToolParams{Name: "type_text", Arguments: map[string]any{"text": strings.Repeat("a", 100), "delay_ms": 50}})
			}()
			deadline := time.Now().Add(2 * time.Second)
			for {
				r.mu.Lock()
				started := len(r.keys) > 0
				r.mu.Unlock()
				if started {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("input never started")
				}
				time.Sleep(time.Millisecond)
			}
			if revoke {
				if err := a.Disable(); err != nil {
					t.Fatal(err)
				}
			} else {
				if call(t, s, "cancel_input", nil).IsError {
					t.Fatal("cancel failed")
				}
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("typing was not canceled")
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if len(r.keys) > 4 || r.keys[len(r.keys)-1] != (report{}) {
				t.Fatal("keys not released", r.keys)
			}
		})
	}
}

func TestSerializationAndKeyReleaseOnFailure(t *testing.T) {
	_, r, _ := fixture(t)
	c := &controller{backend: r.backend(), gate: make(chan struct{}, 1)}
	var order []int
	var mu sync.Mutex
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.input(context.Background(), func(context.Context) (*mcp.CallToolResult, error) {
			close(entered)
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			return nil, nil
		})
	}()
	<-entered
	_, _ = c.input(context.Background(), func(context.Context) (*mcp.CallToolResult, error) {
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
		return nil, nil
	})
	<-done
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatal(order)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.backend.Keyboard = func(mod byte, keys [6]byte) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.keys = append(r.keys, report{mod, keys})
		if keys[0] != 0 {
			cancel()
		}
		return nil
	}
	err := c.typeText(ctx, "abc", 5)
	if err != context.Canceled {
		t.Fatal(err)
	}
	if len(r.keys) != 2 || r.keys[1] != (report{}) {
		t.Fatal(r.keys)
	}
}

func TestBearerAuthenticationOriginAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	a, err := NewAccess(path)
	if err != nil {
		t.Fatal(err)
	}
	backend := (&recordingBackend{}).backend()
	handler := NewHandler(a, backend)
	request := func(token, origin string) int {
		r := httptest.NewRequest("POST", "http://device/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if request("", "") != 401 {
		t.Fatal("disabled endpoint accessible")
	}
	token, err := a.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://evil.test", "null", "http://device@evil.test", "https://device", "http://device/path"} {
		if request(token, origin) != 403 {
			t.Fatal("accepted Origin", origin)
		}
	}
	if request("wrong", "") != 401 {
		t.Fatal("wrong token accepted")
	}
	if request(token, "") == 401 || request(token, "http://device") == 403 {
		t.Fatal("valid bearer rejected")
	}
	reloaded, err := NewAccess(path)
	if err != nil || !reloaded.Enabled() {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), token) {
		t.Fatal("plaintext credential stored")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("MCP token file must be owner-only", err)
	}
	if _, err := a.Rotate(); err != nil {
		t.Fatal(err)
	}
	if request(token, "") != 401 {
		t.Fatal("old token accepted after rotation")
	}
	if err := a.Disable(); err != nil {
		t.Fatal(err)
	}
	if request(token, "") != 401 {
		t.Fatal("disabled token accepted")
	}
}
