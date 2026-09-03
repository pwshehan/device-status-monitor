//go:build windows

package secret

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	crypt32         = windows.NewLazySystemDLL("crypt32.dll")
	kernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procProtectData = crypt32.NewProc("CryptProtectData")
	procUnprotect   = crypt32.NewProc("CryptUnprotectData")
	procLocalFree   = kernel32.NewProc("LocalFree")
)

// cryptProtectLocalMachine seals to the machine rather than to a user profile,
// because the service runs as LocalSystem and must read what it wrote.
const cryptProtectLocalMachine = 0x4

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b dataBlob) bytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

func (b dataBlob) free() {
	if b.pbData != nil {
		_, _, _ = procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
	}
}

func seal(_ string, plaintext []byte) (string, error) {
	in := newBlob(plaintext)
	var out dataBlob

	r, _, err := procProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		uintptr(cryptProtectLocalMachine),
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return "", fmt.Errorf("CryptProtectData: %w", err)
	}
	defer out.free()
	return encode(prefixDPAPI, out.bytes()), nil
}

func openSealed(_ string, stored string) ([]byte, error) {
	raw, err := decode(prefixDPAPI, stored)
	if err != nil {
		return nil, err
	}
	in := newBlob(raw)
	var out dataBlob

	r, _, callErr := procUnprotect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		uintptr(cryptProtectLocalMachine),
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %w", callErr)
	}
	defer out.free()
	return out.bytes(), nil
}
