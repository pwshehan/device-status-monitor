// Command monitor-service is the monitoring engine: a Windows Service in
// production, a foreground process in development.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/api"
	"github.com/gkgraphite/device-status-monitor/internal/appdir"
	"github.com/gkgraphite/device-status-monitor/internal/core"
	"github.com/gkgraphite/device-status-monitor/internal/logx"
	"github.com/gkgraphite/device-status-monitor/internal/store"
	"github.com/gkgraphite/device-status-monitor/internal/svcrun"
)

// version is stamped at build time: -ldflags "-X main.version=1.0.0".
var version = "dev"

const usage = `monitor-service — Local Device Monitor engine

Usage:
  monitor-service [flags]              run (as a service when the SCM starts it)
  monitor-service install              register the Windows service
  monitor-service uninstall            stop and remove the Windows service
  monitor-service start | stop         control the installed service
  monitor-service status               report the service state
  monitor-service rotate-token         issue a new local API token
  monitor-service version              print the version

Flags:
  -dev            run in the foreground against ./.dev-data
  -seed           create example groups and devices if the database is empty
  -log-level      debug | info | warn | error (default info)
  -api-addr       loopback address for the local API (default 127.0.0.1:49215)
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	dev := flag.Bool("dev", false, "run in the foreground against ./.dev-data")
	seed := flag.Bool("seed", false, "create example groups and devices if the database is empty")
	level := flag.String("log-level", "info", "debug | info | warn | error")
	apiAddr := flag.String("api-addr", api.DefaultAddr,
		"loopback address for the local API; empty disables it")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	// Subcommands come after the flags so `-dev` and `install` cannot be
	// confused for one another.
	switch cmd := strings.ToLower(flag.Arg(0)); cmd {
	case "":
		// fall through to running the engine
	case "version":
		fmt.Println(version)
		return nil
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := svcrun.Install(exe); err != nil {
			return err
		}
		fmt.Println("service installed")
		return nil
	case "uninstall":
		if err := svcrun.Uninstall(); err != nil {
			return err
		}
		fmt.Println("service removed")
		return nil
	case "start":
		if err := svcrun.Start(); err != nil {
			return err
		}
		fmt.Println("service started")
		return nil
	case "stop":
		if err := svcrun.Stop(); err != nil {
			return err
		}
		fmt.Println("service stopped")
		return nil
	case "status":
		st, err := svcrun.Status()
		if err != nil {
			return err
		}
		fmt.Println(st)
		return nil
	case "rotate-token":
		dirs, err := appdir.Resolve(*dev)
		if err != nil {
			return fmt.Errorf("resolve data directory: %w", err)
		}
		if err := dirs.Ensure(); err != nil {
			return err
		}
		if _, err := api.RotateToken(dirs.TokenFile()); err != nil {
			return err
		}
		// The token itself is not printed: it would land in shell history and
		// in whatever captured the installer's output. The GUI reads the file.
		fmt.Println("new API token written to", dirs.TokenFile())
		fmt.Println("restart the service for it to take effect")
		return nil
	default:
		flag.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}

	dirs, err := appdir.Resolve(*dev)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	if err := dirs.Ensure(); err != nil {
		return err
	}

	asService := svcrun.IsService()
	logOpts := logx.Options{
		File:    dirs.LogFile(),
		Console: !asService,
		Level:   *level,
	}
	if asService {
		// Only under the SCM: an administrator looking for why monitoring
		// stopped opens Event Viewer, not a folder under ProgramData.
		logOpts.EventLogSource = svcrun.ServiceName
	}
	log, closer := logx.Setup(logOpts)
	if closer != nil {
		defer closer.Close()
	}
	log.Info("monitor-service starting",
		"version", version, "mode", modeName(asService, *dev), "data", dirs.Root)

	if *seed {
		n, err := seedDatabase(dirs)
		if err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		if n > 0 {
			log.Info("seeded example data", "devices", n)
		} else {
			log.Info("database already has devices, nothing seeded")
		}
	}

	opts := core.Options{Dirs: dirs, Log: log, Version: version, APIAddr: *apiAddr}
	if asService {
		return svcrun.RunService(opts)
	}
	return svcrun.Run(opts)
}

// seedDatabase opens the store on its own so seeding happens before the engine
// starts and the scheduler sees the new devices on its first load.
func seedDatabase(dirs appdir.Dirs) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Open(ctx, dirs.DB())
	if err != nil {
		return 0, err
	}
	defer st.Close()

	if err := st.SeedSettings(ctx); err != nil {
		return 0, err
	}
	return core.Seed(ctx, st)
}

func modeName(asService, dev bool) string {
	switch {
	case asService:
		return "windows-service"
	case dev:
		return "dev"
	default:
		return "console"
	}
}
