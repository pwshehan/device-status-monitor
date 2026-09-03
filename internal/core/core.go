// Package core wires the engine together: store, scheduler, evaluator, writer
// and notifier, started and stopped as one unit.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/appdir"
	"github.com/gkgraphite/device-status-monitor/internal/model"
	"github.com/gkgraphite/device-status-monitor/internal/notify"
	"github.com/gkgraphite/device-status-monitor/internal/probe"
	"github.com/gkgraphite/device-status-monitor/internal/scheduler"
	"github.com/gkgraphite/device-status-monitor/internal/state"
	"github.com/gkgraphite/device-status-monitor/internal/store"
)

// Options configures a run.
type Options struct {
	Dirs          appdir.Dirs
	Log           *slog.Logger
	MaxConcurrent int

	// RefreshInterval is how often the effective set is recomputed from the
	// database. A device or group edit reloads immediately; this loop is what
	// makes a pause expire without anything having to wake on its own timer.
	RefreshInterval time.Duration

	// FlushInterval and FlushSize bound the heartbeat write batch.
	FlushInterval time.Duration
	FlushSize     int

	// NotifierInterval is how often the outbox is drained.
	NotifierInterval time.Duration

	// Prober and Sender are injectable for tests.
	Prober probe.Prober
	Sender notify.Sender
}

func (o *Options) setDefaults() {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = scheduler.DefaultMaxConcurrent
	}
	if o.RefreshInterval <= 0 {
		o.RefreshInterval = 15 * time.Second
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = 2 * time.Second
	}
	if o.FlushSize <= 0 {
		o.FlushSize = 200
	}
	if o.Prober == nil {
		o.Prober = probe.TCP{}
	}
	if o.Sender == nil {
		o.Sender = notify.SMTP{}
	}
}

// App is a running engine.
type App struct {
	Store     *store.Store
	Scheduler *scheduler.Scheduler
	Notifier  *notify.Worker

	opts      Options
	log       *slog.Logger
	results   chan probe.Result
	heartbeat chan model.Heartbeat
	reload    chan struct{}

	wg      sync.WaitGroup
	cancel  context.CancelFunc
	started time.Time

	mu    sync.Mutex
	snaps map[int64]*state.Snapshot
	effs  map[int64]model.Effective
}

// Start opens the database, runs migrations and launches every goroutine.
//
// A startup failure returns an error rather than a half-running App: the
// service wrapper reports it to the Event Log and exits non-zero, so the SCM
// never claims a broken service is running.
func Start(parent context.Context, o Options) (*App, error) {
	o.setDefaults()

	if err := o.Dirs.Ensure(); err != nil {
		return nil, fmt.Errorf("prepare data directory: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)

	st, err := store.Open(ctx, o.Dirs.DB())
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := st.SeedSettings(ctx); err != nil {
		cancel()
		_ = st.Close()
		return nil, fmt.Errorf("seed settings: %w", err)
	}

	snaps, err := st.LoadSnapshots(ctx)
	if err != nil {
		cancel()
		_ = st.Close()
		return nil, fmt.Errorf("load device state: %w", err)
	}

	a := &App{
		Store:     st,
		opts:      o,
		log:       o.Log,
		results:   make(chan probe.Result, 512),
		heartbeat: make(chan model.Heartbeat, 1024),
		reload:    make(chan struct{}, 1),
		cancel:    cancel,
		started:   time.Now(),
		snaps:     snaps,
		effs:      map[int64]model.Effective{},
	}
	a.Scheduler = scheduler.New(o.Prober, a.results, o.MaxConcurrent, o.Log)
	a.Notifier = &notify.Worker{
		Store:    st,
		Sender:   o.Sender,
		KeyPath:  filepath.Join(o.Dirs.Root, "secret.key"),
		Interval: o.NotifierInterval,
		Log:      o.Log,
	}

	if err := a.refresh(ctx); err != nil {
		cancel()
		_ = st.Close()
		return nil, fmt.Errorf("load devices: %w", err)
	}

	a.spawn(func() { a.runEvaluator(ctx) })
	a.spawn(func() { a.runWriter(ctx) })
	a.spawn(func() { a.runRefresh(ctx) })
	a.spawn(func() { a.Notifier.Run(ctx) })

	o.Log.Info("engine started",
		"db", o.Dirs.DB(), "devices", a.Scheduler.Running(), "dev", o.Dirs.Dev)
	return a, nil
}

func (a *App) spawn(fn func()) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		fn()
	}()
}

