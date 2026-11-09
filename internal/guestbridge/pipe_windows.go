//go:build windows

package guestbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

type localPipe struct {
	mu      sync.RWMutex
	writeMu sync.Mutex
	handle  windows.Handle
	done    chan struct{}
	once    sync.Once
}

func createLocalPipe(name, sid string) (*localPipe, error) {
	if _, err := windows.StringToSid(sid); err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("O:SYG:SYD:P(A;;GA;;;SY)(A;;GRGW;;;" + sid + ")")
	if err != nil {
		return nil, err
	}
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	// Message mode rejects truncated/oversized frames; remote clients and
	// additional server instances are never accepted.
	h, err := windows.CreateNamedPipe(path, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 65536, 65536, 0, &security)
	if err != nil {
		return nil, err
	}
	return &localPipe{handle: h, done: make(chan struct{})}, nil
}

func (p *localPipe) connect(ctx context.Context) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, err := runOverlapped(ctx, p.done, p.handle, func(overlap *windows.Overlapped, _ *uint32) error {
		err := windows.ConnectNamedPipe(p.handle, overlap)
		if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			return nil
		}
		return err
	})
	return err
}

func openLocalPipe(ctx context.Context, name string, parent uint32) (*localPipe, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			var server uint32
			if windows.GetNamedPipeServerProcessId(h, &server) != nil || server != parent {
				windows.CloseHandle(h)
				return nil, ErrProtocol
			}
			mode := uint32(windows.PIPE_READMODE_MESSAGE)
			if err := windows.SetNamedPipeHandleState(h, &mode, nil, nil); err != nil {
				windows.CloseHandle(h)
				return nil, err
			}
			return &localPipe{handle: h, done: make(chan struct{})}, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *localPipe) Close() error {
	var result error
	p.once.Do(func() {
		close(p.done)
		p.mu.RLock()
		_ = windows.CancelIoEx(p.handle, nil)
		p.mu.RUnlock()
		p.mu.Lock()
		defer p.mu.Unlock()
		result = windows.CloseHandle(p.handle)
	})
	return result
}

func (p *localPipe) transfer(ctx context.Context, data []byte, write bool) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var pin runtime.Pinner
	pin.Pin(&data[0])
	defer pin.Unpin()
	count, err := runOverlapped(ctx, p.done, p.handle, func(overlap *windows.Overlapped, count *uint32) error {
		if write {
			return windows.WriteFile(p.handle, data, count, overlap)
		}
		return windows.ReadFile(p.handle, data, count, overlap)
	})
	if err != nil {
		return err
	}
	if count != uint32(len(data)) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (p *localPipe) Send(ctx context.Context, value any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if data.Len() == 0 || data.Len() > MaxWire {
		return ErrProtocol
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(data.Len()))
	if err := p.transfer(ctx, header[:], true); err != nil {
		return err
	}
	return p.transfer(ctx, data.Bytes(), true)
}

func (p *localPipe) Receive(ctx context.Context, value any) error {
	var header [4]byte
	if err := p.transfer(ctx, header[:], false); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxWire {
		return ErrProtocol
	}
	data := make([]byte, int(size))
	if err := p.transfer(ctx, data, false); err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrProtocol
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrProtocol
	}
	return nil
}
