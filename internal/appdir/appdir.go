// Package appdir resolves where the service keeps its data.
package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// AppName is the folder name used under ProgramData.
const AppName = "LocalMonitor"

// Dirs is the resolved set of data locations.
//
// Binaries live in Program Files and data lives in ProgramData, so an upgrade
// replaces executables without touching the database, and the service (as
// LocalSystem) and the GUI (as the logged-in user) share one data path.
type Dirs struct {
	Root string
	Dev  bool
}

// Resolve picks the data root. In dev mode that is ./.dev-data next to the
// working directory, which is what makes the whole engine runnable on macOS.
func Resolve(dev bool) (Dirs, error) {
	if dev {
		wd, err := os.Getwd()
		if err != nil {
			return Dirs{}, err
		}
		return Dirs{Root: filepath.Join(wd, ".dev-data"), Dev: true}, nil
	}

	if runtime.GOOS == "windows" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			return Dirs{}, fmt.Errorf("ProgramData is not set")
		}
		return Dirs{Root: filepath.Join(programData, AppName)}, nil
	}

	// Non-Windows service mode is not a shipping configuration, but keeping it
	// working means the same code path can be exercised locally.
	base, err := os.UserConfigDir()
	if err != nil {
		return Dirs{}, err
	}
	return Dirs{Root: filepath.Join(base, AppName)}, nil
}

// DB is the SQLite file path.
func (d Dirs) DB() string { return filepath.Join(d.Root, "monitor.db") }

// TokenFile is where the local API's bearer token is stored.
func (d Dirs) TokenFile() string { return filepath.Join(d.Root, "api.token") }

// UpdateDir is where a downloaded installer is staged before it is run.
//
// It is created separately from the directories below, with a restrictive ACL:
// the service runs as LocalSystem, and a directory it executes from must not be
// one a standard user can write to. See internal/update.
func (d Dirs) UpdateDir() string { return filepath.Join(d.Root, "updates") }

// LogDir is the rotating log directory.
func (d Dirs) LogDir() string { return filepath.Join(d.Root, "logs") }

// LogFile is the current log file.
func (d Dirs) LogFile() string { return filepath.Join(d.LogDir(), "monitor.log") }

// Ensure creates the data and log directories.
func (d Dirs) Ensure() error {
	for _, dir := range []string{d.Root, d.LogDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}
