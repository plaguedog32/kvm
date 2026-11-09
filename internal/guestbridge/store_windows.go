//go:build windows

package guestbridge

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const privateFolderACL = "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
const privateFileACL = "O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)"
const fileAllAccess windows.ACCESS_MASK = 0x001f01ff // FILE_ALL_ACCESS, winnt.h

func requireAdministrator() error {
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	member, err := windows.Token(0).IsMember(admin)
	if err != nil || !member {
		return errors.New("run the guest installer as Administrator")
	}
	return nil
}

func guestConfigDirectory() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "JetKVMGuest"), nil
}

func secureConfigDirectory(path string, create bool) (windows.Handle, error) {
	sd, err := windows.SecurityDescriptorFromString(privateFolderACL)
	if err != nil {
		return 0, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	if create {
		security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
		if err := windows.CreateDirectory(name, &security); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return 0, err
		}
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, err
	}
	fail := func(err error) (windows.Handle, error) { windows.CloseHandle(handle); return 0, err }
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fail(err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fail(errors.New("guest configuration directory must not be a link"))
	}
	existing, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fail(err)
	}
	owner, _, err := existing.Owner()
	if err != nil || owner == nil || (!owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) && !owner.IsWellKnown(windows.WinLocalSystemSid)) {
		return fail(errors.New("guest configuration directory has an unexpected owner"))
	}
	acl, _, err := existing.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		return fail(errors.New("guest configuration directory permissions are not private"))
	}
	seenSystem, seenAdmin := false, false
	for i := uint32(0); i < 2; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != fileAllAccess {
			return fail(errors.New("guest configuration directory permissions are not private"))
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.IsWellKnown(windows.WinLocalSystemSid) {
			seenSystem = true
		} else if sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			seenAdmin = true
		} else {
			return fail(errors.New("guest configuration directory permissions are not private"))
		}
	}
	if !seenSystem || !seenAdmin {
		return fail(errors.New("guest configuration directory permissions are not private"))
	}
	privateACL, _, err := sd.DACL()
	if err != nil {
		return fail(err)
	}
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fail(err)
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, admin, nil, privateACL, nil); err != nil {
		return fail(err)
	}
	return handle, nil
}

func protectPairing(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 || len(data) > 32768 {
		return nil, errors.New("invalid protected pairing data")
	}
	entropy := []byte("JetKVM guest pairing v1")
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	extra := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var output windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&input, nil, &extra, 0, nil, windows.CRYPTPROTECT_LOCAL_MACHINE|windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptUnprotectData(&input, nil, &extra, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	if err != nil {
		return nil, errors.New("Windows could not protect the guest pairing configuration")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	if output.Size == 0 || output.Size > 32768 {
		return nil, errors.New("invalid protected pairing data")
	}
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}

func ImportWindowsPairing(reader io.Reader) error {
	if err := requireAdministrator(); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil {
		return err
	}
	defer clear(data)
	if _, err := DecodePairingConfig(data); err != nil {
		return err
	}
	protected, err := protectPairing(data, true)
	if err != nil {
		return err
	}
	directory, err := guestConfigDirectory()
	if err != nil {
		return err
	}
	dirHandle, err := secureConfigDirectory(directory, true)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(dirHandle)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := filepath.Join(directory, ".pairing-"+hex.EncodeToString(nonce[:]))
	name, _ := windows.UTF16PtrFromString(temp)
	sd, err := windows.SecurityDescriptorFromString(privateFileACL)
	if err != nil {
		return err
	}
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, &security, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.DeleteFile(name)
	contents := append([]byte("JKGP\x01"), protected...)
	var count uint32
	err = windows.WriteFile(handle, contents, &count, nil)
	if err == nil && count != uint32(len(contents)) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = windows.FlushFileBuffers(handle)
	}
	closeErr := windows.CloseHandle(handle)
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	target, _ := windows.UTF16PtrFromString(filepath.Join(directory, "pairing.dat"))
	return windows.MoveFileEx(name, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func loadWindowsPairing() (*PairingConfig, error) {
	directory, err := guestConfigDirectory()
	if err != nil {
		return nil, err
	}
	handle, err := secureConfigDirectory(directory, false)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(handle)
	file, err := os.Open(filepath.Join(directory, "pairing.dat"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 32769))
	if err != nil || len(data) > 32768 || !bytes.HasPrefix(data, []byte("JKGP\x01")) {
		return nil, errors.New("invalid protected pairing configuration")
	}
	plain, err := protectPairing(data[5:], false)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return DecodePairingConfig(plain)
}
