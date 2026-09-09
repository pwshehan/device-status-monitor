package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwshehan/local-device-monitor/internal/model"
	"github.com/pwshehan/local-device-monitor/internal/probe"
)

func eff(id int64, name string, interval time.Duration) model.Effective {
	return model.Effective{
		DeviceID: id, Name: name, Addr: "10.0.0.1:22",
		Interval: interval, Timeout: time.Second,
		FailureThreshold: 3, RecoveryThreshold: 1, Notify: true,
		Source: map[string]string{},
	}
}

// counting prober records how many times each device was probed.
func counting() (probe.Prober, func(int64) int32) {
	var mu sync.Mutex
	counts := map[int64]*int32{}

	p := probe.Func(func(_ context.Context, id int64, _ string, _ time.Duration) probe.Result {
		mu.Lock()
		c, ok := counts[id]
		if !ok {
			var n int32
			c = &n
			counts[id] = c
		}
		mu.Unlock()
		atomic.AddInt32(c, 1)
		return probe.Result{DeviceID: id, OK: true, Class: probe.ClassOK, At: time.Now()}
	})

	return p, func(id int64) int32 {
		mu.Lock()
		c := counts[id]
		mu.Unlock()
		if c == nil {
			return 0
		}
		return atomic.LoadInt32(c)
	}
}

func drain(t *testing.T, out <-chan probe.Result, ctx context.Context) *int32 {
	t.Helper()
	var n int32
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-out:
				atomic.AddInt32(&n, 1)
			}
		}
	}()
	return &n
}

func TestApplyStartsStopsAndRestarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan probe.Result, 256)
	p, _ := counting()
	s := New(p, out, 8, nil)
	defer s.Stop()
	drain(t, out, ctx)

	// Two devices start.
	started, stopped, restarted := s.Apply(ctx, []model.Effective{
		eff(1, "a", 50*time.Millisecond),
		eff(2, "b", 50*time.Millisecond),
	})
	if started != 2 || stopped != 0 || restarted != 0 {
		t.Fatalf("first apply: started=%d stopped=%d restarted=%d", started, stopped, restarted)
	}
	if s.Running() != 2 {
		t.Fatalf("running = %d, want 2", s.Running())
	}

	// Re-applying the same set changes nothing: a no-op edit must not restart
	// every ticker.
	started, stopped, restarted = s.Apply(ctx, []model.Effective{
		eff(1, "a", 50*time.Millisecond),
		eff(2, "b", 50*time.Millisecond),
	})
	if started+stopped+restarted != 0 {
		t.Errorf("idempotent apply churned: started=%d stopped=%d restarted=%d", started, stopped, restarted)
	}

	// Changing one interval restarts only that worker.
	started, stopped, restarted = s.Apply(ctx, []model.Effective{
		eff(1, "a", 20*time.Millisecond),
		eff(2, "b", 50*time.Millisecond),
	})
	if restarted != 1 || started != 0 || stopped != 0 {
		t.Errorf("interval change: started=%d stopped=%d restarted=%d, want 1 restart",
			started, stopped, restarted)
	}

	// Dropping a device stops its worker.
	started, stopped, restarted = s.Apply(ctx, []model.Effective{eff(1, "a", 20*time.Millisecond)})
	if stopped != 1 {
		t.Errorf("stopped = %d, want 1", stopped)
	}
	if s.Running() != 1 {
		t.Errorf("running = %d, want 1", s.Running())
	}
}

func TestThresholdChangeDoesNotRestartTicker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan probe.Result, 256)
	p, _ := counting()
	s := New(p, out, 8, nil)
	defer s.Stop()
	drain(t, out, ctx)

	s.Apply(ctx, []model.Effective{eff(1, "a", time.Second)})

	e := eff(1, "a", time.Second)
	e.FailureThreshold = 7
	e.Notify = false
	_, _, restarted := s.Apply(ctx, []model.Effective{e})
	if restarted != 0 {
		t.Errorf("restarted = %d; a threshold change should be adopted in place", restarted)
	}
	// ...but the worker must be using the new values.
	got, ok := s.Effective(1)
	if !ok {
		t.Fatal("worker vanished")
	}
	if got.FailureThreshold != 7 || got.Notify {
		t.Errorf("worker still using stale settings: %+v", got)
	}
}

