//go:build linux

package guestbridge

import (
	"context"
	"errors"
	"io"
	"sync"

	"golang.org/x/sys/unix"
)

// HIDReports uses an independent descriptor so gadget reconfiguration is never
// blocked by an idle guest connection. Rebind errors terminate the connection;
// the owning manager must authenticate a new connection after reopening it.
type HIDReports struct {
	mu sync.RWMutex
	fd int
}

func OpenHIDReports(path string) (*HIDReports, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &HIDReports{fd: fd}, nil
}

func (h *HIDReports) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fd < 0 {
		return nil
	}
	fd := h.fd
	h.fd = -1
	return unix.Close(fd)
}

func (h *HIDReports) transfer(ctx context.Context, data []byte, write bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.mu.RLock()
		if h.fd < 0 {
			h.mu.RUnlock()
			return io.ErrClosedPipe
		}
		var n int
		var err error
		if write {
			n, err = unix.Write(h.fd, data)
		} else {
			n, err = unix.Read(h.fd, data)
		}
		if err == nil {
			h.mu.RUnlock()
			if n != len(data) {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			h.mu.RUnlock()
			continue
		}
		if !errors.Is(err, unix.EAGAIN) {
			h.mu.RUnlock()
			return err
		}
		events := int16(unix.POLLIN)
		if write {
			events = unix.POLLOUT
		}
		poll := []unix.PollFd{{Fd: int32(h.fd), Events: events}}
		// Keep the descriptor protected from Close/reuse only for this short
		// poll, never for the lifetime of a connection or clipboard operation.
		_, err = unix.Poll(poll, 25)
		h.mu.RUnlock()
		if err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
		if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return io.ErrClosedPipe
		}
	}
}

func (h *HIDReports) ReadReport(ctx context.Context) ([ReportSize]byte, error) {
	var r [ReportSize]byte
	err := h.transfer(ctx, r[:], false)
	return r, err
}

func (h *HIDReports) WriteReport(ctx context.Context, r [ReportSize]byte) error {
	return h.transfer(ctx, r[:], true)
}
