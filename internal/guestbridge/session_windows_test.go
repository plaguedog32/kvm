//go:build windows

package guestbridge

import (
	"encoding/binary"
	"testing"
)

func TestSessionInfoRejectsLockedDisconnectedAndMalformed(t *testing.T) {
	valid := make([]byte, 256)
	binary.LittleEndian.PutUint32(valid, 1)
	binary.LittleEndian.PutUint32(valid[8:], 3)
	binary.LittleEndian.PutUint32(valid[16:], 1)
	binary.LittleEndian.PutUint16(valid[86:], 'a')
	if _, err := parseConsoleInfo(valid, 3); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		offset int
		value  uint32
	}{{"unknown level", 0, 2}, {"other user session", 8, 4}, {"disconnected", 12, 4}, {"locked", 16, 0}, {"unknown lock state", 16, 0xffffffff}} {
		t.Run(test.name, func(t *testing.T) {
			bad := append([]byte(nil), valid...)
			binary.LittleEndian.PutUint32(bad[test.offset:], test.value)
			if _, err := parseConsoleInfo(bad, 3); err == nil {
				t.Fatal("unsafe session accepted")
			}
		})
	}
	if _, err := parseConsoleInfo(valid[:163], 3); err == nil {
		t.Fatal("truncated state accepted")
	}
	valid[86], valid[87] = 0, 0
	if _, err := parseConsoleInfo(valid, 3); err == nil {
		t.Fatal("empty user accepted")
	}
}
