//go:build windows

package guestbridge

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	registerWindowClass      = user32DLL.NewProc("RegisterClassExW")
	unregisterWindowClass    = user32DLL.NewProc("UnregisterClassW")
	createWindow             = user32DLL.NewProc("CreateWindowExW")
	destroyWindow            = user32DLL.NewProc("DestroyWindow")
	defaultWindowProc        = user32DLL.NewProc("DefWindowProcW")
	getWindowMessage         = user32DLL.NewProc("GetMessageW")
	translateWindowMessage   = user32DLL.NewProc("TranslateMessage")
	dispatchWindowMessage    = user32DLL.NewProc("DispatchMessageW")
	postWindowMessage        = user32DLL.NewProc("PostMessageW")
	postQuitMessage          = user32DLL.NewProc("PostQuitMessage")
	openClipboard            = user32DLL.NewProc("OpenClipboard")
	closeClipboard           = user32DLL.NewProc("CloseClipboard")
	emptyClipboard           = user32DLL.NewProc("EmptyClipboard")
	getClipboardSequence     = user32DLL.NewProc("GetClipboardSequenceNumber")
	getClipboardData         = user32DLL.NewProc("GetClipboardData")
	registerClipboardFormat  = user32DLL.NewProc("RegisterClipboardFormatW")
	setClipboardData         = user32DLL.NewProc("SetClipboardData")
	clipboardFormatAvailable = user32DLL.NewProc("IsClipboardFormatAvailable")
	kernelDLL                = windows.NewLazySystemDLL("kernel32.dll")
	getModuleHandle          = kernelDLL.NewProc("GetModuleHandleW")
	globalAlloc              = kernelDLL.NewProc("GlobalAlloc")
	globalFree               = kernelDLL.NewProc("GlobalFree")
	globalLock               = kernelDLL.NewProc("GlobalLock")
	globalUnlock             = kernelDLL.NewProc("GlobalUnlock")
	globalSize               = kernelDLL.NewProc("GlobalSize")
)

const clipboardMessage = 0x8001

type clipboardCall struct {
	ctx     context.Context
	request Request
	reply   chan Response
}
type clipboardUI struct {
	window  atomic.Uintptr
	queue   chan clipboardCall
	done    chan struct{}
	session uint32
}

type windowClass struct {
	Size, Style                        uint32
	Callback                           uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type windowMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

func newClipboardUI(ctx context.Context, session uint32) (*clipboardUI, error) {
	u := &clipboardUI{session: session, queue: make(chan clipboardCall, 8), done: make(chan struct{})}
	ready := make(chan error, 1)
	go u.run(ctx, ready)
	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
		return u, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (u *clipboardUI) run(ctx context.Context, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(u.done)
	instance, _, _ := getModuleHandle.Call(0)
	name := windows.StringToUTF16Ptr("JetKVMGuestClipboardV1")
	callback := windows.NewCallback(func(window uintptr, message uint32, wParam, lParam uintptr) uintptr {
		switch message {
		case clipboardMessage:
			for {
				select {
				case call := <-u.queue:
					response := Response{ID: call.request.ID}
					if ctx.Err() != nil {
						response.Error = "clipboard worker is stopping"
					} else {
						response = u.execute(window, call)
					}
					call.reply <- response
				default:
					return 0
				}
			}
		case 0x10: // WM_CLOSE
			destroyWindow.Call(window)
			return 0
		case 2: // WM_DESTROY
			u.window.Store(0)
			postQuitMessage.Call(0)
			return 0
		}
		result, _, _ := defaultWindowProc.Call(window, uintptr(message), wParam, lParam)
		return result
	})
	class := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), Callback: callback, Instance: instance, Name: name}
	atom, _, _ := registerWindowClass.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		ready <- errors.New("register clipboard window failed")
		return
	}
	defer unregisterWindowClass.Call(uintptr(unsafe.Pointer(name)), instance)
	// HWND_MESSAGE = -3. The window owns clipboard data without appearing in
	// the taskbar or taking focus away from the user's application.
	window, _, _ := createWindow.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, instance, 0)
	if window == 0 {
		ready <- errors.New("create clipboard window failed")
		return
	}
	u.window.Store(window)
	defer func() {
		if h := u.window.Load(); h != 0 {
			destroyWindow.Call(h)
		}
	}()
	ready <- nil
	go func() {
		select {
		case <-ctx.Done():
			if h := u.window.Load(); h != 0 {
				postWindowMessage.Call(h, 0x10, 0, 0)
			}
		case <-u.done:
		}
	}()
	var message windowMessage
	for {
		result, _, _ := getWindowMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) <= 0 {
			break
		}
		translateWindowMessage.Call(uintptr(unsafe.Pointer(&message)))
		dispatchWindowMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func (u *clipboardUI) call(ctx context.Context, request Request) (Response, error) {
	call := clipboardCall{ctx: ctx, request: request, reply: make(chan Response, 1)}
	select {
	case u.queue <- call:
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-u.done:
		return Response{}, errors.New("clipboard worker closed")
	}
	ok, _, _ := postWindowMessage.Call(u.window.Load(), clipboardMessage, 0, 0)
	if ok == 0 {
		return Response{}, errors.New("clipboard worker unavailable")
	}
	select {
	case result := <-call.reply:
		return result, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-u.done:
		return Response{}, errors.New("clipboard worker closed")
	}
}

