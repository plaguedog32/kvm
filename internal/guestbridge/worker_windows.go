//go:build windows

package guestbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type clipboardWorker struct {
	pipe         *localPipe
	process      windows.Handle
	pid, session uint32
	sid          string
}

func consoleToken(info consoleInfo) (windows.Token, string, error) {
	var token windows.Token
	if err := windows.WTSQueryUserToken(info.ID, &token); err != nil {
		return 0, "", ErrLocked
	}
	user, err := token.GetTokenUser()
	if err != nil {
		token.Close()
		return 0, "", err
	}
	return token, user.User.Sid.String(), nil
}

func launchClipboardWorker(ctx context.Context, info consoleInfo) (*clipboardWorker, error) {
	token, sid, err := consoleToken(info)
	if err != nil {
		return nil, err
	}
	defer token.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	name := `\\.\pipe\JetKVMGuest-` + hex.EncodeToString(nonce[:])
	pipe, err := createLocalPipe(name, sid)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		pipe.Close()
		return nil, err
	}
	var environment *uint16
	if err := windows.CreateEnvironmentBlock(&environment, token, false); err != nil {
		pipe.Close()
		return nil, err
	}
	defer windows.DestroyEnvironmentBlock(environment)
	arguments := []string{exe, "--clipboard-worker", "--pipe", name, "--parent-pid", strconv.FormatUint(uint64(windows.GetCurrentProcessId()), 10), "--session-id", strconv.FormatUint(uint64(info.ID), 10)}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(arguments))
	if err != nil {
		pipe.Close()
		return nil, err
	}
	application, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		pipe.Close()
		return nil, err
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: windows.StringToUTF16Ptr(`winsta0\default`), Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	var process windows.ProcessInformation
	if err := windows.CreateProcessAsUser(token, application, command, nil, nil, false, windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, environment, nil, &startup, &process); err != nil {
		pipe.Close()
		return nil, err
	}
	windows.CloseHandle(process.Thread)
	worker := &clipboardWorker{pipe: pipe, process: process.Process, pid: process.ProcessId, session: info.ID, sid: sid}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pipe.connect(connectCtx); err != nil {
		worker.Close()
		return nil, err
	}
	var client, session uint32
	if windows.GetNamedPipeClientProcessId(pipe.handle, &client) != nil || client != worker.pid || windows.ProcessIdToSessionId(client, &session) != nil || session != info.ID {
		worker.Close()
		return nil, ErrProtocol
	}
	return worker, nil
}

func (w *clipboardWorker) Close() {
	if w == nil {
		return
	}
	w.pipe.Close()
	_ = windows.TerminateProcess(w.process, 0)
	_, _ = windows.WaitForSingleObject(w.process, 2000)
	windows.CloseHandle(w.process)
}

type workerManager struct{ worker *clipboardWorker }

// The Windows service handles one authenticated USB request at a time. This
// manager is deliberately private to that loop, so a session switch cannot
// race a second concurrent clipboard operation.
func (m *workerManager) dispatch(ctx context.Context, request Request) Response {
	response := Response{ID: request.ID}
	if request.Method == "clipboard_capabilities" {
		response.NativeCopy = true
		return response
	}
	if request.Method != "status" && request.Method != "clipboard_state" && request.Method != "get_clipboard" && request.Method != "copy_clipboard" && request.Method != "set_clipboard" {
		response.Error = "unsupported guest clipboard operation"
		return response
	}
	if request.Method == "set_clipboard" {
		if err := ValidateText(request.Text); err != nil {
			response.Error = err.Error()
			return response
		}
	}
	info, err := activeConsole()
	if err != nil {
		m.close()
		response.Error = ErrLocked.Error()
		return response
	}
	token, sid, err := consoleToken(info)
	if err != nil {
		m.close()
		response.Error = ErrLocked.Error()
		return response
	}
	token.Close()
	if m.worker != nil {
		state, waitErr := windows.WaitForSingleObject(m.worker.process, 0)
		if m.worker.session != info.ID || m.worker.sid != sid || waitErr != nil || state != uint32(windows.WAIT_TIMEOUT) {
			m.close()
		}
	}
	if m.worker == nil {
		m.worker, err = launchClipboardWorker(ctx, info)
		if err != nil {
			response.Error = "clipboard worker unavailable"
			return response
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := m.worker.pipe.Send(callCtx, request); err != nil {
		m.close()
		response.Error = "clipboard worker disconnected"
		if request.Method == "set_clipboard" {
			response.Error += "; clipboard may have changed"
		}
		return response
	}
	if err := m.worker.pipe.Receive(callCtx, &response); err != nil {
		m.close()
		message := "clipboard worker did not complete"
		if request.Method == "set_clipboard" {
			message += "; clipboard may have changed"
		}
		return Response{ID: request.ID, Error: message}
	}
	if response.ID != request.ID {
		m.close()
		return Response{ID: request.ID, Error: "invalid clipboard worker response"}
	}
	after, err := activeConsole()
	if err != nil || after.ID != info.ID {
		m.close()
		return Response{ID: request.ID, Error: ErrLocked.Error()}
	}
	token, afterSID, err := consoleToken(after)
	if err == nil {
		token.Close()
	}
	if err != nil || afterSID != sid {
		m.close()
		return Response{ID: request.ID, Error: ErrLocked.Error()}
	}
	response.User = after.User
	return response
}

func (m *workerManager) close() {
	if m.worker != nil {
		m.worker.Close()
		m.worker = nil
	}
}

// RunClipboardWorker is entered before loading any service configuration. The
// worker never receives or opens the USB pairing key and exposes no shell API.
func RunClipboardWorker(parent, session uint32, name string) error {
	if parent == 0 || session == 0 || !strings.HasPrefix(name, `\\.\pipe\JetKVMGuest-`) {
		return ErrProtocol
	}
	suffix := strings.TrimPrefix(name, `\\.\pipe\JetKVMGuest-`)
	if len(suffix) != 32 {
		return ErrProtocol
	}
	if _, err := hex.DecodeString(suffix); err != nil {
		return ErrProtocol
	}
	if !workerDesktopUnlocked(session) {
		return ErrLocked
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connectCtx, connectCancel := context.WithTimeout(ctx, 5*time.Second)
	pipe, err := openLocalPipe(connectCtx, name, parent)
	connectCancel()
	if err != nil {
		return err
	}
	defer pipe.Close()
	ui, err := newClipboardUI(ctx, session)
	if err != nil {
		return err
	}
	for {
		var request Request
		if err := pipe.Receive(ctx, &request); err != nil {
			return err
		}
		if request.ID == 0 {
			return ErrProtocol
		}
		callCtx, callCancel := context.WithTimeout(ctx, 4*time.Second)
		response, err := ui.call(callCtx, request)
		callCancel()
		if err != nil {
			return fmt.Errorf("clipboard worker operation failed: %w", err)
		}
		if err := pipe.Send(ctx, response); err != nil {
			return err
		}
	}
}
