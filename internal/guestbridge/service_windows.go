//go:build windows

package guestbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const WindowsServiceName = "JetKVMGuest"

type guestWindowsService struct{ log *eventlog.Log }

func RunWindowsService() error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return errors.New("guest service must run as LocalSystem")
	}
	log, err := eventlog.Open(WindowsServiceName)
	if err != nil {
		return err
	}
	defer log.Close()
	return svc.Run(WindowsServiceName, &guestWindowsService{log: log})
}

func (s *guestWindowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	config, err := loadWindowsPairing()
	if err != nil {
		s.log.Error(1, "Guest pairing configuration is unavailable; import a pairing configuration again.")
		return true, 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx, config) }()
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	status <- current
	for {
		select {
		case request, ok := <-requests:
			if !ok {
				cancel()
				return false, 0
			}
			switch request.Cmd {
			case svc.Interrogate:
				status <- current
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 15000}
				cancel()
				select {
				case <-done:
					return false, 0
				case <-time.After(15 * time.Second):
					return true, 3
				}
			}
		case <-done:
			return false, 0
		}
	}
}

func guestRetry(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *guestWindowsService) run(ctx context.Context, config *PairingConfig) {
	key, err := config.Key()
	if err != nil {
		return
	}
	defer clear(key)
	identity := HIDIdentity{VendorID: config.VendorID, ProductID: config.ProductID, Serial: config.Serial}
	s.log.Info(1, "Guest clipboard service started. Clipboard access is request-only and requires the unlocked physical console.")
	for ctx.Err() == nil {
		paths, err := DiscoverHIDReports(identity)
		if err == nil {
			for _, path := range paths {
				if ctx.Err() != nil {
					return
				}
				rawDevice, err := OpenHIDReports(path)
				if err != nil {
					continue
				}
				device := flowControlledOutputReports(rawDevice)
				connectionCtx, cancel := context.WithCancel(ctx)
				stop := context.AfterFunc(connectionCtx, func() { device.Close() })
				handshakeCtx, handshakeCancel := context.WithTimeout(connectionCtx, 5*time.Second)
				channel, err := ClientHandshake(handshakeCtx, &Framer{IO: device}, key)
				handshakeCancel()
				if err == nil {
					s.log.Info(1, "Authenticated USB clipboard channel connected.")
					serveWindowsClipboard(connectionCtx, channel)
				}
				cancel()
				stop()
				device.Close()
			}
		}
		if !guestRetry(ctx) {
			return
		}
	}
}

func serveWindowsClipboard(ctx context.Context, channel *Secure) {
	manager := &workerManager{}
	defer manager.close()
	for ctx.Err() == nil {
		var request Request
		if err := channel.Receive(ctx, &request); err != nil || request.ID == 0 {
			return
		}
		response := manager.dispatch(ctx, request)
		if err := ValidateText(response.Text); err != nil {
			response = Response{ID: request.ID, Error: "invalid clipboard text from worker"}
		}
		if err := channel.Send(ctx, response); err != nil {
			return
		}
	}
}

func installedWindowsGuestPath() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "JetKVMGuest", "jetkvm-guest.exe"), nil
}

func InstallWindowsService() error {
	if err := requireAdministrator(); err != nil {
		return err
	}
	expected, err := installedWindowsGuestPath()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(executable), filepath.Clean(expected)) {
		return errors.New("install the guest executable under Program Files\\JetKVMGuest first")
	}
	if _, err := loadWindowsPairing(); err != nil {
		return errors.New("import the protected pairing configuration before installing the service")
	}
	connection, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer connection.Disconnect()
	service, err := connection.OpenService(WindowsServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		service, err = connection.CreateService(WindowsServiceName, executable, mgr.Config{DisplayName: "JetKVM Guest Clipboard", Description: "Paired USB clipboard integration for the unlocked physical console. No network listener or remote shell.", StartType: mgr.StartAutomatic, ServiceStartName: "LocalSystem"}, "--service")
	}
	if err != nil {
		return err
	}
	defer service.Close()
	if err := service.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 10 * time.Second}, {Type: mgr.ServiceRestart, Delay: 30 * time.Second}}, 86400); err != nil {
		return err
	}
	source, sourceErr := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\EventLog\Application\`+WindowsServiceName, registry.QUERY_VALUE)
	if sourceErr == nil {
		source.Close()
	} else if errors.Is(sourceErr, windows.ERROR_FILE_NOT_FOUND) {
		if err := eventlog.InstallAsEventCreate(WindowsServiceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
			return err
		}
	} else {
		return sourceErr
	}
	state, err := service.Query()
	if err != nil {
		return err
	}
	if state.State == svc.Running {
		if _, err := service.Control(svc.Stop); err != nil {
			return err
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			state, err = service.Query()
			if err != nil {
				return err
			}
			if state.State == svc.Stopped {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if state.State != svc.Stopped {
			return errors.New("guest service did not stop")
		}
	}
	updated := mgr.Config{ServiceType: windows.SERVICE_WIN32_OWN_PROCESS, StartType: mgr.StartAutomatic, ErrorControl: mgr.ErrorNormal, BinaryPathName: windows.ComposeCommandLine([]string{executable, "--service"}), DisplayName: "JetKVM Guest Clipboard", Description: "Paired USB clipboard integration for the unlocked physical console. No network listener or remote shell.", ServiceStartName: "LocalSystem", SidType: windows.SERVICE_SID_TYPE_UNRESTRICTED}
	if err := service.UpdateConfig(updated); err != nil {
		return err
	}
	return service.Start()
}
