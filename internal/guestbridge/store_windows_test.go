//go:build windows

package guestbridge

import (
	"bytes"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsMachineProtection(t *testing.T) {
	plain := []byte("test-only pairing material")
	sealed, err := protectPairing(plain, true)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("DPAPI data contains plaintext")
	}
	opened, err := protectPairing(sealed, false)
	if err != nil || !bytes.Equal(opened, plain) {
		t.Fatal("DPAPI roundtrip", err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := protectPairing(sealed, false); err == nil {
		t.Fatal("corrupt DPAPI data accepted")
	}
}

func TestWindowsPrivateDirectoryRejectsUnsafeOwnerAndACL(t *testing.T) {
	if requireAdministrator() != nil {
		t.Skip("requires an elevated Windows test process")
	}
	root := t.TempDir()
	private := filepath.Join(root, "private")
	handle, err := secureConfigDirectory(private, true)
	if err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(handle)
	handle, err = secureConfigDirectory(private, false)
	if err != nil {
		t.Fatal("cannot reopen secured directory", err)
	}
	windows.CloseHandle(handle)
	public := filepath.Join(root, "unsafe")
	sd, err := windows.SecurityDescriptorFromString("O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, _ := windows.UTF16PtrFromString(public)
	if err := windows.CreateDirectory(name, &sa); err != nil {
		t.Fatal(err)
	}
	if handle, err := secureConfigDirectory(public, false); err == nil {
		windows.CloseHandle(handle)
		t.Fatal("unsafe existing ACL accepted")
	}
}
