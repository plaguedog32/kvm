package usbgadget

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestGuestHIDPreservesLegacyTopologyAndConfigurationIsolation(t *testing.T) {
	u := &UsbGadget{configMap: deepCopyConfigMap(defaultGadgetConfig), customConfig: Config{isEmpty: true}}
	u.enabledDevices = defaultUsbGadgetDevices
	u.enabledDevices.GuestIntegration = true
	u.loadGuestHIDConfig()
	item := u.configMap["wake_hid"]
	if item.device != "hid.usb3" || item.order != 1003 || item.attrs["report_length"] != "64" || item.attrs["no_out_endpoint"] != "1" || item.attrs["wakeup_on_write"] != "1" {
		t.Fatal("guest integration changed USB topology")
	}
	if !bytes.Contains(item.reportDesc, []byte{0x81, 0x02}) || !bytes.Contains(item.reportDesc, []byte{0x91, 0x02}) {
		t.Fatal("guest descriptor is not bidirectional")
	}
	if len(u.configMap) != len(defaultGadgetConfig) || defaultGadgetConfig["wake_hid"].attrs["report_length"] != "1" {
		t.Fatal("global gadget configuration changed")
	}
	legacy := defaultUsbGadgetDevices
	u.SetGadgetDevices(&legacy)
	if u.configMap["wake_hid"].attrs["report_length"] != "1" || !bytes.Equal(u.configMap["wake_hid"].reportDesc, wakeReportDesc) {
		t.Fatal("disabled integration changed legacy wake reports")
	}
	encoded, _ := json.Marshal(Devices{GuestIntegration: true})
	if bytes.Contains(encoded, []byte("GuestIntegration")) || bytes.Contains(encoded, []byte("guest")) {
		t.Fatal("pairing flag leaked into legacy USB settings")
	}
}

func TestWakeReportUsesActualKernelLengthDuringGuestDisable(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	u := newTestGadgetWithKeyboard(nil)
	u.wakeHidFile = write
	u.kvmGadgetPath = t.TempDir()
	function := filepath.Join(u.kvmGadgetPath, "functions", "hid.usb3")
	if err := os.MkdirAll(function, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(function, "report_length"), []byte("64\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Desired guest mode is false, but the existing kernel collection is 64.
	if err := u.wakeWriteHidFile(1); err != nil {
		t.Fatal(err)
	}
	var report [64]byte
	n, err := read.Read(report[:])
	if err != nil || n != 64 || report[0] != 1 || !bytes.Equal(report[1:], make([]byte, 63)) {
		t.Fatalf("wake report length %d, error %v", n, err)
	}
}

type guestTestCloser struct{ closed bool }

func (c *guestTestCloser) Close() error { c.closed = true; return nil }
func TestGuestConnectionClosedForRebindAndUsesLiveDescriptor(t *testing.T) {
	u := newTestGadgetWithKeyboard(nil)
	u.kvmGadgetPath = t.TempDir()
	function := filepath.Join(u.kvmGadgetPath, "functions", "hid.usb3")
	if err := os.MkdirAll(function, 0700); err != nil {
		t.Fatal(err)
	}
	length := filepath.Join(function, "report_length")
	if err := os.WriteFile(length, []byte("64"), 0600); err != nil {
		t.Fatal(err)
	}
	first := &guestTestCloser{}
	if _, err := u.OpenGuestHIDConnection(func(name string) (io.Closer, error) {
		if name != "/dev/hidg3" {
			t.Fatal(name)
		}
		return first, nil
	}); err != nil {
		t.Fatal(err)
	}
	u.ResetHIDFiles()
	if !first.closed || u.guestConnection != nil {
		t.Fatal("rebind retained guest handle")
	}
	if err := os.WriteFile(length, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := u.OpenGuestHIDConnection(func(string) (io.Closer, error) { called = true; return &guestTestCloser{}, nil }); err == nil || called {
		t.Fatal("legacy collection admitted guest transport")
	}
}
