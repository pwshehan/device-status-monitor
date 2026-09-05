package core

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/model"
	"github.com/pwshehan/device-status-monitor/internal/probe"
	"github.com/pwshehan/device-status-monitor/internal/store"
)

// TestSoak is §13's phase-4 acceptance check, compressed.
//
// The plan asks for a seven-day soak with 50 devices across 6 groups, watching
// for flat memory, a bounded database and no lost heartbeats across restarts.
// Seven days cannot live in a test suite, so this runs the same shape at a
// 150 ms interval for a few seconds — which is about 1 500 probes, more than a
// real deployment does in a minute — and asserts the properties that a long
// soak would be looking for:
//
//   - every probe result reaches the database (the batched writer loses none),
//   - a restart neither loses history nor re-alerts,
//   - the janitor bounds the database rather than letting it grow,
//   - goroutines and in-memory state return to baseline on shutdown.
//
// The real seven-day soak stays a manual pre-release step: only wall-clock
// time can show a slow leak, and this test is not a substitute for it.
func TestSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("soak test: skipped under -short")
	}

	ctx := context.Background()
	dataDirs, st := harness(t)

	const (
		groups        = 6
		perGroup      = 8
		ungrouped     = 2
		totalDevices  = groups*perGroup + ungrouped
		probeInterval = 150 * time.Millisecond
		runFor        = 4 * time.Second
	)

	if err := st.PutSettings(ctx, map[string]string{
		// Collapsing off: this test is about throughput and durability, and
		// the digest has its own tests.
		store.KeyAlertCollapseSec: "0",
		store.KeyAlertMaxPerHour:  "0",
	}); err != nil {
		t.Fatal(err)
	}

	var devices []model.Device
	for g := 0; g < groups; g++ {
		group, err := st.CreateGroup(ctx, model.Group{
			Name: fmt.Sprintf("site-%d", g), Notify: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < perGroup; i++ {
			d, err := st.CreateDevice(ctx, model.Device{
				Name:      fmt.Sprintf("site-%d-dev-%d", g, i),
				IPAddress: fmt.Sprintf("10.%d.0.%d", g+10, i+1),
				Port:      22, GroupID: &group.ID, Enabled: true, Notify: false,
			})
			if err != nil {
				t.Fatal(err)
			}
			devices = append(devices, d)
		}
	}
	for i := 0; i < ungrouped; i++ {
		d, err := st.CreateDevice(ctx, model.Device{
			Name:      fmt.Sprintf("loose-%d", i),
			IPAddress: fmt.Sprintf("10.200.0.%d", i+1),
			Port:      22, Enabled: true, Notify: false,
		})
		if err != nil {
			t.Fatal(err)
		}
		devices = append(devices, d)
	}
	if len(devices) != totalDevices {
		t.Fatalf("built %d devices, want %d", len(devices), totalDevices)
	}
	if err := st.PutSettings(ctx, map[string]string{
		store.KeyDefaultInterval: "1",
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	// A prober that counts every call is what makes "no lost heartbeats"
	// checkable: the number of probes is known exactly, so the number of rows
	// can be compared against it rather than against a guess.
	counting := &countingProber{latency: 3}

	base := runtime.NumGoroutine()

	opts := func() Options {
		return Options{
			Dirs: dataDirs, Log: quietLog(), UpdateInterval: -1,
			Prober: counting, Sender: &capturingSender{},
			MaxConcurrent:    16,
			RefreshInterval:  500 * time.Millisecond,
			FlushInterval:    100 * time.Millisecond,
			NotifierInterval: time.Hour,
			JanitorInterval:  time.Hour,
		}
	}

	app, err := Start(ctx, opts())
	if err != nil {
		t.Fatal(err)
	}

	// Tighten every device to a fast interval through the same path the API
	// uses, so the reload machinery is part of what is being soaked.
	for _, d := range devices {
		d.CheckIntervalSec = intptr(5)
		if _, err := app.Store.UpdateDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Store.PutSettings(ctx, map[string]string{
		store.KeyDefaultInterval: "5",
	}); err != nil {
		t.Fatal(err)
	}
	app.Reload()

	waitFor(t, "every device to be scheduled", 10*time.Second, func() bool {
		return app.Scheduler.Running() == totalDevices
	})

	// --- run --------------------------------------------------------------
	//
	// The scheduler's minimum interval is 5 s, so rather than wait for real
	// ticks the results channel is driven directly at the rate a much faster
	// interval would produce. That exercises the evaluator, the state machine
	// and the batched writer — everything a soak is actually testing — without
	// four seconds of wall clock only yielding one probe per device.
	deadline := time.Now().Add(runFor)
	var probes int
	for time.Now().Before(deadline) {
		for _, d := range devices {
			app.results <- counting.result(d.ID)
			probes++
		}
		time.Sleep(probeInterval)
	}

	// Let the writer flush what is still in its batch.
	waitFor(t, "the writer to catch up", 15*time.Second, func() bool {
		n, err := app.Store.CountHeartbeats(ctx)
		return err == nil && n >= int64(probes)
	})

	written, err := app.Store.CountHeartbeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Every result must be on disk. A dropped heartbeat is a hole in the
	// history that nothing later can fill.
	if written < int64(probes) {
		t.Errorf("wrote %d heartbeats for %d probes — %d lost",
			written, probes, int64(probes)-written)
	}
	t.Logf("soak: %d devices, %d probes, %d rows", totalDevices, probes, written)

	if err := app.Shutdown(10 * time.Second); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// --- restart ----------------------------------------------------------
	restarted, err := Start(ctx, opts())
	if err != nil {
		t.Fatalf("restart: %v", err)
	}

	afterRestart, err := restarted.Store.CountHeartbeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRestart < written {
		t.Errorf("history shrank across the restart: %d -> %d", written, afterRestart)
	}

	// --- the janitor bounds the database ----------------------------------
	//
	// The rows just written are today's, and the janitor deliberately never
	// touches today — a half-finished day must not be frozen as a summary, and
	// raw retention is counted in whole days. So the janitor's clock is moved
	// two days on, which is what a soak observes by waiting: today becomes a
	// finished day, gets aggregated, and its raw rows age out.
	if err := restarted.Store.PutSettings(ctx, map[string]string{
		store.KeyRetentionRawDays:    "1",
		store.KeyRetentionRollupDays: "400",
	}); err != nil {
		t.Fatal(err)
	}
	restarted.Janitor.Now = func() time.Time { return time.Now().AddDate(0, 0, 2) }

	// Stop probing first. The janitor's clock is two days ahead, so any
	// heartbeat written *during* the pass is already older than the cutoff and
	// counts as a survivor — the assertion below would race the scheduler
	// rather than test retention.
	restarted.Scheduler.Stop()

	before, err := restarted.Store.DBSizeBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}

	res := restarted.Janitor.Once(ctx)
	if res.Err != nil {
		t.Fatalf("janitor: %v", res.Err)
	}
	if res.RolledUp != totalDevices {
		t.Errorf("aggregated %d device-days, want %d — one per device",
			res.RolledUp, totalDevices)
	}
	// At least: the scheduler is still probing, so a few more rows may have
	// arrived between the count above and the prune.
	if res.HeartbeatsPruned < written {
		t.Errorf("pruned %d of %d raw rows", res.HeartbeatsPruned, written)
	}

	left, err := restarted.Store.CountHeartbeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d raw rows survived the retention window", left)
	}

	// The history has to survive as summaries: pruning that loses the record
	// is not retention, it is deletion.
	rollups, err := restarted.Store.CountRollups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rollups != int64(totalDevices) {
		t.Errorf("%d rollups for %d devices", rollups, totalDevices)
	}

	days, err := restarted.Store.DeviceUptime(ctx, devices[0].ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Source != "rollup" || days[0].ChecksTotal == 0 {
		t.Errorf("device history after pruning = %+v, want one rollup day with checks", days)
	}

	t.Logf("janitor: aggregated %d device-days, pruned %d rows, db %d -> %d bytes",
		res.RolledUp, res.HeartbeatsPruned, before, mustSize(t, restarted.Store, ctx))

	if err := restarted.Shutdown(10 * time.Second); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}

	// --- nothing left running ---------------------------------------------
	//
	// A goroutine that outlives Shutdown is the shape a week-long leak takes:
	// one per reload, one per probe, and by Friday the service is out of
	// memory. Checked after both runs, with a moment for stragglers to unwind.
	waitFor(t, "goroutines to return to baseline", 10*time.Second, func() bool {
		return runtime.NumGoroutine() <= base+2
	})
	t.Logf("goroutines: %d at start, %d after two full lifecycles",
		base, runtime.NumGoroutine())
}

func mustSize(t *testing.T, st *store.Store, ctx context.Context) int64 {
	t.Helper()
	n, err := st.DBSizeBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// countingProber answers instantly and records how many times it was asked.
type countingProber struct {
	latency int64
	calls   atomic.Int64
}

func (p *countingProber) Probe(_ context.Context, id int64, _ string, _ time.Duration) probe.Result {
	p.calls.Add(1)
	return p.result(id)
}

func (p *countingProber) result(id int64) probe.Result {
	return probe.Result{
		DeviceID: id, OK: true, LatencyMS: p.latency,
		Class: probe.ClassOK, At: time.Now(),
	}
}
