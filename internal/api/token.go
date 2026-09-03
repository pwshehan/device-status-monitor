package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TokenBytes is the entropy behind the bearer token.
const TokenBytes = 32

// LoadOrCreateToken reads the token file, generating one on first start.
//
// The file is how the GUI, running as the logged-in user, authenticates to the
// service, running as LocalSystem: a shared secret in a shared location beats
// any scheme where the two have to agree on a password. It is created 0600,
// which on Windows means it inherits the ProgramData ACL — SYSTEM and
// Administrators full, Users read. That is deliberate and it is also the
// honest limitation of this design: any local user can read the token and
// therefore reconfigure monitoring. The SMTP password stays sealed either way
// (internal/secret), and the hardened alternative — a named pipe with an
// explicit DACL — is documented in the plan as a follow-up.
//
// Setting an explicit DACL rather than relying on inheritance is Phase 5 work,
// when there is a Windows host to verify it on; relying on inheritance here is
// a stated compromise, not an oversight.
func LoadOrCreateToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
		// An empty file is worse than a missing one: it would authenticate
		// nothing and every request would 401 with no explanation.
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return RotateToken(path)
}

// RotateToken generates a new token and replaces the file atomically, so a GUI
// reading it concurrently sees either the old token or the new one, never a
// half-written line.
func RotateToken(path string) (string, error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(buf)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create token directory: %w", err)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("replace %s: %w", path, err)
	}
	return token, nil
}
