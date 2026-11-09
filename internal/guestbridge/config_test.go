package guestbridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPairingConfigurationValidationAndRedaction(t *testing.T) {
	config := PairingConfig{Version: 1, PairingKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)), VendorID: 0x1d6b, ProductID: 0x0104, Serial: "unit"}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePairingConfig(data)
	if err != nil || got.Serial != "unit" {
		t.Fatal(err)
	}
	for _, render := range []string{fmt.Sprint(config), fmt.Sprintf("%#v", config)} {
		if strings.Contains(render, config.PairingKey) {
			t.Fatal("pairing key leaked through diagnostics")
		}
	}
	for _, alter := range []func(*PairingConfig){func(p *PairingConfig) { p.Version = 2 }, func(p *PairingConfig) { p.PairingKey = "x" }, func(p *PairingConfig) { p.VendorID = 0 }, func(p *PairingConfig) { p.ProductID = 0 }, func(p *PairingConfig) { p.Serial = "a\x00b" }} {
		bad := config
		alter(&bad)
		data, _ := json.Marshal(bad)
		if _, err := DecodePairingConfig(data); err == nil {
			t.Fatal("invalid pairing configuration accepted")
		}
	}
	if _, err := DecodePairingConfig(append(data, []byte("{}")...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if _, err := DecodePairingConfig([]byte(`{"version":1,"command":"run"}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}
