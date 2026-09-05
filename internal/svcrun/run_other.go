//go:build !windows

// Package svcrun adapts the engine to its host: a Windows Service on Windows,
// a foreground process everywhere else. Keeping the difference to one file per
// platform is what lets the whole engine be developed and tested on macOS.
package svcrun

import (
	"context"
	"errors"
	"os/signal"
	"syscall"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/core"
)

// ServiceName matches the Windows build so callers can name the service — for
// an Event Log source, a log line, or an error message — without a build tag of
// their own. Nothing off Windows acts on it.
const ServiceName = "LocalMonitorSvc"

// ErrNotSupported is returned by the service-management commands off Windows.
var ErrNotSupported = errors.New("service management is only available on Windows")

// IsService reports whether the process was started by the service manager.
// Never true off Windows.
func IsService() bool { return false }

// Run starts the engine and blocks until interrupted.
func Run(o core.Options) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := core.Start(ctx, o)
	if err != nil {
		return err
	}

	<-ctx.Done()
	o.Log.Info("shutdown signal received")
	return app.Shutdown(ShutdownTimeout)
}

// RunService is a Windows-only entry point.
func RunService(core.Options) error { return ErrNotSupported }

// Install registers the service.
func Install(string) error { return ErrNotSupported }

// Uninstall removes the service.
func Uninstall() error { return ErrNotSupported }

// Start starts the installed service.
func Start() error { return ErrNotSupported }

// Stop stops the installed service.
func Stop() error { return ErrNotSupported }

// Status reports the installed service's state.
func Status() (string, error) { return "", ErrNotSupported }

// ShutdownTimeout bounds how long a stop waits for workers to finish.
const ShutdownTimeout = 10 * time.Second
