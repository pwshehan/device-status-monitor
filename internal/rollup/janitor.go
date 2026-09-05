// Package rollup keeps the database bounded: it aggregates finished days,
// prunes what has aged out, and reclaims the space.
//
// Without it nothing here is sustainable. Two hundred devices on a 30-second
// interval write 576 000 heartbeat rows a day — about 40 MB — and a monitor
// that fills the disk it runs on has failed at its job in the most ironic way
// available.
package rollup

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/store"
)

// Defaults for a pass. Every one of them is deliberately unexciting: the
// janitor's job is to be invisible.
const (
	// DefaultInterval is how often a pass runs. Hourly is often enough that no
	// single pass has much to do, which is what keeps each write lock short.
	DefaultInterval = time.Hour

	// RollupBatch caps the device-days aggregated in one pass. A service that
	// has been off for a month comes back to a lot of work; doing it in
	// hourly batches keeps the writer responsive while it catches up.
	RollupBatch = 500

	// VacuumFreeRatio is how empty the file must be before a VACUUM is worth
	// it. VACUUM rewrites the whole database and blocks everything, so it is
	// reserved for when a retention change has actually left a hole.
	VacuumFreeRatio = 0.25

	// VacuumInterval is the minimum gap between vacuums, however empty the
	// file looks.
	VacuumInterval = 24 * time.Hour
)

// Janitor runs the maintenance pass.
type Janitor struct {
	Store    *store.Store
	Interval time.Duration
	Log      *slog.Logger

	// Now is injectable so the tests can place work in the past without
	// waiting for a real day to end.
	Now func() time.Time

	passMu     sync.Mutex
	statusMu   sync.RWMutex
	lastVacuum time.Time
	lastPass   time.Time
	lastResult Result
}

// Result is what one pass did, for the log and for /api/health.
type Result struct {
	At               time.Time
	RolledUp         int
	HeartbeatsPruned int64
	RollupsPruned    int64
	IncidentsPruned  int64
	AlertsPruned     int64
	Vacuumed         bool
	Err              error
}

// Run performs a pass at startup and then on the interval.
//
// The startup pass matters: a service that was off for a week has a week of
// un-aggregated days and a week of raw rows past their retention, and waiting
// an hour to start on that is a strange first act.
func (j *Janitor) Run(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	j.Once(ctx)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.Once(ctx)
		}
	}
}

// Once runs a single pass. Exported so a test — or a future maintenance
// subcommand — can do the work without waiting on a ticker.
func (j *Janitor) Once(ctx context.Context) Result {
	j.passMu.Lock()
	defer j.passMu.Unlock()

	now := j.now()
	res := Result{At: now}
	log := j.logger()

	retention, err := j.Store.Retention(ctx)
	if err != nil {
		// A malformed retention setting must not stop the aggregation half of
		// the pass, so the built-in defaults stand in and the pass continues.
		log.Warn("read retention settings, using defaults", "err", err)
	}

	rollupBefore := startOfDay(now).AddDate(0, 0, -retention.RollupDays)

	res.RolledUp, err = j.rollUp(ctx, now, rollupBefore.Format(dayFormat))
	if err != nil {
		res.Err = err
		log.Error("aggregate finished days", "err", err)
	}

	// Pruning runs even if aggregation failed, but never for a day that has
	// not been rolled up yet — that ordering is why the two steps are separate
	// and why raw rows are only dropped by age.
	rawBefore := startOfDay(now).AddDate(0, 0, -retention.RawDays)
	if n, err := j.Store.PruneHeartbeats(ctx, rawBefore); err != nil {
		res.Err = err
		log.Error("prune heartbeats", "err", err)
	} else {
		res.HeartbeatsPruned = n
	}

	if n, err := j.Store.PruneRollups(ctx, rollupBefore.Format(dayFormat)); err != nil {
		res.Err = err
		log.Error("prune rollups", "err", err)
	} else {
		res.RollupsPruned = n
	}

	// Incidents and delivered mail age out with the rollups: past that point
	// the daily summaries are the history, and an incident older than the
	// summaries it fed has nothing left to explain.
	if n, err := j.Store.PruneResolvedIncidents(ctx, rollupBefore); err != nil {
		log.Error("prune incidents", "err", err)
	} else {
		res.IncidentsPruned = n
	}
	if n, err := j.Store.PruneSentAlerts(ctx, rawBefore); err != nil {
		log.Error("prune sent alerts", "err", err)
	} else {
		res.AlertsPruned = n
	}

	if err := j.Store.Checkpoint(ctx); err != nil && ctx.Err() == nil {
		log.Warn("wal checkpoint", "err", err)
	}
	res.Vacuumed = j.maybeVacuum(ctx, now)

	if res.RolledUp > 0 || res.HeartbeatsPruned > 0 || res.RollupsPruned > 0 || res.Vacuumed {
		log.Info("janitor pass",
			"rolled_up", res.RolledUp,
			"heartbeats_pruned", res.HeartbeatsPruned,
			"rollups_pruned", res.RollupsPruned,
			"incidents_pruned", res.IncidentsPruned,
			"alerts_pruned", res.AlertsPruned,
			"vacuumed", res.Vacuumed,
			"raw_days", retention.RawDays,
			"rollup_days", retention.RollupDays)
	}

	j.statusMu.Lock()
	j.lastPass = now
	j.lastResult = res
	j.statusMu.Unlock()
	return res
}

