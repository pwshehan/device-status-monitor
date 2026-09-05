// Package scheduler runs one probe loop per device.
package scheduler

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/model"
	"github.com/pwshehan/device-status-monitor/internal/probe"
)

// DefaultMaxConcurrent caps simultaneous dials. Per-device intervals are the
// natural unit of work, so there is a goroutine per device — cheap — but the
// number of sockets open at once is what actually needs bounding.
const DefaultMaxConcurrent = 64

// Scheduler owns the per-device workers and the concurrency limit.
type Scheduler struct {
	prober  probe.Prober
	results chan<- probe.Result
	sem     chan struct{}
	log     *slog.Logger

	mu      sync.Mutex
	workers map[int64]*worker
	wg      sync.WaitGroup
	stopped bool
}

type worker struct {
	eff    model.Effective
	cancel context.CancelFunc
}

// New builds a scheduler that publishes results to out.
func New(p probe.Prober, out chan<- probe.Result, maxConcurrent int, log *slog.Logger) *Scheduler {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		prober:  p,
		results: out,
		sem:     make(chan struct{}, maxConcurrent),
		log:     log,
		workers: map[int64]*worker{},
	}
}

// Apply reconciles the running workers with the given effective set: start what
// is new, stop what is gone, restart what changed.
//
// This is the single entry point for configuration changes, so editing a group
// and editing a device take exactly the same path — a group edit simply arrives
// as a new Effective for each of its members.
func (s *Scheduler) Apply(ctx context.Context, effs []model.Effective) (started, stopped, restarted int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return 0, 0, 0
	}

	wanted := make(map[int64]model.Effective, len(effs))
	for _, e := range effs {
		wanted[e.DeviceID] = e
	}

	// Stop workers for devices that are gone, disabled or now paused.
	for id, w := range s.workers {
		e, keep := wanted[id]
		if !keep || e.Paused(time.Now()) {
			w.cancel()
			delete(s.workers, id)
			stopped++
			continue
		}
		if changed(w.eff, e) {
			w.cancel()
			delete(s.workers, id)
			s.start(ctx, e)
			restarted++
		} else {
			// Keep the ticker, but adopt fields that do not affect timing
			// (thresholds, recipients, notify) so the evaluator sees them.
			w.eff = e
		}
	}

	// Start workers for devices that are new to us.
	for id, e := range wanted {
		if _, running := s.workers[id]; running {
			continue
		}
		if e.Paused(time.Now()) {
			continue
		}
		s.start(ctx, e)
		started++
	}

	if started+stopped+restarted > 0 {
		s.log.Info("scheduler reconciled",
			"running", len(s.workers), "started", started, "stopped", stopped, "restarted", restarted)
	}
	return started, stopped, restarted
}

// changed reports whether a new Effective needs the ticker rebuilt. Only the
// fields that affect timing count; a threshold change is picked up in place.
func changed(a, b model.Effective) bool {
	return a.Addr != b.Addr || a.Interval != b.Interval || a.Timeout != b.Timeout
}

// start launches one device worker. Caller holds s.mu.
func (s *Scheduler) start(parent context.Context, e model.Effective) {
	ctx, cancel := context.WithCancel(parent)
	s.workers[e.DeviceID] = &worker{eff: e, cancel: cancel}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(ctx, e)
	}()
}

// loop probes one device forever.
func (s *Scheduler) loop(ctx context.Context, e model.Effective) {
	// Spread the first probe across the interval. Without jitter, 200 devices
	// added by an import all dial on the same tick for the rest of time.
	jitter := time.Duration(rand.Int64N(int64(e.Interval)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	t := time.NewTicker(e.Interval)
	defer t.Stop()

	s.probeOnce(ctx, e)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.probeOnce(ctx, e)
		}
	}
}

func (s *Scheduler) probeOnce(ctx context.Context, e model.Effective) {
	// Wait for a concurrency slot, but give up if we are shutting down.
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-s.sem }()

	res := s.prober.Probe(ctx, e.DeviceID, e.Addr, e.Timeout)

	select {
	case s.results <- res:
	case <-ctx.Done():
	}
}

// CheckNow probes one device immediately, outside its schedule. The result is
// returned rather than published, so a manual check does not disturb the state
// machine or write a heartbeat.
func (s *Scheduler) CheckNow(ctx context.Context, e model.Effective) probe.Result {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return probe.Result{DeviceID: e.DeviceID, Class: probe.ClassCanceled, At: time.Now()}
	}
	return s.prober.Probe(ctx, e.DeviceID, e.Addr, e.Timeout)
}

// Running returns how many device workers are active.
func (s *Scheduler) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.workers)
}

// Effective returns the effective settings a running worker is using.
func (s *Scheduler) Effective(deviceID int64) (model.Effective, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.workers[deviceID]
	if !ok {
		return model.Effective{}, false
	}
	return w.eff, true
}

// Stop cancels every worker and waits for them to return.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	s.stopped = true
	for id, w := range s.workers {
		w.cancel()
		delete(s.workers, id)
	}
	s.mu.Unlock()
	s.wg.Wait()
}
