package timesync

import (
	"testing"
	"time"

	"github.com/jetkvm/kvm/internal/localonly"
)

func TestLocalOnlyTimeSourcesDoNotUsePublicFallbacks(t *testing.T) {
	mode := (&TimeSync{}).getSyncMode()
	if mode.NtpUseFallback || mode.HttpUseFallback {
		t.Fatal("public time fallback enabled")
	}
	for _, method := range mode.Ordering {
		if method == "ntp" || method == "http" {
			t.Fatal("public time method included")
		}
	}
	if _, _, err := queryNtpServer("8.8.8.8", time.Second); err != localonly.ErrDisabled {
		t.Fatal("public NTP was not rejected before querying", err)
	}
}
