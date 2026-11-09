package guestbridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// PairingConfig is a one-time secret export. It must never be included in a
// status response, log, public download, or per-user worker command line.
type PairingConfig struct {
	Version    int    `json:"version"`
	PairingKey string `json:"pairing_key"`
	VendorID   uint16 `json:"vendor_id"`
	ProductID  uint16 `json:"product_id"`
	Serial     string `json:"serial,omitempty"`
}

func (p PairingConfig) String() string   { return "JetKVM guest pairing [redacted]" }
func (p PairingConfig) GoString() string { return p.String() }

func (p PairingConfig) Key() ([]byte, error) {
	if len(p.PairingKey) != 44 {
		return nil, fmt.Errorf("invalid guest pairing configuration")
	}
	key, err := base64.StdEncoding.DecodeString(p.PairingKey)
	if err != nil || len(key) != 32 || p.Version != 1 || p.VendorID == 0 || p.ProductID == 0 || len(p.Serial) > 256 || !utf8.ValidString(p.Serial) || strings.ContainsRune(p.Serial, 0) {
		return nil, fmt.Errorf("invalid guest pairing configuration")
	}
	return key, nil
}

func DecodePairingConfig(data []byte) (*PairingConfig, error) {
	if len(data) > 4096 || !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid guest pairing configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var p PairingConfig
	if decoder.Decode(&p) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid guest pairing configuration")
	}
	key, err := p.Key()
	clear(key)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
