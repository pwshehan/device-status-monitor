package core

import (
	"context"

	"github.com/pwshehan/device-status-monitor/internal/update"
)

// The engine's half of the update surface. Thin delegations to the checker,
// kept here rather than in core.go so that wiring one more background worker
// does not grow the file that already wires all of them.
//
// A nil Updater is a running configuration, not a bug: the engine tests start
// an App with no API and no checker, and a nil check here is cheaper than
// making every one of them construct one.

// UpdateStatus is what the last check found and what has been staged.
func (a *App) UpdateStatus() update.Status {
	if a.Updater == nil {
		return update.Status{}
	}
	return a.Updater.Status()
}

// CheckUpdate asks GitHub now instead of waiting for the next pass.
func (a *App) CheckUpdate(ctx context.Context) update.Status {
	if a.Updater == nil {
		return update.Status{}
	}
	return a.Updater.Once(ctx)
}

// DownloadUpdate stages the installer for the newest known release.
func (a *App) DownloadUpdate(ctx context.Context) error {
	if a.Updater == nil {
		return update.ErrNotReady
	}
	return a.Updater.Download(ctx)
}

// InstallUpdate runs the staged installer. It returns before the installer
// starts; see update.Checker.Install for why.
func (a *App) InstallUpdate(ctx context.Context) error {
	if a.Updater == nil {
		return update.ErrNotReady
	}
	return a.Updater.Install(ctx)
}
