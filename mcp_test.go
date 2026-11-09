//go:build linux && arm

package kvm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jetkvm/kvm/internal/mcpserver"
	"github.com/jetkvm/kvm/internal/native"
	"github.com/pion/webrtc/v4"
)

type snapshotNative struct {
	native.EmptyNativeInterface
	calls          []string
	fail           bool
	onSnapshot     func()
	sleepSupported bool
}

func (n *snapshotNative) VideoStart() error { n.calls = append(n.calls, "start"); return nil }
func (n *snapshotNative) VideoStop() error  { n.calls = append(n.calls, "stop"); return nil }
func (n *snapshotNative) VideoSnapshot(context.Context) (*native.Snapshot, error) {
	n.calls = append(n.calls, "snapshot")
	if n.onSnapshot != nil {
		n.onSnapshot()
	}
	if n.fail {
		return nil, fmt.Errorf("no HDMI signal")
	}
	return &native.Snapshot{JPEG: []byte("jpeg"), Width: 1920, Height: 1080}, nil
}

func (n *snapshotNative) VideoSleepModeSupported() bool { return n.sleepSupported }
func (n *snapshotNative) VideoSetCodecType(codec int) error {
	n.calls = append(n.calls, fmt.Sprintf("codec:%d", codec))
	return nil
}

func TestMCPSnapshotPreservesViewerAndCleansUpStandaloneCapture(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		sessions               int
		fail, paused, canceled bool
		want                   []string
	}{
		{"browser viewing", 1, false, false, false, []string{"snapshot"}},
		{"standalone", 0, false, false, false, []string{"start", "snapshot", "stop"}},
		{"no signal", 0, true, false, false, []string{"start", "snapshot", "stop"}},
		{"browser paused", 1, false, true, false, nil},
		{"canceled", 0, false, false, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalNative, originalConfig, originalSession := nativeInstance, config, currentSession
			activeSessionsMutex.Lock()
			originalCount := actionSessions
			actionSessions = tc.sessions
			activeSessionsMutex.Unlock()
			hostDisplayAdvertiseLock.Lock()
			originalAdvertised := hostDisplayAdvertised
			hostDisplayAdvertised = true
			hostDisplayAdvertiseLock.Unlock()
			originalPause := videoPausedBy
			recorder := &snapshotNative{fail: tc.fail}
			nativeInstance = recorder
			config = &Config{}
			viewer := &Session{codecMimeType: webrtc.MimeTypeH265}
			if tc.sessions > 0 {
				currentSession = viewer
			} else {
				currentSession = nil
			}
			if tc.paused {
				videoPausedBy = viewer
			} else {
				videoPausedBy = nil
			}
			t.Cleanup(func() {
				nativeInstance, config, currentSession = originalNative, originalConfig, originalSession
				videoPausedBy = originalPause
				activeSessionsMutex.Lock()
				actionSessions = originalCount
				activeSessionsMutex.Unlock()
				hostDisplayAdvertiseLock.Lock()
				hostDisplayAdvertised = originalAdvertised
				hostDisplayAdvertiseLock.Unlock()
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			frame, err := captureMCPScreenshot(ctx)
			if (err != nil) != (tc.fail || tc.paused || tc.canceled) {
				t.Fatal(frame, err)
			}
			if !reflect.DeepEqual(recorder.calls, tc.want) {
				t.Fatalf("native calls=%v want %v", recorder.calls, tc.want)
			}
			if tc.sessions > 0 && currentSession != viewer {
				t.Fatal("MCP replaced the browser session")
			}
			if mcpCaptureActive.Load() {
				t.Fatal("capture activity leaked")
			}
			if tc.name == "standalone" {
				recorder.calls = nil
				recorder.onSnapshot = func() { incrActiveSessions(); currentSession = viewer }
				if _, err := captureMCPScreenshot(context.Background()); err != nil {
					t.Fatal(err)
				}
				startNativeVideoForSession(viewer)
				if !reflect.DeepEqual(recorder.calls, []string{"start", "snapshot", "stop", "codec:1", "start"}) {
					t.Fatalf("viewer codec handoff: %v", recorder.calls)
				}
			}
		})
	}
}

func TestMCPRoutesShareRevocationAcrossRouters(t *testing.T) {
	access, err := mcpserver.NewAccess(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := &deviceMCPService{access: access, handler: access.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))}
	routers := []*gin.Engine{gin.New(), gin.New()}
	for _, r := range routers {
		registerMCPRoutes(r, r.Group("/"), service)
	}
	request := func(index int, method, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		routers[index].ServeHTTP(w, r)
		return w
	}
	w := request(0, "POST", "/mcp/access", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if request(1, "POST", "/mcp", issued.Token).Code != 200 {
		t.Fatal("token not shared")
	}
	if request(0, "DELETE", "/mcp/access", "").Code != 200 {
		t.Fatal("disable failed")
	}
	if request(1, "POST", "/mcp", issued.Token).Code != 401 {
		t.Fatal("revoked token accepted by second router")
	}
}

func TestMCPResetContinuesWhenPersistenceFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	access, err := mcpserver.NewAccess(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.Rotate(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "block"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	revokeMCPForReset(access, path)
	if access.Enabled() {
		t.Fatal("failed persistence left token active")
	}
	// Reaching here proves reset cleanup is best-effort, not an error return.
}

func TestMCPSnapshotIdlePolicies(t *testing.T) {
	originalNative, originalConfig := nativeInstance, config
	originalPause := videoPausedBy
	videoPausedBy = nil
	hostDisplayAdvertiseLock.Lock()
	originalAdvertised := hostDisplayAdvertised
	hostDisplayAdvertiseLock.Unlock()
	activeSessionsMutex.Lock()
	originalCount := actionSessions
	actionSessions = 0
	activeSessionsMutex.Unlock()
	n := &snapshotNative{sleepSupported: true}
	nativeInstance = n
	config = &Config{HideDisplayWhenIdle: true}
	t.Cleanup(func() {
		stopVideoSleepModeTicker()
		nativeInstance, config = originalNative, originalConfig
		videoPausedBy = originalPause
		activeSessionsMutex.Lock()
		actionSessions = originalCount
		activeSessionsMutex.Unlock()
		hostDisplayAdvertiseLock.Lock()
		hostDisplayAdvertised = originalAdvertised
		hostDisplayAdvertiseLock.Unlock()
	})
	if _, err := captureMCPScreenshot(context.Background()); err == nil {
		t.Fatal("hidden display must give a clear error")
	}
	if len(n.calls) != 0 {
		t.Fatal("hidden display was toggled", n.calls)
	}
	config = &Config{VideoSleepAfterSec: 3600}
	if _, err := captureMCPScreenshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	videoSleepTickerMu.Lock()
	scheduled := videoSleepModeCancel != nil
	videoSleepTickerMu.Unlock()
	if !scheduled {
		t.Fatal("standalone capture did not restart idle sleep timer")
	}
}