func TestPausedDeviceIsNotProbed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan probe.Result, 256)
	p, count := counting()
	s := New(p, out, 8, nil)
	defer s.Stop()
	drain(t, out, ctx)

	paused := eff(1, "paused", 10*time.Millisecond)
	paused.PausedUntil = time.Now().Add(time.Hour)
	active := eff(2, "active", 10*time.Millisecond)

	started, _, _ := s.Apply(ctx, []model.Effective{paused, active})
	if started != 1 {
		t.Fatalf("started = %d, want 1 (the paused device must not be scheduled)", started)
	}

	time.Sleep(120 * time.Millisecond)
	if n := count(1); n != 0 {
		t.Errorf("paused device was probed %d times", n)
	}
	if n := count(2); n == 0 {
		t.Error("active device was never probed")
	}
}

func TestPauseExpiryResumesOnNextApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan probe.Result, 256)
	p, count := counting()
	s := New(p, out, 8, nil)
	defer s.Stop()
	drain(t, out, ctx)

	e := eff(1, "d", 10*time.Millisecond)
	e.PausedUntil = time.Now().Add(60 * time.Millisecond)
	s.Apply(ctx, []model.Effective{e})
	if s.Running() != 0 {
		t.Fatal("paused device should not be running")
	}

	// The refresh loop re-applies periodically, which is what makes a pause
	// expire without anything having to wake up on a timer of its own.
	time.Sleep(80 * time.Millisecond)
	started, _, _ := s.Apply(ctx, []model.Effective{e})
	if started != 1 {
		t.Fatalf("started = %d, want the expired pause to resume the worker", started)
	}
	time.Sleep(60 * time.Millisecond)
	if count(1) == 0 {
		t.Error("resumed device was never probed")
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const limit = 4
	var inFlight, peak int32
	var mu sync.Mutex

	p := probe.Func(func(_ context.Context, id int64, _ string, _ time.Duration) probe.Result {
		n := atomic.AddInt32(&inFlight, 1)
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return probe.Result{DeviceID: id, OK: true, Class: probe.ClassOK, At: time.Now()}
	})

	out := make(chan probe.Result, 1024)
	s := New(p, out, limit, nil)
	defer s.Stop()
	drain(t, out, ctx)

	var effs []model.Effective
	for i := int64(1); i <= 40; i++ {
		effs = append(effs, eff(i, "d", 15*time.Millisecond))
	}
	s.Apply(ctx, effs)

	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	got := peak
	mu.Unlock()
	if got > limit {
		t.Errorf("peak concurrency = %d, limit was %d", got, limit)
	}
	if got == 0 {
		t.Error("nothing was probed")
	}
}

func TestStopEndsEveryWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan probe.Result, 256)
	p, count := counting()
	s := New(p, out, 8, nil)
	drain(t, out, ctx)

	var effs []model.Effective
	for i := int64(1); i <= 10; i++ {
		effs = append(effs, eff(i, "d", 10*time.Millisecond))
	}
	s.Apply(ctx, effs)
	time.Sleep(50 * time.Millisecond)

	s.Stop() // must not hang
	if s.Running() != 0 {
		t.Errorf("running = %d after Stop", s.Running())
	}

	before := count(1)
	time.Sleep(60 * time.Millisecond)
	if after := count(1); after != before {
		t.Errorf("worker still probing after Stop: %d -> %d", before, after)
	}

	// Apply after Stop is a no-op rather than a resurrection.
	if started, _, _ := s.Apply(ctx, effs); started != 0 {
		t.Errorf("Apply after Stop started %d workers", started)
	}
}

func TestCheckNowDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan probe.Result, 16)
	p, _ := counting()
	s := New(p, out, 8, nil)
	defer s.Stop()

	res := s.CheckNow(ctx, eff(1, "d", time.Hour))
	if !res.OK {
		t.Fatalf("want a result, got %+v", res)
	}
	// A manual check must not feed the state machine or write a heartbeat.
	select {
	case r := <-out:
		t.Errorf("CheckNow published %+v to the results channel", r)
	case <-time.After(50 * time.Millisecond):
	}
}
