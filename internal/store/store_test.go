package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/model"
	"github.com/pwshehan/device-status-monitor/internal/state"
)

func ptr[T any](v T) *T { return &v }

func open(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SeedSettings(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	v1, _ := s1.SchemaVersion(ctx)
	if v1 != 1 {
		t.Fatalf("schema version = %d, want 1", v1)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	// Re-opening must not re-run the migration.
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	v2, _ := s2.SchemaVersion(ctx)
	if v2 != v1 {
		t.Errorf("schema version changed on reopen: %d -> %d", v1, v2)
	}
}

func TestSeedSettingsDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	if err := s.PutSettings(ctx, map[string]string{KeyDefaultInterval: "90"}); err != nil {
		t.Fatal(err)
	}
	// An upgrade re-seeds; the user's value must survive.
	if err := s.SeedSettings(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Setting(ctx, KeyDefaultInterval)
	if got != "90" {
		t.Errorf("interval = %q, want the user's 90", got)
	}
}

func TestDefaultsFallBackOnGarbage(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	// A typo in one setting must not stop every device being monitored.
	if err := s.PutSettings(ctx, map[string]string{
		KeyDefaultInterval: "thirty",
		KeyDefaultTimeout:  "-4",
	}); err != nil {
		t.Fatal(err)
	}
	def, err := s.Defaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if def.CheckIntervalSec != 30 || def.TimeoutSec != 3 {
		t.Errorf("defaults = %+v, want the built-in fallbacks", def)
	}
}

func TestDeleteGroupOrphansDevicesInsteadOfDeletingThem(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	g, err := s.CreateGroup(ctx, model.Group{Name: "Warehouse", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"switch-a", "switch-b"} {
		if _, err := s.CreateDevice(ctx, model.Device{
			Name: name, IPAddress: "10.0.0." + string(rune('1'+i)), Port: 22,
			GroupID: &g.ID, Enabled: true, Notify: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	orphaned, err := s.DeleteGroup(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orphaned != 2 {
		t.Errorf("orphaned = %d, want 2", orphaned)
	}

	// The devices must still exist, now ungrouped. Deleting a label must never
	// delete the things it labelled.
	devices, err := s.ListDevices(ctx, DeviceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("device count = %d, want 2 survivors", len(devices))
	}
	for _, d := range devices {
		if d.GroupID != nil {
			t.Errorf("%s still has group %v", d.Name, *d.GroupID)
		}
	}
}

func TestDeleteDeviceCascadesHistory(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	d, err := s.CreateDevice(ctx, model.Device{Name: "n", IPAddress: "10.0.0.1", Port: 22, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.InsertHeartbeats(ctx, []model.Heartbeat{
		{DeviceID: d.ID, Status: model.StatusUp, LatencyMS: 3, CheckedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(ctx, d.ID, now, now, "timeout", true); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountHeartbeats(ctx); n != 0 {
		t.Errorf("heartbeats = %d, want 0 after cascade", n)
	}
	if n, _ := s.CountOpenIncidents(ctx); n != 0 {
		t.Errorf("open incidents = %d, want 0 after cascade", n)
	}
	if err := s.DeleteDevice(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

func TestLoadEffectiveResolvesThroughGroup(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	g, err := s.CreateGroup(ctx, model.Group{
		Name: "Warehouse", Notify: true,
		CheckIntervalSec: ptr(60), Recipients: ptr("warehouse@example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDevice(ctx, model.Device{
		Name: "inherits", IPAddress: "10.0.0.1", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDevice(ctx, model.Device{
		Name: "overrides", IPAddress: "10.0.0.2", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: true, CheckIntervalSec: ptr(10),
	}); err != nil {
		t.Fatal(err)
	}
	// Disabled devices are not scheduled at all.
	if _, err := s.CreateDevice(ctx, model.Device{
		Name: "disabled", IPAddress: "10.0.0.3", Port: 22, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}

	effs, err := s.LoadEffective(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(effs) != 2 {
		t.Fatalf("effective count = %d, want 2 (disabled excluded)", len(effs))
	}

	byName := map[string]model.Effective{}
	for _, e := range effs {
		byName[e.Name] = e
	}
	if got := byName["inherits"]; got.Interval != 60*time.Second || got.Source["interval"] != "group:Warehouse" {
		t.Errorf("inherits: interval=%s source=%s", got.Interval, got.Source["interval"])
	}
	if got := byName["overrides"]; got.Interval != 10*time.Second || got.Source["interval"] != "device" {
		t.Errorf("overrides: interval=%s source=%s", got.Interval, got.Source["interval"])
	}
	if got := byName["inherits"]; len(got.Recipients) != 1 || got.Recipients[0] != "warehouse@example.com" {
		t.Errorf("recipients = %v, want the group's", got.Recipients)
	}
}

func TestPauseGroupDoesNotTouchMemberRows(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	g, err := s.CreateGroup(ctx, model.Group{Name: "Warehouse", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	// One device is individually paused before the maintenance window opens.
	devicePause := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	d, err := s.CreateDevice(ctx, model.Device{
		Name: "nas", IPAddress: "10.0.9.5", Port: 445,
		GroupID: &g.ID, Enabled: true, Notify: true, PausedUntil: &devicePause,
	})
	if err != nil {
		t.Fatal(err)
	}

	window := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := s.PauseGroup(ctx, g.ID, &window); err != nil {
		t.Fatal(err)
	}
	// Resume the group again.
	if err := s.PauseGroup(ctx, g.ID, nil); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetDevice(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PausedUntil == nil || !got.PausedUntil.Equal(devicePause.UTC()) {
		t.Errorf("device pause = %v, want %v — resuming the group clobbered it",
			got.PausedUntil, devicePause.UTC())
	}
}

func TestBulkMoveIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	g, _ := s.CreateGroup(ctx, model.Group{Name: "Head Office", Notify: true})
	var ids []int64
	for i := range 5 {
		d, err := s.CreateDevice(ctx, model.Device{
			Name: "d", IPAddress: "10.1.0." + string(rune('1'+i)), Port: 22, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}

	n, err := s.Bulk(ctx, BulkMove, ids, &g.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("moved %d, want 5", n)
	}
	moved, err := s.ListDevices(ctx, DeviceFilter{GroupID: &g.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 5 {
		t.Errorf("group holds %d devices, want 5", len(moved))
	}

	// Moving to nil ungroups them again.
	if _, err := s.Bulk(ctx, BulkMove, ids, nil, nil); err != nil {
		t.Fatal(err)
	}
	ungrouped, _ := s.ListDevices(ctx, DeviceFilter{Ungrouped: true})
	if len(ungrouped) != 5 {
		t.Errorf("ungrouped = %d, want 5", len(ungrouped))
	}
}

func TestIncidentLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	d, _ := s.CreateDevice(ctx, model.Device{Name: "n", IPAddress: "10.0.0.1", Port: 22, Enabled: true})

	started := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	detected := started.Add(90 * time.Second)
	id, err := s.OpenIncident(ctx, d.ID, started, detected, "TIMEOUT: i/o timeout", true)
	if err != nil {
		t.Fatal(err)
	}

	open, err := s.OpenIncidentFor(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !open.StartedAt.Equal(started.UTC()) {
		t.Errorf("started_at = %v, want the first failure at %v", open.StartedAt, started.UTC())
	}
	if !open.AlertSent {
		t.Error("alert_sent should be recorded")
	}

	resolved := started.Add(10 * time.Minute)
	if err := s.CloseIncident(ctx, id, resolved, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncidentFor(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("still open after close: %v", err)
	}

	log, err := s.Incidents(ctx, d.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 {
		t.Fatalf("incident log = %d rows", len(log))
	}
	if log[0].DurationSec == nil || *log[0].DurationSec != 600 {
		t.Errorf("duration = %v, want 600s measured from the first failure", log[0].DurationSec)
	}
}

func TestOutboxBackoffAndExhaustion(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	now := time.Now().Truncate(time.Second)
	id, err := s.Enqueue(ctx, model.Alert{
		Kind: model.AlertDown, Subject: "[DOWN] test",
		BodyText: "body", Recipients: "ops@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	due, err := s.DueAlerts(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("due = %d, want 1", len(due))
	}

	// One failure pushes it a minute out.
	exhausted, err := s.MarkFailed(ctx, id, 0, errors.New("dial tcp: refused"), now)
	if err != nil {
		t.Fatal(err)
	}
	if exhausted {
		t.Error("exhausted after one attempt")
	}
	if due, _ := s.DueAlerts(ctx, now, 10); len(due) != 0 {
		t.Error("alert is due again immediately; backoff not applied")
	}
	if due, _ := s.DueAlerts(ctx, now.Add(2*time.Minute), 10); len(due) != 1 {
		t.Error("alert should be due after the backoff elapses")
	}

	// Past the last backoff step it is given up on, not retried forever.
	exhausted, err = s.MarkFailed(ctx, id, MaxAttempts, errors.New("still refused"), now)
	if err != nil {
		t.Fatal(err)
	}
	if !exhausted {
		t.Error("want exhausted past the last backoff step")
	}

	if err := s.MarkSent(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.PendingAlerts(ctx); n != 0 {
		t.Errorf("pending = %d, want 0", n)
	}
}

func TestLoadSnapshotsResumesOpenIncident(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	d, _ := s.CreateDevice(ctx, model.Device{Name: "n", IPAddress: "10.0.0.1", Port: 22, Enabled: true})

	started := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	incID, err := s.OpenIncident(ctx, d.ID, started, started.Add(90*time.Second), "TIMEOUT", true)
	if err != nil {
		t.Fatal(err)
	}
	snap := state.Snapshot{
		Status: model.StatusDown, ConsecutiveFailures: 4, FirstFailureAt: started,
	}
	if err := s.SaveState(ctx, d.ID, snap, LiveResult{
		CheckedAt: time.Now(), LatencyMS: 3000, ErrMsg: "TIMEOUT", StatusChanged: true,
	}); err != nil {
		t.Fatal(err)
	}

	// This is what a service restart does.
	snaps, err := s.LoadSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := snaps[d.ID]
	if got == nil {
		t.Fatal("no snapshot for the device")
	}
	if got.Status != model.StatusDown || got.ConsecutiveFailures != 4 {
		t.Errorf("snapshot = %+v, want DOWN with 4 failures", got)
	}
	if got.OpenIncidentID != incID {
		t.Errorf("open incident = %d, want %d", got.OpenIncidentID, incID)
	}
	if !got.OpenIncidentStart.Equal(started.UTC()) {
		t.Errorf("incident start = %v, want %v", got.OpenIncidentStart, started.UTC())
	}
	if !got.AlertSent {
		t.Error("alert_sent must survive a restart, or the outage re-alerts")
	}
}

func TestConcurrentReadWhileWriting(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	d, _ := s.CreateDevice(ctx, model.Device{Name: "n", IPAddress: "10.0.0.1", Port: 22, Enabled: true})

	done := make(chan error, 1)
	go func() {
		now := time.Now()
		for i := range 50 {
			batch := make([]model.Heartbeat, 0, 20)
			for j := range 20 {
				batch = append(batch, model.Heartbeat{
					DeviceID: d.ID, Status: model.StatusUp, LatencyMS: int64(j),
					CheckedAt: now.Add(time.Duration(i*20+j) * time.Second),
				})
			}
			if err := s.InsertHeartbeats(ctx, batch); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	// WAL is what lets these reads proceed while the writer holds the lock.
	for range 100 {
		if _, err := s.ListDevices(ctx, DeviceFilter{}); err != nil {
			t.Fatalf("read during write: %v", err)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("write: %v", err)
	}

	n, _ := s.CountHeartbeats(ctx)
	if n != 1000 {
		t.Errorf("heartbeats = %d, want 1000", n)
	}
}

func TestTagFilterMatchesWholeTags(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	if _, err := s.CreateDevice(ctx, model.Device{
		Name: "a", IPAddress: "10.0.0.1", Port: 22, Enabled: true, Tags: "critical,edge",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDevice(ctx, model.Device{
		Name: "b", IPAddress: "10.0.0.2", Port: 22, Enabled: true, Tags: "non-critical",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListDevices(ctx, DeviceFilter{Tag: "critical"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("tag filter returned %d rows (%v); 'non-critical' must not match 'critical'", len(got), got)
	}
}

func TestRecentChecksAreOldestFirstAndPerDevice(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	a, err := s.CreateDevice(ctx, model.Device{
		Name: "a", IPAddress: "10.0.0.1", Port: 22, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateDevice(ctx, model.Device{
		Name: "b", IPAddress: "10.0.0.2", Port: 22, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Six checks for a, the middle two failing; two for b.
	base := time.Now().Add(-time.Hour)
	var batch []model.Heartbeat
	for i := 0; i < 6; i++ {
		status := model.StatusUp
		if i == 2 || i == 3 {
			status = model.StatusDown
		}
		batch = append(batch, model.Heartbeat{
			DeviceID: a.ID, Status: status, LatencyMS: 4,
			CheckedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	for i := 0; i < 2; i++ {
		batch = append(batch, model.Heartbeat{
			DeviceID: b.ID, Status: model.StatusDown, LatencyMS: 3000,
			CheckedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	if err := s.InsertHeartbeats(ctx, batch); err != nil {
		t.Fatal(err)
	}

	got, err := s.RecentChecks(ctx, []int64{a.ID, b.ID}, 4)
	if err != nil {
		t.Fatal(err)
	}

	// The most recent four, in the order they happened: a strip drawn from
	// these reads left to right like time does.
	wantA := []model.Status{
		model.StatusDown, model.StatusDown, model.StatusUp, model.StatusUp,
	}
	if len(got[a.ID]) != len(wantA) {
		t.Fatalf("device a: %v, want %v", got[a.ID], wantA)
	}
	for i := range wantA {
		if got[a.ID][i] != wantA[i] {
			t.Errorf("device a position %d = %s, want %s", i, got[a.ID][i], wantA[i])
		}
	}

	// The limit is per device, not across the result: b's two must survive a
	// noisier neighbour.
	if len(got[b.ID]) != 2 {
		t.Errorf("device b: %v, want its own two checks", got[b.ID])
	}

	// A device with no history is absent rather than empty-but-present, and
	// the caller treats both the same way.
	if _, ok := got[9999]; ok {
		t.Error("a device with no checks should not appear")
	}
}