// Reload asks the refresh loop to recompute the effective set now. Coalescing
// means a bulk edit of forty devices reconciles once, not forty times.
func (a *App) Reload() {
	select {
	case a.reload <- struct{}{}:
	default:
	}
}

// Uptime is how long the engine has been running.
func (a *App) Uptime() time.Duration { return time.Since(a.started) }

// Shutdown cancels every goroutine, flushes pending heartbeats and closes the
// database. It returns after everything has stopped or the timeout expires.
//
// The flush matters: without it the last batch of heartbeats, and possibly a
// state change, are lost on every service stop.
func (a *App) Shutdown(timeout time.Duration) error {
	a.cancel()

	done := make(chan struct{})
	go func() {
		a.Scheduler.Stop()
		a.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		a.log.Warn("shutdown timed out waiting for workers", "timeout", timeout)
	}

	// Drain whatever the writer did not get to, on a fresh context: the run
	// context is already cancelled.
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.flushRemaining(flushCtx)

	err := a.Store.Close()
	a.log.Info("engine stopped")
	return err
}

func (a *App) flushRemaining(ctx context.Context) {
	var batch []model.Heartbeat
	for {
		select {
		case h := <-a.heartbeat:
			batch = append(batch, h)
			if len(batch) < 500 {
				continue
			}
		default:
		}
		break
	}
	if len(batch) == 0 {
		return
	}
	if err := a.Store.InsertHeartbeats(ctx, batch); err != nil {
		a.log.Error("flush heartbeats on shutdown", "count", len(batch), "err", err)
		return
	}
	a.log.Info("flushed heartbeats on shutdown", "count", len(batch))
}

// --- refresh -----------------------------------------------------------------

func (a *App) runRefresh(ctx context.Context) {
	t := time.NewTicker(a.opts.RefreshInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.reload:
		}
		if err := a.refresh(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("refresh devices", "err", err)
		}
	}
}

// refresh recomputes the effective set and hands it to the scheduler. Group
// edits and device edits arrive here identically, which is the whole reason
// model.Resolve is the only place precedence is decided.
func (a *App) refresh(ctx context.Context) error {
	effs, err := a.Store.LoadEffective(ctx)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.effs = make(map[int64]model.Effective, len(effs))
	for _, e := range effs {
		a.effs[e.DeviceID] = e
		if _, ok := a.snaps[e.DeviceID]; !ok {
			a.snaps[e.DeviceID] = &state.Snapshot{Status: model.StatusUnknown}
		}
	}
	a.mu.Unlock()

	a.Scheduler.Apply(ctx, effs)
	return nil
}

// EffectiveFor returns the resolved settings for one device.
func (a *App) EffectiveFor(deviceID int64) (model.Effective, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.effs[deviceID]
	return e, ok
}

// --- writer ------------------------------------------------------------------

// runWriter batches heartbeat inserts. 200 devices on a 30 s interval is about
// seven rows a second; one transaction each would fsync the WAL that often for
// no benefit.
func (a *App) runWriter(ctx context.Context) {
	t := time.NewTicker(a.opts.FlushInterval)
	defer t.Stop()

	batch := make([]model.Heartbeat, 0, a.opts.FlushSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := a.Store.InsertHeartbeats(ctx, batch); err != nil {
			if ctx.Err() == nil {
				a.log.Error("insert heartbeats", "count", len(batch), "err", err)
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case h := <-a.heartbeat:
			batch = append(batch, h)
			if len(batch) >= a.opts.FlushSize {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

func (a *App) record(h model.Heartbeat) {
	select {
	case a.heartbeat <- h:
	default:
		// The writer is behind. Dropping one raw sample is preferable to
		// blocking the evaluator, which would stall every device's state.
		a.log.Warn("heartbeat buffer full, sample dropped", "device", h.DeviceID)
	}
}