// rollUp aggregates every finished day that has no rollup yet.
//
// `oldestKept` is the rollup retention boundary. Days older than it are
// skipped rather than aggregated: writing a summary that the pruning step
// three lines later would delete is pure waste, and on a service that has been
// off for longer than the retention window it would be a lot of waste.
func (j *Janitor) rollUp(ctx context.Context, now time.Time, oldestKept string) (int, error) {
	// Only days that have ended: a half-finished day would be frozen at
	// whatever it looked like when the janitor ran and never revisited.
	candidates, err := j.Store.PendingRollups(ctx, startOfDay(now), RollupBatch)
	if err != nil {
		return 0, err
	}

	written, skipped := 0, 0
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return written, nil
		}
		if c.Day < oldestKept {
			skipped++
			continue
		}

		r := store.Rollup{
			DeviceID:     c.DeviceID,
			Day:          c.Day,
			ChecksTotal:  c.ChecksTotal,
			ChecksUp:     c.ChecksUp,
			AvgLatencyMS: c.AvgLatency,
		}
		if c.ChecksTotal > 0 {
			r.UptimePct = float64(c.ChecksUp) * 100 / float64(c.ChecksTotal)
		}
		if p95, err := j.Store.P95Latency(ctx, c.DeviceID, c.Day); err != nil {
			j.logger().Warn("p95 for day", "device", c.DeviceID, "day", c.Day, "err", err)
		} else {
			r.P95LatencyMS = p95
		}
		if down, err := j.Store.DowntimeForDay(ctx, c.DeviceID, c.Day); err != nil {
			j.logger().Warn("downtime for day", "device", c.DeviceID, "day", c.Day, "err", err)
		} else {
			r.DowntimeSec = down
		}

		if err := j.Store.WriteRollup(ctx, r); err != nil {
			return written, err
		}
		written++
	}
	if skipped > 0 {
		j.logger().Info("skipped days older than the rollup window",
			"days", skipped, "oldest_kept", oldestKept)
	}
	return written, nil
}

// maybeVacuum reclaims space, but only when there is enough of it to be worth
// blocking the database for.
func (j *Janitor) maybeVacuum(ctx context.Context, now time.Time) bool {
	if !j.lastVacuum.IsZero() && now.Sub(j.lastVacuum) < VacuumInterval {
		return false
	}
	free, total, err := j.Store.FreePages(ctx)
	if err != nil || total == 0 {
		return false
	}
	if float64(free)/float64(total) < VacuumFreeRatio {
		return false
	}
	if err := j.Store.Vacuum(ctx); err != nil {
		if ctx.Err() == nil {
			j.logger().Warn("vacuum", "err", err)
		}
		return false
	}
	j.lastVacuum = now
	return true
}

// LastPass reports what the most recent pass did, for /api/health.
func (j *Janitor) LastPass() (time.Time, Result) {
	j.statusMu.RLock()
	defer j.statusMu.RUnlock()
	return j.lastPass, j.lastResult
}

func (j *Janitor) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Janitor) logger() *slog.Logger {
	if j.Log != nil {
		return j.Log
	}
	return slog.Default()
}

// dayFormat is the rollup key: a local calendar date.
const dayFormat = "2006-01-02"

// startOfDay is local midnight, which is the boundary rollup days use.
func startOfDay(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
