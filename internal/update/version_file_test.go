package update

import (
	"os"
	"strings"
	"testing"
)

// TestVersionFileIsAVersionThisCanRead is the local half of the release gate.
//
// The release workflow refuses to build a tag whose version this parser cannot
// read, but that check only runs once a tag exists. Running the same assertion
// here means a bad VERSION file fails CI on the pull request that introduced
// it, using the very parser that will compare it against GitHub later.
func TestVersionFileIsAVersionThisCanRead(t *testing.T) {
	raw, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}

	// TrimSpace, not a CRLF-specific trim: what matters is that the value the
	// Makefile stamps and the value parsed here are the same one.
	s := strings.TrimSpace(string(raw))
	if s == "" {
		t.Fatal("VERSION is empty")
	}
	if s != string(raw) && strings.TrimRight(string(raw), "\r\n") != s {
		t.Errorf("VERSION has surrounding whitespace beyond a trailing newline: %q", string(raw))
	}
	if strings.HasPrefix(s, "v") {
		t.Errorf("VERSION = %q; it holds the version, and the tag adds the v", s)
	}

	v, err := ParseVersion(s)
	if err != nil {
		t.Fatalf("VERSION = %q: %v", s, err)
	}
	if v.String() != s {
		t.Errorf("VERSION = %q but parses as %q; the release asset names would not match", s, v)
	}
}