func (u *clipboardUI) execute(window uintptr, call clipboardCall) Response {
	r := Response{ID: call.request.ID}
	if call.ctx.Err() != nil {
		r.Error = "clipboard operation canceled"
		return r
	}
	if !workerDesktopUnlocked(u.session) {
		r.Error = ErrLocked.Error()
		return r
	}
	var err error
	switch call.request.Method {
	case "status":
	case "clipboard_state":
		sequence, _, _ := getClipboardSequence.Call()
		r.ClipboardSequence = uint32(sequence)
	case "get_clipboard", "copy_clipboard":
		r.Text, r.HasText, r.ClipboardSequence, err = readWindowsClipboard(call.ctx, window)
		if call.request.Method == "get_clipboard" {
			r.ClipboardSequence = 0
		}
	case "set_clipboard":
		err = writeWindowsClipboard(call.ctx, window, call.request.Text, u.session)
	default:
		err = errors.New("unsupported guest clipboard operation")
	}
	if !workerDesktopUnlocked(u.session) {
		r.Text = ""
		r.HasText = false
		r.ClipboardSequence = 0
		if call.request.Method == "set_clipboard" {
			err = errors.New("session locked during clipboard update; clipboard may have changed")
		} else {
			err = ErrLocked
		}
	} else {
		r.Unlocked = true
	}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

func openWindowsClipboard(ctx context.Context, window uintptr) error {
	deadline := time.Now().Add(time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, _, _ := openClipboard.Call(window)
		if ok != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("clipboard is busy")
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func readWindowsClipboard(ctx context.Context, window uintptr) (string, bool, uint32, error) {
	if err := openWindowsClipboard(ctx, window); err != nil {
		return "", false, 0, err
	}
	defer closeClipboard.Call()
	sequence, _, _ := getClipboardSequence.Call()
	available, _, _ := clipboardFormatAvailable.Call(13) // CF_UNICODETEXT
	if available == 0 {
		return "", false, uint32(sequence), nil
	}
	handle, _, _ := getClipboardData.Call(13)
	if handle == 0 {
		return "", false, 0, errors.New("read clipboard text failed")
	}
	size, _, _ := globalSize.Call(handle)
	if size < 2 || size%2 != 0 {
		return "", false, 0, errors.New("invalid clipboard text allocation")
	}
	pointer, _, _ := globalLock.Call(handle)
	if pointer == 0 {
		return "", false, 0, errors.New("lock clipboard text failed")
	}
	defer globalUnlock.Call(handle)
	units := unsafe.Slice((*uint16)(unsafe.Pointer(pointer)), int(min(size/2, MaxText+1)))
	text, err := DecodeClipboardUTF16(units)
	return text, err == nil, uint32(sequence), err
}

func writeWindowsClipboard(ctx context.Context, window uintptr, text string, session uint32) error {
	units, err := EncodeClipboardUTF16(text)
	if err != nil {
		return err
	}
	handle, _, _ := globalAlloc.Call(2, uintptr(len(units)*2)) // GMEM_MOVEABLE
	if handle == 0 {
		return errors.New("allocate clipboard text failed")
	}
	owned := true
	defer func() {
		if owned {
			globalFree.Call(handle)
		}
	}()
	pointer, _, _ := globalLock.Call(handle)
	if pointer == 0 {
		return errors.New("lock new clipboard text failed")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(pointer)), len(units)), units)
	globalUnlock.Call(handle)
	if err := openWindowsClipboard(ctx, window); err != nil {
		return err
	}
	defer closeClipboard.Call()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !workerDesktopUnlocked(session) {
		return ErrLocked
	}
	ok, _, _ := emptyClipboard.Call()
	if ok == 0 {
		return errors.New("empty clipboard failed")
	}
	if err := excludeClipboardHistory(); err != nil {
		return err
	}
	result, _, _ := setClipboardData.Call(13, handle)
	if result == 0 {
		return fmt.Errorf("set clipboard text failed; clipboard may have changed")
	}
	owned = false // Ownership of the HGLOBAL now belongs to Windows.
	return nil
}

// Request-only clipboard text must not become Windows history or cloud sync.
// Registered format payloads are serialized DWORD zero, owned by Windows after
// SetClipboardData. Set these while the clipboard is still open, before text.
func excludeClipboardHistory() error {
	for _, name := range []string{"ExcludeClipboardContentFromMonitorProcessing", "CanIncludeInClipboardHistory", "CanUploadToCloudClipboard"} {
		format, _, _ := registerClipboardFormat.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(name))))
		if format == 0 {
			return errors.New("register private clipboard format failed")
		}
		block, _, _ := globalAlloc.Call(0x42, 4) // GMEM_MOVEABLE | GMEM_ZEROINIT
		if block == 0 {
			return errors.New("allocate private clipboard format failed")
		}
		result, _, _ := setClipboardData.Call(format, block)
		if result == 0 {
			globalFree.Call(block)
			return errors.New("set private clipboard format failed; clipboard may have changed")
		}
	}
	return nil
}
