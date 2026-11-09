package usbgadget

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
)

// OpenGuestHIDConnection admits one independent guest transport during the
// current HID generation. ResetHIDFiles closes it before unbinding USB; the
// transport does not hold hidLifecycle for the lifetime of a connection.
func (u *UsbGadget) OpenGuestHIDConnection(open func(string) (io.Closer, error)) (io.Closer, error) {
	u.hidLifecycle.RLock()
	defer u.hidLifecycle.RUnlock()
	u.guestConnectionLock.Lock()
	defer u.guestConnectionLock.Unlock()
	length, err := os.ReadFile(path.Join(u.kvmGadgetPath, "functions", "hid.usb3", "report_length"))
	if err != nil || string(bytes.TrimSpace(length)) != "64" {
		return nil, fmt.Errorf("guest HID collection is unavailable")
	}
	if u.guestConnection != nil {
		u.guestConnection.Close()
		u.guestConnection = nil
	}
	connection, err := open("/dev/hidg3")
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, fmt.Errorf("guest HID opener returned no connection")
	}
	u.guestConnection = connection
	return connection, nil
}

func (u *UsbGadget) closeGuestConnection() {
	u.guestConnectionLock.Lock()
	defer u.guestConnectionLock.Unlock()
	if u.guestConnection != nil {
		u.guestConnection.Close()
		u.guestConnection = nil
	}
}
