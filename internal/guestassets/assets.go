// Package guestassets embeds the companion built into the device firmware.
package guestassets

import (
	"archive/zip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
)

//go:embed files/*
var files embed.FS

func Available() bool {
	f, err := files.Open("files/jetkvm-guest.exe")
	if err == nil {
		f.Close()
	}
	return err == nil
}

// Bundle streams an uncompressed archive to avoid a large allocation or CPU
// spike on the device. Pairing is supplied only by the authenticated route.
func Bundle(w io.Writer, pairing []byte) error {
	exe, err := files.Open("files/jetkvm-guest.exe")
	if err != nil {
		return err
	}
	defer exe.Close()
	sum := sha256.New()
	size, err := io.Copy(sum, exe)
	if err != nil {
		return err
	}
	manifest, err := json.Marshal(map[string]any{"version": 1, "sha256": hex.EncodeToString(sum.Sum(nil)), "bytes": size})
	if err != nil {
		return err
	}
	z := zip.NewWriter(w)
	add := func(name string, data []byte) error {
		f, e := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if e != nil {
			return e
		}
		_, e = f.Write(data)
		return e
	}
	if err = add("manifest.json", manifest); err != nil {
		return err
	}
	if err = add("pairing.json", pairing); err != nil {
		return err
	}
	installer, err := fs.ReadFile(files, "files/install.ps1")
	if err != nil {
		return err
	}
	if err = add("install.ps1", installer); err != nil {
		return err
	}
	exe.Close()
	exe, err = files.Open("files/jetkvm-guest.exe")
	if err != nil {
		return err
	}
	defer exe.Close()
	target, err := z.CreateHeader(&zip.FileHeader{Name: "jetkvm-guest.exe", Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err = io.Copy(target, exe); err != nil {
		return err
	}
	return z.Close()
}
