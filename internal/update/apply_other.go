//go:build !windows

package update

import (
	"fmt"
	"os"
)

// Supported reports whether this build can apply an update at all.
//
// False everywhere but Windows: releases ship a Windows installer and nothing
// else. The checker still runs — being able to exercise the whole state machine
// up to "ready" on a development machine is what keeps this testable off
// Windows — but the last step refuses.
const Supported = false

// ensureStagingDir creates the staging directory.
//
// 0o700 rather than the 0o755 the rest of the data directory uses: on Windows
// this directory gets an explicit ACL because LocalSystem executes what is in
// it, and the non-Windows path should not be the lax one just because nothing
// ships here.
func ensureStagingDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

func launchInstaller(exe, logFile string) error {
	return fmt.Errorf("updates can only be applied on Windows")
}
