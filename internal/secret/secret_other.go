//go:build !windows

package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const keyLen = 32

// loadOrCreateKey reads the AES key from keyPath, creating it on first use.
// The file is 0600: the guarantee here is filesystem permissions, which is
// weaker than DPAPI but strictly better than plaintext in the database.
func loadOrCreateKey(keyPath string) ([]byte, error) {
	if keyPath == "" {
		return nil, errors.New("no key path configured")
	}
	key, err := os.ReadFile(keyPath)
	switch {
	case err == nil:
		if len(key) != keyLen {
			return nil, fmt.Errorf("key file %s is %d bytes, want %d", keyPath, len(key), keyLen)
		}
		return key, nil
	case !os.IsNotExist(err):
		return nil, err
	}

	key = make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func aead(keyPath string) (cipher.AEAD, error) {
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(keyPath string, plaintext []byte) (string, error) {
	gcm, err := aead(keyPath)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return encode(prefixAES, gcm.Seal(nonce, nonce, plaintext, nil)), nil
}

func openSealed(keyPath, stored string) ([]byte, error) {
	raw, err := decode(prefixAES, stored)
	if err != nil {
		return nil, err
	}
	gcm, err := aead(keyPath)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("sealed value is truncated")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}
