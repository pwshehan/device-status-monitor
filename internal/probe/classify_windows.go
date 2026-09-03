//go:build windows

package probe

import (
	"errors"

	"golang.org/x/sys/windows"
)

// platformClass maps Winsock's error numbers onto probe classes.
//
// The constants come from golang.org/x/sys/windows, not syscall: Go's own
// syscall package does not define the WSAE* numbers.
//
// Winsock does not reuse the POSIX errno values, so
// errors.Is(err, syscall.ECONNREFUSED) is false on Windows even for a plainly
// refused connection — every such probe was landing in ClassOther, which is
// the one platform where that matters, since it is the platform this ships on.
// The generic classification in probe.go is still right everywhere else, so
// this is a per-platform addition rather than a rewrite.
//
// Found by running the suite on Windows for the first time; CI runs on Linux,
// where the POSIX branch is correct and the test passes.
func platformClass(err error) (Class, bool) {
	switch {
	case errors.Is(err, windows.WSAECONNREFUSED):
		return ClassRefused, true
	case errors.Is(err, windows.WSAETIMEDOUT):
		return ClassTimeout, true
	case errors.Is(err, windows.WSAEHOSTUNREACH), errors.Is(err, windows.WSAENETUNREACH):
		return ClassUnreachable, true
	case errors.Is(err, windows.WSAHOST_NOT_FOUND), errors.Is(err, windows.WSATRY_AGAIN):
		return ClassDNS, true
	}
	return "", false
}
