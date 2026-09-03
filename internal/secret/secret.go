// Package secret seals and opens small secrets at rest — in practice, the SMTP
// password.
//
// On Windows this is DPAPI at machine scope, so the ciphertext is bound to the
// machine and no key material is stored alongside it. Elsewhere it is AES-GCM
// under a key file that only the owner can read, which is what keeps the dev
// loop from writing plaintext credentials into a database.
//
// The plan schedules this for Phase 4, but it lands here in Phase 1: the
// notifier needs a password to send mail, and there is no acceptable interim
// state where that password sits in the database as plaintext.
package secret

import (
	"encoding/base64"
	"errors"
	"strings"
)

// ErrNoSecret is returned when the stored value is empty.
var ErrNoSecret = errors.New("no secret stored")

// prefix tags the sealing scheme, so a database moved between platforms fails
// loudly instead of decrypting to garbage.
const (
	prefixDPAPI = "dpapi:"
	prefixAES   = "aesgcm:"
)

// Seal encrypts plaintext for storage and returns an opaque, prefixed string.
func Seal(keyPath string, plaintext []byte) (string, error) {
	return seal(keyPath, plaintext)
}

// Open decrypts a value produced by Seal.
func Open(keyPath, stored string) ([]byte, error) {
	if strings.TrimSpace(stored) == "" {
		return nil, ErrNoSecret
	}
	return openSealed(keyPath, stored)
}

// Scheme reports which sealing scheme produced a stored value, for diagnostics.
func Scheme(stored string) string {
	switch {
	case strings.HasPrefix(stored, prefixDPAPI):
		return "DPAPI (machine scope)"
	case strings.HasPrefix(stored, prefixAES):
		return "AES-GCM (key file)"
	default:
		return "unknown"
	}
}

func encode(prefix string, b []byte) string {
	return prefix + base64.StdEncoding.EncodeToString(b)
}

func decode(prefix, stored string) ([]byte, error) {
	if !strings.HasPrefix(stored, prefix) {
		return nil, errors.New("secret was sealed by a different scheme (" + Scheme(stored) + ")")
	}
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, prefix))
}
