package rollup

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pwshehan/local-device-monitor/internal/model"
	"github.com/pwshehan/local-device-monitor/internal/store"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func open(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "rollup.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SeedSettings(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func device(t *testing.T, st *store.Store, name, ip string) model.Device {
	t.Helper()
	d, err := st.CreateDevice(context.Background(), model.Device{
		Name: name, IPAddress: ip, Port: 22, Enabled: true, Notify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// midnight is local midnight `daysAgo` days back, which is how rollup days are
// keyed.
func midnight(daysAgo int) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		AddDate(0, 0, -daysAgo)
}

func day(daysAgo int) string { return midnight(daysAgo).Format("2006-01-02") }

// seedDay writes `checks` heartbeats spread across a past day, `down` of them
// failing.
func seedDay(t *testing.T, st *store.Store, deviceID int64, daysAgo, checks, down int) {
	t.Helper()
	// Spread across the target day rather than at a fixed interval: a
	// thousand rows a minute apart would run off the end of the day and land
	// in the future, which is a fixture bug that looks exactly like a pruning
	// bug.
	start := midnight(daysAgo)
	span := 24 * time.Hour
	if daysAgo == 0 {
		// Today is only as long as it has been so far: rows after now would be
		// in the future, which every window query filters out and which looks
		// exactly like a pruning bug.
		span = time.Since(start)
	}
	step := span / time.Duration(checks+1)

	batch := make([]model.Heartbeat, 0, checks)
	for i := 0; i < checks; i++ {
		hb := model.Heartbeat{
			DeviceID:  deviceID,
			Status:    model.StatusUp,
			LatencyMS: int64(4 + i%3),
			CheckedAt: start.Add(step * time.Duration(i+1)),
		}
		if i < down {
			hb.Status = model.StatusDown
			hb.LatencyMS = 3000
			hb.ErrorMsg = "TIMEOUT: i/o timeout"
		}
		batch = append(batch, hb)
	}
	if err := st.InsertHeartbeats(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func TestAggregatesFinishedDaysOnly(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")

	seedDay(t, st, d.ID, 2, 100, 10) // a finished day
	seedDay(t, st, d.ID, 0, 50, 0)   // today, still in progress

	j := &Janitor{Store: st, Log: quiet()}
	res := j.Once(ctx)

	if res.Err != nil {
		t.Fatalf("pass failed: %v", res.Err)
	}
	if res.RolledUp != 1 {
		t.Fatalf("rolled up %d days, want 1 — today must be left alone", res.RolledUp)
	}

	rows, err := st.DeviceUptime(ctx, d.ID, 7)
	if err != nil {
		t.Fatal(err)
	}

	var rolled, raw int
	for _, r := range rows {
		switch r.Source {
		case "rollup":
			rolled++
			if r.Day != day(2) {
				t.Errorf("rolled up %s, want %s", r.Day, day(2))
			}
			if r.ChecksTotal != 100 || r.ChecksUp != 90 {
				t.Errorf("checks = %d/%d, want 90/100", r.ChecksUp, r.ChecksTotal)
			}
			if r.UptimePct != 90 {
				t.Errorf("uptime = %v, want 90", r.UptimePct)
			}
			if r.P95LatencyMS == nil {
				t.Error("p95 not computed")
			}
		case "raw":
			raw++
		}
	}
	if rolled != 1 || raw != 1 {
		t.Errorf("sources = %d rollup, %d raw; want one of each", rolled, raw)
	}

	// A second pass must not redo the work, or every hour would rewrite every
	// day the database holds.
	if again := j.Once(ctx); again.RolledUp != 0 {
		t.Errorf("second pass rolled up %d days, want 0", again.RolledUp)
	}
}

func TestDowntimeComesFromIncidentsNotRowCounts(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")

	// One heartbeat, so the day is a rollup candidate, but a two-hour outage
	// in the incident log. Counting DOWN rows would report a few seconds of
	// downtime; the incident is the truth.
	seedDay(t, st, d.ID, 1, 1, 1)

	start := midnight(1).Add(3 * time.Hour)
	end := start.Add(2 * time.Hour)
	id, err := st.OpenIncident(ctx, d.ID, start, start, "TIMEOUT: i/o timeout", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CloseIncident(ctx, id, end, true); err != nil {
		t.Fatal(err)
	}

	if res := (&Janitor{Store: st, Log: quiet()}).Once(ctx); res.RolledUp != 1 {
		t.Fatalf("rolled up %d, want 1 (err %v)", res.RolledUp, res.Err)
	}

	rows, err := st.DeviceUptime(ctx, d.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	if got := rows[0].DowntimeSec; got != 7200 {
		t.Errorf("downtime = %ds, want 7200 from the incident log", got)
	}
}

func TestOutageSpanningMidnightIsSplitAcrossDays(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")

	seedDay(t, st, d.ID, 2, 5, 0)
	seedDay(t, st, d.ID, 1, 5, 0)

	// Down from 23:00 to 01:00: one hour belongs to each day. This is the case
	// counting DOWN heartbeats cannot get right at all.
	start := midnight(2).Add(23 * time.Hour)
	end := midnight(1).Add(time.Hour)
	id, err := st.OpenIncident(ctx, d.ID, start, start, "TIMEOUT: i/o timeout", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CloseIncident(ctx, id, end, true); err != nil {
		t.Fatal(err)
	}

	if res := (&Janitor{Store: st, Log: quiet()}).Once(ctx); res.RolledUp != 2 {
		t.Fatalf("rolled up %d days, want 2 (err %v)", res.RolledUp, res.Err)
	}

	rows, err := st.DeviceUptime(ctx, d.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	byDay := map[string]int64{}
	for _, r := range rows {
		byDay[r.Day] = r.DowntimeSec
	}
	if byDay[day(2)] != 3600 {
		t.Errorf("%s downtime = %d, want 3600", day(2), byDay[day(2)])
	}
	if byDay[day(1)] != 3600 {
		t.Errorf("%s downtime = %d, want 3600", day(1), byDay[day(1)])
	}
}

func TestPruningHonoursRetention(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")

	// Two days of raw rows either side of a one-day retention window.
	seedDay(t, st, d.ID, 5, 20, 0)
	seedDay(t, st, d.ID, 0, 20, 0)

	if err := st.PutSettings(ctx, map[string]string{
		store.KeyRetentionRawDays: "1",
		// Long enough that the day being pruned from raw is still inside the
		// rollup window — otherwise the janitor is right to skip it, which is
		// what TestSkipsDaysOlderThanTheRollupWindow covers.
		store.KeyRetentionRollupDays: "30",
	}); err != nil {
		t.Fatal(err)
	}

	j := &Janitor{Store: st, Log: quiet()}
	res := j.Once(ctx)
	if res.Err != nil {
		t.Fatalf("pass: %v", res.Err)
	}

	// The old day is aggregated *before* its raw rows are dropped, so the
	// history survives pruning as a summary.
	if res.RolledUp != 1 {
		t.Errorf("rolled up %d, want the old day aggregated before pruning", res.RolledUp)
	}
	if res.HeartbeatsPruned != 20 {
		t.Errorf("pruned %d heartbeats, want 20", res.HeartbeatsPruned)
	}

	hbs, err := st.Heartbeats(ctx, d.ID, midnight(30), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hbs) != 20 {
		t.Errorf("%d raw rows left, want today's 20", len(hbs))
	}

	rows, err := st.DeviceUptime(ctx, d.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if r.Day == day(5) {
			found = true
			if r.Source != "rollup" || r.ChecksTotal != 20 {
				t.Errorf("%s = %+v, want a rollup of 20 checks", r.Day, r)
			}
		}
	}
	if !found {
		t.Error("the pruned day's history did not survive as a rollup")
	}
}

func TestRollupsAgeOutToo(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")

	if err := st.WriteRollup(ctx, store.Rollup{
		DeviceID: d.ID, Day: day(400), ChecksTotal: 10, ChecksUp: 10, UptimePct: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteRollup(ctx, store.Rollup{
		DeviceID: d.ID, Day: day(2), ChecksTotal: 10, ChecksUp: 10, UptimePct: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSettings(ctx, map[string]string{store.KeyRetentionRollupDays: "30"}); err != nil {
		t.Fatal(err)
	}

	res := (&Janitor{Store: st, Log: quiet()}).Once(ctx)
	if res.RollupsPruned != 1 {
		t.Errorf("pruned %d rollups, want 1", res.RollupsPruned)
	}

	n, err := st.CountRollups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d rollups left, want 1", n)
	}
}

func TestOpenIncidentsSurvivePruning(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")

	// An outage that started long ago and has not ended. Deleting it would
	// lose the start time the eventual recovery mail needs — and the device is
	// still down right now.
	old := midnight(500)
	if _, err := st.OpenIncident(ctx, d.ID, old, old, "TIMEOUT: i/o timeout", true); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSettings(ctx, map[string]string{store.KeyRetentionRollupDays: "30"}); err != nil {
		t.Fatal(err)
	}

	(&Janitor{Store: st, Log: quiet()}).Once(ctx)

	if _, err := st.OpenIncidentFor(ctx, d.ID); err != nil {
		t.Errorf("the open incident was pruned: %v", err)
	}
}

func TestPruningChunksLargeDeletes(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")

	// More rows than one chunk, so the loop has to run more than once.
	seedDay(t, st, d.ID, 3, store.PruneChunk+250, 0)

	n, err := st.PruneHeartbeats(ctx, midnight(1))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(store.PruneChunk+250) {
		t.Errorf("pruned %d, want %d", n, store.PruneChunk+250)
	}

	left, err := st.CountHeartbeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d rows left, want 0", left)
	}
}

func TestSkipsDaysOlderThanTheRollupWindow(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")

	// A day well past the rollup retention. Aggregating it would produce a
	// summary that the same pass then deletes — and a service that has been
	// off for a year would do that hundreds of times.
	seedDay(t, st, d.ID, 90, 20, 0)
	seedDay(t, st, d.ID, 1, 20, 0)

	if err := st.PutSettings(ctx, map[string]string{
		store.KeyRetentionRawDays:    "1",
		store.KeyRetentionRollupDays: "30",
	}); err != nil {
		t.Fatal(err)
	}

	res := (&Janitor{Store: st, Log: quiet()}).Once(ctx)
	if res.RolledUp != 1 {
		t.Errorf("rolled up %d days, want only the one inside the window", res.RolledUp)
	}

	n, err := st.CountRollups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d rollups stored, want 1", n)
	}

	// The ancient raw rows are still pruned — they are past raw retention
	// whether or not anyone wanted a summary of them — while a one-day raw
	// window keeps yesterday's.
	left, err := st.CountHeartbeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if left != 20 {
		t.Errorf("%d raw rows left, want yesterday's 20 and none from 90 days ago", left)
	}
	old, err := st.Heartbeats(ctx, d.ID, midnight(91), midnight(89), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 0 {
		t.Errorf("%d rows from 90 days ago survived pruning", len(old))
	}
}

func TestConcurrentOnceAndLastPass(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")
	seedDay(t, st, d.ID, 2, 10, 0)

	j := &Janitor{Store: st, Log: quiet()}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = j.Once(ctx)
			_, _ = j.LastPass()
		}()
	}
	wg.Wait()

	at, _ := j.LastPass()
	if at.IsZero() {
		t.Fatal("last pass timestamp was not recorded")
	}
}

func TestLastPassDoesNotBlockOnInFlightOnce(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	d := device(t, st, "switch", "10.0.0.1")
	seedDay(t, st, d.ID, 2, 10, 0)

	j := &Janitor{Store: st, Log: quiet()}
	j.passMu.Lock()

	onceDone := make(chan struct{})
	go func() {
		defer close(onceDone)
		_ = j.Once(ctx)
	}()

	lastPassDone := make(chan struct{})
	go func() {
		defer close(lastPassDone)
		_, _ = j.LastPass()
	}()

	select {
	case <-lastPassDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("LastPass blocked on an in-flight Once")
	}

	j.passMu.Unlock()
	<-onceDone
}
