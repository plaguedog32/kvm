package kvm

import (
	"fmt"

	"github.com/jetkvm/kvm/internal/localonly"
	"github.com/jetkvm/kvm/internal/tailscale"
)

func rpcGetTailscaleStatus() (*tailscale.Status, error) {
	ensureConfigLoaded()
	if localonly.Enabled {
		return &tailscale.Status{}, nil
	}
	return tailscale.GetStatus(config.TailscaleControlURL, func(err error) {
		tailscaleLogger.Warn().Err(err).Msg("failed to get tailscale status")
	})
}

func rpcGetTailscaleControlURL() (string, error) {
	if localonly.Enabled {
		return "", nil
	}
	ensureConfigLoaded()
	return tailscale.EffectiveControlURL(config.TailscaleControlURL), nil
}

func rpcSetTailscaleControlURL(controlURL string) error {
	if localonly.Enabled {
		return localonly.ErrDisabled
	}
	ensureConfigLoaded()

	previousURL := config.TailscaleControlURL

	normalized, err := tailscale.SetControlURL(controlURL)
	if err != nil {
		return err
	}

	config.TailscaleControlURL = normalized
	if err := SaveConfig(); err != nil {
		config.TailscaleControlURL = previousURL
		return fmt.Errorf("failed to save tailscale control URL: %w", err)
	}

	return nil
}
