//go:build windows

package guestbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	hidDLL                  = windows.NewLazySystemDLL("hid.dll")
	hidGetAttributes        = hidDLL.NewProc("HidD_GetAttributes")
	hidGetPreparsedData     = hidDLL.NewProc("HidD_GetPreparsedData")
	hidFreePreparsedData    = hidDLL.NewProc("HidD_FreePreparsedData")
	hidGetCaps              = hidDLL.NewProc("HidP_GetCaps")
	hidSetNumInputBuffers   = hidDLL.NewProc("HidD_SetNumInputBuffers")
	hidGetSerial            = hidDLL.NewProc("HidD_GetSerialNumberString")
	setupDLL                = windows.NewLazySystemDLL("setupapi.dll")
	setupEnumInterfaces     = setupDLL.NewProc("SetupDiEnumDeviceInterfaces")
	setupGetInterfaceDetail = setupDLL.NewProc("SetupDiGetDeviceInterfaceDetailW")
)

type HIDIdentity struct {
	VendorID  uint16
	ProductID uint16
	Serial    string
}

type hidAttributes struct {
	Size                         uint32
	VendorID, ProductID, Version uint16
}
type hidCaps struct {
	Usage, UsagePage, InputLength, OutputLength, FeatureLength uint16
	Reserved                                                   [17]uint16
	Counts                                                     [10]uint16
}
type hidInterfaceData struct {
	Size     uint32
	Class    windows.GUID
	Flags    uint32
	Reserved uintptr
}

// DiscoverHIDReports excludes keyboard/mouse collections and the legacy
// one-byte wake collection. Mutual authentication still establishes identity;
// VID/PID and optional serial numbers only narrow device discovery.
func DiscoverHIDReports(identity HIDIdentity) ([]string, error) {
	guid := windows.GUID{Data1: 0x4d1e55b2, Data2: 0xf16f, Data3: 0x11cf, Data4: [8]byte{0x88, 0xcb, 0, 0x11, 0x11, 0, 0, 0x30}}
	set, err := windows.SetupDiGetClassDevsEx(&guid, "", 0, windows.DIGCF_PRESENT|windows.DIGCF_DEVICEINTERFACE, 0, "")
	if err != nil {
		return nil, err
	}
	defer windows.SetupDiDestroyDeviceInfoList(set)
	var paths []string
	for i := uint32(0); i < 4096; i++ {
		data := hidInterfaceData{Size: uint32(unsafe.Sizeof(hidInterfaceData{}))}
		ok, _, callErr := setupEnumInterfaces.Call(uintptr(set), 0, uintptr(unsafe.Pointer(&guid)), uintptr(i), uintptr(unsafe.Pointer(&data)))
		if ok == 0 {
			if errors.Is(callErr, windows.ERROR_NO_MORE_ITEMS) {
				break
			}
			return nil, fmt.Errorf("enumerate USB HID interfaces: %w", callErr)
		}
		var required uint32
		setupGetInterfaceDetail.Call(uintptr(set), uintptr(unsafe.Pointer(&data)), 0, 0, uintptr(unsafe.Pointer(&required)), 0)
		if required < 6 || required > 65536 {
			continue
		}
		detail := make([]byte, required)
		cbSize := uint32(8)
		if unsafe.Sizeof(uintptr(0)) == 4 {
			cbSize = 6
		}
		binary.LittleEndian.PutUint32(detail, cbSize)
		ok, _, _ = setupGetInterfaceDetail.Call(uintptr(set), uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&detail[0])), uintptr(required), 0, 0)
		if ok == 0 {
			continue
		}
		path := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&detail[4])), (len(detail)-4)/2))
		if matchingHID(path, identity) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func matchingHID(path string, identity HIDIdentity) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	attrs := hidAttributes{Size: uint32(unsafe.Sizeof(hidAttributes{}))}
	ok, _, _ := hidGetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attrs)))
	if ok == 0 || attrs.VendorID != identity.VendorID || attrs.ProductID != identity.ProductID {
		return false
	}
	var parsed uintptr
	ok, _, _ = hidGetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&parsed)))
	if ok == 0 {
		return false
	}
	defer hidFreePreparsedData.Call(parsed)
	var caps hidCaps
	status, _, _ := hidGetCaps.Call(parsed, uintptr(unsafe.Pointer(&caps)))
	if int32(status) < 0 || caps.UsagePage != 0xff00 || caps.Usage != 1 || caps.InputLength != ReportSize+1 || caps.OutputLength != ReportSize+1 {
		return false
	}
	if identity.Serial != "" {
		var serial [256]uint16
		ok, _, _ = hidGetSerial.Call(uintptr(h), uintptr(unsafe.Pointer(&serial[0])), uintptr(unsafe.Sizeof(serial)))
		if ok == 0 || windows.UTF16ToString(serial[:]) != identity.Serial {
			return false
		}
	}
	return true
}

// HIDReports uses overlapped reads and control output reports. Output goes
// through endpoint zero, preserving the KVM's limited interrupt endpoints.
type HIDReports struct {
	mu          sync.RWMutex
	read, write windows.Handle
	done        chan struct{}
	closeOnce   sync.Once
}

func OpenHIDReports(path string) (*HIDReports, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	read, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	ok, _, callErr := hidSetNumInputBuffers.Call(uintptr(read), 512)
	if ok == 0 {
		windows.CloseHandle(read)
		return nil, fmt.Errorf("configure USB input buffer: %w", callErr)
	}
	write, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		windows.CloseHandle(read)
		return nil, err
	}
	return &HIDReports{read: read, write: write, done: make(chan struct{})}, nil
}

func (h *HIDReports) Close() error {
	var result error
	h.closeOnce.Do(func() {
		close(h.done)
		h.mu.RLock()
		_ = windows.CancelIoEx(h.read, nil)
		_ = windows.CancelIoEx(h.write, nil)
		h.mu.RUnlock()
		h.mu.Lock()
		defer h.mu.Unlock()
		result = errors.Join(windows.CloseHandle(h.read), windows.CloseHandle(h.write))
	})
	return result
}

func (h *HIDReports) transfer(ctx context.Context, data []byte, write bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	select {
	case <-h.done:
		return io.ErrClosedPipe
	default:
	}
	handle := h.read
	if write {
		handle = h.write
	}
	var pin runtime.Pinner
	pin.Pin(&data[0])
	defer pin.Unpin()
	count, err := runOverlapped(ctx, h.done, handle, func(overlap *windows.Overlapped, count *uint32) error {
		if write {
			// hidclass.h: HID_IN_CTL_CODE(101), METHOD_IN_DIRECT. This is the
			// overlapped equivalent of HidD_SetOutputReport (including ID zero).
			const setOutputReport = (0x000b << 16) | (101 << 2) | 1
			return windows.DeviceIoControl(handle, setOutputReport, &data[0], uint32(len(data)), nil, 0, count, overlap)
		} else {
			return windows.ReadFile(handle, data, count, overlap)
		}
	})
	if err != nil {
		return err
	}
	if !write && count != uint32(len(data)) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (h *HIDReports) ReadReport(ctx context.Context) ([ReportSize]byte, error) {
	var report [ReportSize]byte
	var data [ReportSize + 1]byte
	if err := h.transfer(ctx, data[:], false); err != nil {
		return report, err
	}
	if data[0] != 0 {
		return report, ErrProtocol
	}
	copy(report[:], data[1:])
	return report, nil
}

func (h *HIDReports) WriteReport(ctx context.Context, report [ReportSize]byte) error {
	var data [ReportSize + 1]byte
	copy(data[1:], report[:])
	return h.transfer(ctx, data[:], true)
}
