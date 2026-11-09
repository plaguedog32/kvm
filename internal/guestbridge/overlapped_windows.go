//go:build windows

package guestbridge

import (
	"context"
	"errors"
	"io"
	"runtime"

	"golang.org/x/sys/windows"
)

// runOverlapped keeps the OVERLAPPED/event alive through cancellation. Callers
// pin any data buffers and keep the handle open until this function returns.
func runOverlapped(ctx context.Context, closed <-chan struct{}, handle windows.Handle, start func(*windows.Overlapped, *uint32) error) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	select {
	case <-closed:
		return 0, io.ErrClosedPipe
	default:
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlap := &windows.Overlapped{HEvent: event}
	var pin runtime.Pinner
	pin.Pin(overlap)
	defer pin.Unpin()
	var count uint32
	err = start(overlap, &count)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		for {
			status, waitErr := windows.WaitForSingleObject(event, 25)
			if waitErr != nil {
				_ = windows.CancelIoEx(handle, overlap)
				err = waitErr
				break
			}
			if status == windows.WAIT_OBJECT_0 {
				err = nil
				break
			}
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-closed:
				err = io.ErrClosedPipe
			default:
				continue
			}
			_ = windows.CancelIoEx(handle, overlap)
			break
		}
		completionErr := windows.GetOverlappedResult(handle, overlap, &count, true)
		if err == nil {
			err = completionErr
		}
	}
	return count, err
}
