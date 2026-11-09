//go:build windows

package guestbridge

import (
	"encoding/binary"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wtsDLL            = windows.NewLazySystemDLL("wtsapi32.dll")
	wtsQuerySession   = wtsDLL.NewProc("WTSQuerySessionInformationW")
	user32DLL         = windows.NewLazySystemDLL("user32.dll")
	openInputDesktop  = user32DLL.NewProc("OpenInputDesktop")
	closeDesktop      = user32DLL.NewProc("CloseDesktop")
	getUserObjectInfo = user32DLL.NewProc("GetUserObjectInformationW")
)

type consoleInfo struct {
	ID   uint32
	User string
}

func parseConsoleInfo(data []byte, id uint32) (consoleInfo, error) {
	// WTSINFOEXW has a DWORD level then an eight-byte-aligned union. In the
	// level-one prefix, SessionId/State/Flags are at offsets 8/12/16.
	if len(data) < 164 || binary.LittleEndian.Uint32(data) != 1 || binary.LittleEndian.Uint32(data[8:]) != id || binary.LittleEndian.Uint32(data[12:]) != 0 || binary.LittleEndian.Uint32(data[16:]) != 1 {
		return consoleInfo{}, ErrLocked
	}
	decode := func(data []byte) string {
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(data[i*2:])
		}
		return windows.UTF16ToString(units)
	}
	user, domain := decode(data[86:128]), decode(data[128:164])
	if user == "" {
		return consoleInfo{}, ErrLocked
	}
	if domain != "" {
		user = domain + "\\" + user
	}
	return consoleInfo{ID: id, User: user}, nil
}

func activeConsole() (consoleInfo, error) {
	id := windows.WTSGetActiveConsoleSessionId()
	if id == 0xffffffff || id == 0 {
		return consoleInfo{}, ErrLocked
	}
	var pointer uintptr
	var size uint32
	ok, _, _ := wtsQuerySession.Call(0, uintptr(id), 25, uintptr(unsafe.Pointer(&pointer)), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || pointer == 0 {
		return consoleInfo{}, ErrLocked
	}
	defer windows.WTSFreeMemory(pointer)
	if size < 164 || size > 65536 {
		return consoleInfo{}, ErrLocked
	}
	info, err := parseConsoleInfo(unsafe.Slice((*byte)(unsafe.Pointer(pointer)), int(size)), id)
	if err != nil {
		return consoleInfo{}, err
	}
	if windows.WTSGetActiveConsoleSessionId() != id {
		return consoleInfo{}, ErrLocked
	}
	return info, nil
}

func workerDesktopUnlocked(expectedSession uint32) bool {
	var ownSession uint32
	if windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &ownSession) != nil || ownSession != expectedSession || windows.WTSGetActiveConsoleSessionId() != expectedSession {
		return false
	}
	// Session-zero services cannot infer the user's input desktop. This check
	// intentionally runs inside the unprivileged worker on winsta0\\default.
	desktop, _, _ := openInputDesktop.Call(0, 0, 0x0001) // DESKTOP_READOBJECTS
	if desktop == 0 {
		return false
	}
	defer closeDesktop.Call(desktop)
	var name [128]uint16
	var required uint32
	ok, _, _ := getUserObjectInfo.Call(desktop, 2, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Sizeof(name)), uintptr(unsafe.Pointer(&required))) // UOI_NAME
	if ok == 0 || required > uint32(unsafe.Sizeof(name)) {
		return false
	}
	return strings.EqualFold(windows.UTF16ToString(name[:]), "Default")
}
