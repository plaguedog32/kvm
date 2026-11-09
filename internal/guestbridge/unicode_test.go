package guestbridge

import (
	"strings"
	"testing"
)

func TestClipboardUnicodeBoundaries(t *testing.T) {
	for _, text := range []string{"", "hello\r\nworld", "日本語 🐕", strings.Repeat("a", MaxText)} {
		units, err := EncodeClipboardUTF16(text)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeClipboardUTF16(append(units, 0xffff, 0xffff))
		if err != nil || got != text {
			t.Fatalf("Unicode round trip: %v", err)
		}
	}
	for _, units := range [][]uint16{{'x'}, {0xd800, 0}, {0xdc00, 0}, {0xd800, 'a', 0}, {0xd800}, {}} {
		if _, err := DecodeClipboardUTF16(units); err == nil {
			t.Fatalf("malformed UTF-16 accepted: %x", units)
		}
	}
	units := make([]uint16, MaxText/3+2)
	for i := range units[:len(units)-1] {
		units[i] = 0x3042
	}
	if _, err := DecodeClipboardUTF16(units); err == nil {
		t.Fatal("UTF-8 byte limit ignored")
	}
}
