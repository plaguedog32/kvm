package mcpserver

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestUSKeyboardAndPowerShellEncoding(t *testing.T) {
	for ch := rune(32); ch < 127; ch++ {
		r, err := character(ch)
		if err != nil || r.keys[0] < 4 || r.keys[0] > 56 {
			t.Fatal(ch, r, err)
		}
	}
	for _, tc := range []struct {
		key            string
		mods           []string
		code, modifier byte
	}{{"A", nil, 4, 2}, {"F12", nil, 69, 0}, {"Delete", []string{"Control", "Alt"}, 76, 5}, {"?", nil, 56, 2}} {
		r, err := keyReport(tc.key, tc.mods)
		if err != nil || r.keys[0] != tc.code || r.modifier != tc.modifier {
			t.Fatal(tc, r, err)
		}
	}
	script := "Write-Output '\u03bb\U0001f600'"
	command, err := powershellCommand(script)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(command[strings.LastIndex(command, " ")+1:])
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	if string(utf16.Decode(units)) != script {
		t.Fatal("Unicode script changed")
	}
}
