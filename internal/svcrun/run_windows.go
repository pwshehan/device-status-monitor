//go:build windows

// Package svcrun adapts the engine to its host: a Windows Service on Windows,
// a foreground process everywhere else.
package svcrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/pwshehan/local-device-monitor/internal/core"
)

// ServiceName and ServiceDisplay identify the service to the SCM.
const (
	ServiceName    = "LocalMonitorSvc"
	ServiceDisplay = "Local Device Monitor"
	ServiceDesc    = "Monitors TCP endpoints, records uptime history and sends email alerts."
)

// ShutdownTimeout bounds how long a stop waits for workers to finish.
const ShutdownTimeout = 10 * time.Second

// ErrNotSupported is never returned on Windows; it exists to match the
// non-Windows build.
var ErrNotSupported = errors.New("not supported")

// IsService reports whether the process was started by the service manager.
func IsService() bool {
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// Run starts the engine in the foreground, for console and dev use.
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

type handler struct{ opts core.Options }

// Execute is the SCM callback.
func (h handler) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	app, err := core.Start(ctx, h.opts)
	if err != nil {
		// The log file may not exist yet, and a service has no console, so the
		// Event Log is the only place this can be seen. Exiting non-zero is
		// what stops the SCM reporting a broken service as running.
		logStartupFailure(err)
		cancel()
		changes <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
		return true, 1
	}

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for req := range r {
		switch req.Cmd {
		case svc.Interrogate:
			changes <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			cancel()
			// Waiting here is what keeps the last heartbeat batch and any
			// open incident from being lost on every service stop.
			if err := app.Shutdown(ShutdownTimeout); err != nil {
				h.opts.Log.Error("shutdown", "err", err)
			}
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		default:
			h.opts.Log.Warn("unexpected service control request", "cmd", req.Cmd)
		}
	}
	cancel()
	return false, 0
}

// RunService hands control to the SCM.
func RunService(o core.Options) error {
	return svc.Run(ServiceName, handler{opts: o})
}

func logStartupFailure(err error) {
	el, elErr := eventlog.Open(ServiceName)
	if elErr != nil {
		return
	}
	defer el.Close()
	_ = el.Error(1, fmt.Sprintf("%s failed to start: %v", ServiceDisplay, err))
}

// Install registers the service with the SCM.
//
// Done here rather than with sc.exe from the installer: binPath quoting is a
// classic source of installers that appear to succeed and leave a service that
// cannot start, and sc.exe cannot set failure actions in one step.
func Install(exePath string) error {
	if exePath == "" {
		p, err := os.Executable()
		if err != nil {
			return err
		}
		exePath = p
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (run as administrator): %w", err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(ServiceName); err == nil {
		s.Close()
		return fmt.Errorf("service %s is already installed", ServiceName)
	}

	s, err := m.CreateService(ServiceName, exePath, mgr.Config{
		DisplayName:  ServiceDisplay,
		Description:  ServiceDesc,
		StartType:    mgr.StartAutomatic,
		ServiceType:  windowsServiceTypeOwnProcess,
		ErrorControl: mgr.ErrorNormal,
	})
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	// Restart on crash rather than staying dead until someone notices the
	// monitoring stopped.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}

	// Registering the Event Log source is what lets the service explain a
	// failed start somewhere an administrator will look. Not fatal if it
	// fails — the service still runs and still writes its log file — but the
	// caller is told, because silently having no Event Log entries is exactly
	// the sort of thing nobody discovers until they need one.
	if err := eventlog.InstallAsEventCreate(ServiceName,
		eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!errors.Is(err, os.ErrExist) {
		return fmt.Errorf("service installed, but registering its Event Log source failed: %w", err)
	}
	return nil
}

const windowsServiceTypeOwnProcess = 0x10

// Uninstall stops and removes the service.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (run as administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("service %s is not installed", ServiceName)
	}
	defer s.Close()

	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop service: %w", err)
		}
		_ = waitFor(s, svc.Stopped, 30*time.Second)
	}
	_ = eventlog.Remove(ServiceName)
	return s.Delete()
}

// Start starts the installed service.
func Start() error {
	s, m, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return waitFor(s, svc.Running, 30*time.Second)
}

// Stop stops the installed service.
func Stop() error {
	s, m, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()

	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	return waitFor(s, svc.Stopped, 30*time.Second)
}

// Status reports the installed service's state.
func Status() (string, error) {
	s, m, err := openService()
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return "", err
	}
	return stateName(st.State), nil
}

func openService() (*mgr.Service, *mgr.Mgr, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, fmt.Errorf("connect to service manager: %w", err)
	}
	s, err := m.OpenService(ServiceName)
	if err != nil {
		m.Disconnect()
		return nil, nil, fmt.Errorf("service %s is not installed", ServiceName)
	}
	return s, m, nil
}

func waitFor(s *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for the service to reach %s", stateName(want))
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "resuming"
	case svc.PausePending:
		return "pausing"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("state %d", s)
	}
}
