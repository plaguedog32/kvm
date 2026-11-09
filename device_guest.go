package kvm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/jetkvm/kvm/internal/guestassets"
	"github.com/jetkvm/kvm/internal/guestbridge"
	"github.com/jetkvm/kvm/internal/mcpserver"
)

const guestAccessPath = "/userdata/jetkvm/guest-access.json"

type deviceGuestService struct {
	access   *guestbridge.Access
	manager  *guestbridge.Manager
	changeMu sync.Mutex
}

var guestServiceOnce sync.Once
var guestService *deviceGuestService

func getGuestService() *deviceGuestService {
	guestServiceOnce.Do(func() {
		access, err := guestbridge.NewAccess(guestAccessPath)
		if err != nil {
			logger.Warn().Err(err).Msg("Guest pairing unavailable; integration disabled")
		}
		service := &deviceGuestService{access: access}
		asleep := func() bool { return gadget != nil && gadget.GetUsbState() == "suspended" }
		service.manager = guestbridge.NewManagerWithSleepCheck(access, func(ctx context.Context) (guestbridge.Reports, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if gadget == nil {
				return nil, guestbridge.ErrUnavailable
			}
			connection, err := gadget.OpenGuestHIDConnection(func(path string) (io.Closer, error) {
				raw, err := guestbridge.OpenHIDReports(path)
				if err != nil {
					return nil, err
				}
				return guestbridge.AcknowledgeOutputReports(guestbridge.WhileAwakeReports(raw, asleep)), nil
			})
			if err != nil {
				return nil, err
			}
			return connection.(guestbridge.Reports), nil
		}, asleep)
		guestService = service
	})
	return guestService
}
func configureGuestUSB() error {
	if gadget == nil {
		return fmt.Errorf("USB gadget is unavailable")
	}
	gadget.SetGadgetDevices(effectiveUsbDevices())
	return updateUsbRelatedConfig()
}
func guestPairingIdentity() (guestbridge.PairingConfig, error) {
	p := guestbridge.PairingConfig{Version: 1, VendorID: 0x1d6b, ProductID: 0x0104}
	if config != nil && config.UsbConfig != nil {
		parse := func(value string, fallback uint16) (uint16, error) {
			if value == "" {
				return fallback, nil
			}
			n, e := strconv.ParseUint(value, 0, 16)
			if e != nil || n == 0 {
				return 0, fmt.Errorf("invalid USB identity")
			}
			return uint16(n), nil
		}
		var err error
		p.VendorID, err = parse(config.UsbConfig.VendorId, p.VendorID)
		if err != nil {
			return p, err
		}
		p.ProductID, err = parse(config.UsbConfig.ProductId, p.ProductID)
		if err != nil {
			return p, err
		}
		p.Serial = config.UsbConfig.SerialNumber
	}
	p.PairingKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	key, err := p.Key()
	clear(key)
	p.PairingKey = ""
	return p, err
}
func setupGuestRoutes(protected *gin.RouterGroup) {
	service := getGuestService()
	routes := protected.Group("/guest/access")
	routes.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		if !mcpserver.SameOrigin(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	routes.GET("", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": service.manager.Status(), "installer_available": guestassets.Available()})
	})
	routes.POST("", func(c *gin.Context) {
		pairing, code, err := service.preparePairing()
		if err != nil {
			c.JSON(code, gin.H{"error": err.Error()})
			return
		}
		defer clear(pairing)
		c.Header("Content-Disposition", `attachment; filename="jetkvm-guest-private.zip"`)
		c.Header("Content-Type", "application/zip")
		if err = guestassets.Bundle(c.Writer, pairing); err != nil {
			logger.Warn().Msg("Guest installer download did not complete")
		}
	})
	routes.DELETE("", func(c *gin.Context) {
		service.changeMu.Lock()
		persistErr := service.access.Disable()
		usbErr := configureGuestUSB()
		service.changeMu.Unlock()
		if persistErr != nil || usbErr != nil {
			c.JSON(500, gin.H{"enabled": false, "error": "Guest disabled for this boot; persistence or USB update failed"})
			return
		}
		c.JSON(200, gin.H{"enabled": false})
	})
}

// Only pairing generation and USB configuration are serialized. A stalled
// installer download must never keep the user from revoking access.
func (service *deviceGuestService) preparePairing() ([]byte, int, error) {
	if !guestassets.Available() {
		return nil, 503, errors.New("Windows companion is not included in this build")
	}
	service.changeMu.Lock()
	defer service.changeMu.Unlock()
	identity, err := guestPairingIdentity()
	if err != nil {
		return nil, 400, errors.New("invalid USB identity")
	}
	wasEnabled := service.access.Enabled()
	key, err := service.access.Rotate()
	if err != nil {
		return nil, 500, errors.New("guest pairing disabled; could not persist pairing")
	}
	defer clear(key)
	if !wasEnabled {
		if err = configureGuestUSB(); err != nil {
			_ = service.access.Disable()
			_ = configureGuestUSB()
			return nil, 500, errors.New("guest pairing disabled; USB reconfiguration failed")
		}
	}
	identity.PairingKey = base64.StdEncoding.EncodeToString(key)
	pairing, err := json.Marshal(identity)
	if err != nil {
		return nil, 500, errors.New("could not encode guest pairing")
	}
	return pairing, 200, nil
}
