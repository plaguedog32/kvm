package localonly

import (
	"strings"
	"testing"
)

func TestOnlyLocalHostICECandidatesSurvive(t *testing.T) {
	local := "candidate:1 1 udp 2122260223 192.168.50.2 55000 typ host"
	public := "candidate:2 1 udp 2122260223 8.8.8.8 55000 typ srflx"
	relay := "candidate:3 1 udp 2122260223 192.168.50.2 55000 typ relay"
	mdns := "candidate:4 1 udp 2122260223 browser.local 55000 typ host"
	if !CandidateAllowed(local) || !CandidateAllowed(mdns) || CandidateAllowed(public) || CandidateAllowed(relay) {
		t.Fatal("ICE candidate boundary failed")
	}
	sdp := FilterSDP("v=0\r\na=" + local + "\r\na=" + public + "\r\na=" + relay + "\r\n")
	if strings.Contains(sdp, "8.8.8.8") || strings.Contains(sdp, "typ relay") || !strings.Contains(sdp, local) {
		t.Fatal("external SDP candidate remained")
	}
}
