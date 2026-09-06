// Package api serves the local HTTP API the desktop app drives: JSON handlers,
// the auth and origin middleware that make a loopback port defensible, and the
// SSE hub that pushes live status.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/probe"
	"github.com/pwshehan/device-status-monitor/internal/store"
	"github.com/pwshehan/device-status-monitor/internal/update"
)

// DefaultAddr is the loopback address the service listens on. Never 0.0.0.0:
// this API is a local control channel, not a network service.
const DefaultAddr = "127.0.0.1:49215"

// Engine is what the handlers need from the running monitor.
//
// An interface rather than *core.App so the dependency points one way — core
// wires the API up, the API knows nothing about core — and so the handlers can
// be tested against a stub with no scheduler, no database writer and no clock.
type Engine interface {
	// Reload asks the engine to recompute the effective set now. Every write
	// that changes what or how often something is probed calls it.
	Reload()

	// CheckNow probes one device immediately, outside its schedule, without
	// touching its state or history. False means the engine is not running it.
	CheckNow(ctx context.Context, deviceID int64) (probe.Result, bool)

	// Uptime is how long the engine has been running.
	Uptime() time.Duration

	// Running is how many device workers are active.
	Running() int

	// SchedulerLagMS is how far behind the most overdue device's probe is.
	SchedulerLagMS() int64

	// JanitorStatus reports the last maintenance pass. Retention that has
	// quietly stopped running is invisible until the disk fills, so it is
	// worth a line on the health endpoint.
	JanitorStatus() JanitorStatus

	// SendTestEmail delivers immediately, bypassing the outbox, and returns
	// the SMTP error verbatim.
	SendTestEmail(ctx context.Context, to []string) error

	// SaveSMTPPassword seals a new password. The plaintext never reaches the
	// database, and the API never returns it.
	SaveSMTPPassword(ctx context.Context, plaintext string) error

	// UpdateStatus is what the last check found and what has been staged.
	UpdateStatus(ctx context.Context) update.Status

	// CheckUpdate asks GitHub now instead of waiting for the next pass.
	CheckUpdate(ctx context.Context) update.Status

	// DownloadUpdate stages the installer for the newest known release. Only
	// needed when automatic downloads are off.
	DownloadUpdate(ctx context.Context) error

	// InstallUpdate runs the staged installer and returns immediately. It
	// returns before the installer starts, because the installer stops this
	// service and nothing waiting past that point would ever be answered.
	InstallUpdate(ctx context.Context) error
}

// JanitorStatus is the outcome of the last maintenance pass.
type JanitorStatus struct {
	At               time.Time
	RolledUp         int
	HeartbeatsPruned int64
	RollupsPruned    int64
	Vacuumed         bool
	Err              string
}

// Config is everything the server needs that is not behaviour.
type Config struct {
	Addr    string // default DefaultAddr
	Token   string // bearer token; empty makes every authenticated route 503
	Version string
	DataDir string
	LogDir  string
}

// Server is the HTTP API.
type Server struct {
	st    *store.Store
	eng   Engine
	hub   *Hub
	log   *slog.Logger
	token string
	cfg   Config

	srv  *http.Server
	ln   net.Listener
	done chan struct{}
}

// New builds a server. It does not listen; call Start.
func New(st *store.Store, eng Engine, hub *Hub, cfg Config, log *slog.Logger) *Server {
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if log == nil {
		log = slog.Default()
	}
	if hub == nil {
		hub = NewHub()
	}
	return &Server{
		st: st, eng: eng, hub: hub, log: log,
		token: cfg.Token, cfg: cfg, done: make(chan struct{}),
	}
}

// Hub returns the event hub, so the engine can publish to the same one the SSE
// handler reads.
func (s *Server) Hub() *Hub { return s.hub }

// Handler returns the routed, middleware-wrapped handler. Exported for tests,
// which drive it through httptest rather than a real listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Method-and-path patterns need Go 1.22+; a 405 for a known path with the
	// wrong method comes free with them.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/summary", s.handleSummary)

	mux.HandleFunc("GET /api/devices", s.handleListDevices)
	mux.HandleFunc("POST /api/devices", s.handleCreateDevice)
	mux.HandleFunc("POST /api/devices/bulk", s.handleBulkDevices)
	mux.HandleFunc("GET /api/devices/{id}", s.handleGetDevice)
	mux.HandleFunc("PATCH /api/devices/{id}", s.handlePatchDevice)
	mux.HandleFunc("DELETE /api/devices/{id}", s.handleDeleteDevice)
	mux.HandleFunc("POST /api/devices/{id}/pause", s.handlePauseDevice)
	mux.HandleFunc("POST /api/devices/{id}/check", s.handleCheckDevice)
	mux.HandleFunc("GET /api/devices/{id}/heartbeats", s.handleDeviceHeartbeats)
	mux.HandleFunc("GET /api/devices/{id}/uptime", s.handleDeviceUptime)
	mux.HandleFunc("GET /api/devices/{id}/incidents", s.handleDeviceIncidents)

	mux.HandleFunc("GET /api/groups", s.handleListGroups)
	mux.HandleFunc("POST /api/groups", s.handleCreateGroup)
	mux.HandleFunc("POST /api/groups/reorder", s.handleReorderGroups)
	mux.HandleFunc("GET /api/groups/{id}", s.handleGetGroup)
	mux.HandleFunc("PATCH /api/groups/{id}", s.handlePatchGroup)
	mux.HandleFunc("DELETE /api/groups/{id}", s.handleDeleteGroup)
	mux.HandleFunc("POST /api/groups/{id}/pause", s.handlePauseGroup)
	mux.HandleFunc("GET /api/groups/{id}/uptime", s.handleGroupUptime)

	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("POST /api/settings/test-email", s.handleTestEmail)

	mux.HandleFunc("GET /api/update", s.handleUpdate)
	mux.HandleFunc("POST /api/update/check", s.handleUpdateCheck)
	mux.HandleFunc("POST /api/update/download", s.handleUpdateDownload)
	mux.HandleFunc("POST /api/update/install", s.handleUpdateInstall)

	mux.HandleFunc("GET /api/events", s.handleEvents)

	// No catch-all route: an unknown path and a known path with the wrong
	// method are different answers (404 vs. 405 plus Allow), and registering
	// "/" here would collapse them into the first. jsonProblems is what puts
	// net/http's plain-text version of both into the API's error shape.
	return s.chain(jsonProblems(mux))
}

// Start binds the listener and serves in the background.
//
// Binding is synchronous on purpose: "address already in use" means another
// copy of the service is running, and that has to be an error the caller can
// report, not a log line nobody reads.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: /api/events is a long-lived stream and any write
		// deadline would cut it off mid-flight.
		IdleTimeout: 120 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
		// net/http's default error log goes to stderr, which a Windows
		// service does not have. Route it through the same handler as
		// everything else.
		ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}

	go func() {
		defer close(s.done)
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("api server stopped", "err", err)
		}
	}()

	s.log.Info("api listening", "addr", ln.Addr().String())
	return nil
}

// Addr reports the bound address, which is how a test finds the port.
func (s *Server) Addr() string {
	if s.ln == nil {
		return s.cfg.Addr
	}
	return s.ln.Addr().String()
}

// Shutdown stops accepting, then waits out in-flight requests up to timeout.
func (s *Server) Shutdown(timeout time.Duration) error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := s.srv.Shutdown(ctx)
	select {
	case <-s.done:
	case <-ctx.Done():
	}
	return err
}
