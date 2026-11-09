package kvm

import (
	"net/url"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/jetkvm/kvm/internal/localonly"
)

var localOnlySystemFirewall atomic.Bool

func rpcGetNetworkPolicyState() map[string]any {
	return map[string]any{"application": "private-lan-only", "system_firewall_applied": localOnlySystemFirewall.Load()}
}

func applyLocalOnlyConfig(c *Config) {
	if c == nil || !localonly.Enabled {
		return
	}
	c.CloudToken = ""
	c.CloudURL = ""
	c.CloudAppURL = ""
	c.GoogleIdentity = ""
	c.AutoUpdateEnabled = false
	if c.UpdateAPIURL != "" {
		u, err := url.Parse(c.UpdateAPIURL)
		if err != nil || localonly.CheckURL(u) != nil {
			c.UpdateAPIURL = ""
		}
	}
	if c.NetworkConfig != nil {
		c.NetworkConfig.HTTPProxy.String = ""
		c.NetworkConfig.HTTPProxy.Valid = false
		c.NetworkConfig.TimeSyncDisableFallback.Bool = true
		c.NetworkConfig.TimeSyncDisableFallback.Valid = true
	}
	if c.MqttConfig != nil && c.MqttConfig.Enabled && !localonly.PrivateIP(c.MqttConfig.Broker) {
		c.MqttConfig.Enabled = false
	}
}
func localOnlyCandidate(candidate string) bool { return localonly.CandidateAllowed(candidate) }

func localOnlyBrowserPolicy(c *gin.Context) {
	// Block external scripts, models, fonts, images, frames and API connections.
	// URLs in documentation are inert links, not automatic asset loads.
	connect := "'self'"
	u, err := url.Parse("//" + c.Request.Host)
	if err == nil && localonly.LocalHost(u.Hostname()) && u.User == nil {
		connect += " ws://" + c.Request.Host + " wss://" + c.Request.Host
	}
	c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src "+connect+"; worker-src 'self' blob:; frame-src 'none'; object-src 'none'; base-uri 'self'; form-action 'self'")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-JetKVM-Network-Policy", "private-lan-only")
	c.Next()
}
