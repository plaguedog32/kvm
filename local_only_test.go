package kvm

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLocalOnlyConfigCannotRestoreExternalDefaults(t *testing.T) {
	c := getDefaultConfig()
	c.CloudToken = "old token"
	c.CloudURL = "https://api.jetkvm.com"
	c.CloudAppURL = "https://app.jetkvm.com"
	c.GoogleIdentity = "old identity"
	c.AutoUpdateEnabled = true
	c.UpdateAPIURL = "https://api.jetkvm.com"
	c.MqttConfig.Enabled = true
	c.MqttConfig.Broker = "broker.example.com"
	applyLocalOnlyConfig(&c)
	if c.CloudToken != "" || c.CloudURL != "" || c.CloudAppURL != "" || c.GoogleIdentity != "" || c.AutoUpdateEnabled || c.UpdateAPIURL != "" || c.MqttConfig.Enabled {
		t.Fatal("external config survived the local-only policy")
	}
	c.UpdateAPIURL = "http://192.168.50.2:8769"
	c.MqttConfig.Broker = "192.168.50.3"
	c.MqttConfig.Enabled = true
	applyLocalOnlyConfig(&c)
	if c.UpdateAPIURL == "" || !c.MqttConfig.Enabled {
		t.Fatal("private endpoints were rejected")
	}
}
func TestLocalOnlyBrowserPolicyBlocksExternalResources(t *testing.T) {
	r := gin.New()
	r.Use(localOnlyBrowserPolicy)
	r.GET("/", func(c *gin.Context) { c.String(200, "local") })
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://192.168.50.2/", nil)
	r.ServeHTTP(w, req)
	policy := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "connect-src 'self' ws://192.168.50.2", "frame-src 'none'", "form-action 'self'"} {
		if !strings.Contains(policy, want) {
			t.Error("missing browser boundary", want)
		}
	}
}
