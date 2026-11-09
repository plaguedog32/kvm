package kvm

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/jetkvm/kvm/internal/mcpserver"
	"github.com/jetkvm/kvm/internal/native"
)

var mcpCaptureActive atomic.Bool
var mcpVideoState atomic.Pointer[native.VideoState]

const mcpAccessPath = "/userdata/jetkvm/mcp-access.json"

type deviceMCPService struct {
	access  *mcpserver.Access
	handler http.Handler
}

var mcpServiceOnce sync.Once
var mcpService *deviceMCPService

func getMCPService() *deviceMCPService {
	mcpServiceOnce.Do(func() { mcpService = buildMCPService() })
	return mcpService
}

func captureMCPScreenshot(ctx context.Context) (*mcpserver.Image, error) {
	// Share capture hardware, never the browser's Session or codec negotiation.
	// The pause lock also serializes viewer start/stop against one-shot capture.
	videoPauseMu.Lock()
	defer videoPauseMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if videoPausedBy != nil {
		return nil, fmt.Errorf("video is paused by the browser; resume it before capturing")
	}
	standalone := getActiveSessions() == 0
	if standalone && config.HideDisplayWhenIdle {
		return nil, fmt.Errorf("display is hidden while idle; connect a browser viewer or disable hide display when idle before using MCP screenshots")
	}
	mcpCaptureActive.Store(true)
	defer mcpCaptureActive.Store(false)
	if err := setHostDisplayAdvertised(true, "mcp_snapshot", false); err != nil {
		return nil, err
	}
	defer func() {
		mcpCaptureActive.Store(false)
		if standalone {
			// Any viewer arriving during capture waits on videoPauseMu. Stop
			// our encoder so that viewer can start its negotiated codec.
			_ = nativeInstance.VideoStop()
			if getActiveSessions() == 0 {
				_ = applyHostDisplayAdvertisement("mcp_snapshot_finished")
				startVideoSleepModeTicker()
			}
		}
	}()
	if standalone {
		if err := nativeInstance.VideoStart(); err != nil {
			return nil, err
		}
	}
	frame, err := nativeInstance.VideoSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &mcpserver.Image{Data: frame.JPEG, Width: frame.Width, Height: frame.Height}, nil
}

func mcpHIDReady() error {
	if gadget == nil || !usbReadyForHidReports() {
		return fmt.Errorf("target USB HID is unavailable; inspect USB connection and retry after recovery")
	}
	return nil
}

func buildMCPService() *deviceMCPService {
	access, err := mcpserver.NewAccess(mcpAccessPath)
	if err != nil {
		logger.Error().Err(err).Msg("MCP access configuration could not be loaded; MCP disabled")
	}
	backend := mcpserver.Backend{
		Status: func(ctx context.Context) (any, error) {
			media, err := rpcGetVirtualMediaState()
			if err != nil {
				return nil, err
			}
			video := mcpVideoState.Load()
			if video == nil {
				video = &native.VideoState{}
			}
			return map[string]any{"video": video, "usb": rpcGetUSBState(), "media": media, "keyboard": rpcGetKeyboardLedState(), "browser_sessions": getActiveSessions(), "network_policy": map[string]any{"application": "private-lan-only", "system_firewall_applied": localOnlySystemFirewall.Load()}}, nil
		},
		GuestStatus: func(ctx context.Context) (any, error) { return getGuestService().manager.CheckStatus(ctx), nil },
		GetClipboard: func(ctx context.Context) (*mcpserver.Clipboard, error) {
			result, err := getGuestService().manager.GetClipboard(ctx)
			if err != nil {
				return nil, err
			}
			return &mcpserver.Clipboard{Text: result.Text, HasText: result.HasText, User: result.User}, nil
		},
		WithClipboard: func(ctx context.Context, text string, paste func(context.Context) error) error {
			if paste != nil {
				if err := mcpHIDReady(); err != nil {
					return err
				}
			}
			return getGuestService().manager.WithClipboard(ctx, text, paste)
		},
		Screenshot: captureMCPScreenshot,
		CapsLock:   func() bool { return gadget.GetKeyboardState().CapsLock },
		Keyboard: func(modifier byte, keys [6]byte) error {
			if err := mcpHIDReady(); err != nil {
				return err
			}
			return gadget.KeyboardReport(modifier, keys[:])
		},
		Mouse: func(x, y int, buttons byte) error {
			if err := mcpHIDReady(); err != nil {
				return err
			}
			if !gadget.HasAbsoluteMouse() {
				return fmt.Errorf("absolute mouse is disabled")
			}
			return gadget.AbsMouseReport(x, y, buttons)
		},
		Wheel: func(y, x int8) error {
			if err := mcpHIDReady(); err != nil {
				return err
			}
			if gadget.HasAbsoluteMouse() {
				return gadget.AbsMouseWheelReport(y, x)
			}
			return gadget.RelMouseWheelReport(y, x)
		},
		ListMedia: func() (any, error) { return rpcListStorageFiles() },
		MountMedia: func(ctx context.Context, url, filename, mode string) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var err error
			if url != "" {
				err = rpcMountWithHTTP(url, VirtualMediaMode(mode))
			} else {
				err = rpcMountWithStorage(filename, VirtualMediaMode(mode))
			}
			if err != nil {
				return nil, err
			}
			return rpcGetVirtualMediaState()
		},
		UnmountMedia: rpcUnmountImage,
	}
	return &deviceMCPService{access: access, handler: mcpserver.NewHandler(access, backend)}
}

func setupMCPRoutes(r *gin.Engine, protected *gin.RouterGroup) {
	registerMCPRoutes(r, protected, getMCPService())
}

func registerMCPRoutes(r *gin.Engine, protected *gin.RouterGroup, service *deviceMCPService) {
	access := service.access
	r.Any("/mcp", gin.WrapH(service.handler))
	admin := protected.Group("/mcp/access")
	admin.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if !mcpserver.SameOrigin(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	admin.GET("", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"enabled": access.Enabled()}) })
	admin.POST("", func(c *gin.Context) {
		token, err := access.Rotate()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"enabled": access.Enabled(), "error": "MCP disabled for this boot; could not save access settings"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"enabled": true, "token": token})
	})
	admin.DELETE("", func(c *gin.Context) {
		if err := access.Disable(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"enabled": access.Enabled(), "error": "MCP disabled for this boot; could not persist revocation"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"enabled": false})
	})
}
