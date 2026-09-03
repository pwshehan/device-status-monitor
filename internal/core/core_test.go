package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/api"
	"github.com/gkgraphite/device-status-monitor/internal/appdir"
	"github.com/gkgraphite/device-status-monitor/internal/model"
	"github.com/gkgraphite/device-status-monitor/internal/notify"
	"github.com/gkgraphite/device-status-monitor/internal/probe"
	"github.com/gkgraphite/device-status-monitor/internal/store"
)

// quietLog keeps test output readable; raise the level when debugging a failure.
func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// flakyProber answers according to a flag the test flips.
type flakyProber struct{ up atomic.Bool }

func (p *flakyProber) Probe(_ context.Context, id int64, _ string, _ time.Duration) probe.Result {
	if p.up.Load() {
		return probe.Result{DeviceID: id, OK: true, LatencyMS: 4, Class: probe.ClassOK, At: time.Now()}
	}
	return probe.Result{
		DeviceID: id, Class: probe.ClassTimeout, LatencyMS: 2000,
		Err: errors.New("i/o timeout"), At: time.Now(),
	}
}

// capturingSender records what would have been delivered.
type capturingSender struct {
	mu   sync.Mutex
	sent []notify.Message
}

func (c *capturingSender) Send(_ context.Context, _ notify.Config, m notify.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return nil
}

func (c *capturingSender) subjects() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.sent))
	for _, m := range c.sent {
		out = append(out, m.Subject)
	}
	return out
}

func (c *capturingSender) messages() []notify.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]notify.Message, len(c.sent))
	copy(out, c.sent)
	return out
}

// harness prepares a data directory with usable SMTP settings and a 1 s global
// probe interval, so an end-to-end run finishes in seconds.
func harness(t *testing.T) (appdir.Dirs, *store.Store) {
	t.Helper()
	dirs := appdir.Dirs{Root: t.TempDir(), Dev: true}
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	st, err := store.Open(ctx, dirs.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SeedSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSettings(ctx, map[string]string{
		store.KeyDefaultInterval: "1",
		store.KeySMTPHost:        "127.0.0.1",
		store.KeySMTPPort:        "2525",
		store.KeySMTPSecurity:    notify.SecurityNone,
		store.KeySMTPFrom:        "monitor@example.com",
		store.KeyAlertRecipients: "ops@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	return dirs, st
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// bringUp waits until the device has been seen working, which is the
// precondition for any DOWN alert: the state machine will not claim a device
// "went down" if it was never up (see state.Apply).
func bringUp(t *testing.T, app *App, deviceID int64) {
	t.Helper()
	waitFor(t, "the device to be seen UP", 10*time.Second, func() bool {
		d, err := app.Store.GetDevice(context.Background(), deviceID)
		return err == nil && d.Status == model.StatusUp
	})
}

// TestEngineEndToEnd is the Phase 1 acceptance check: probe, trip the state
// machine, open an incident, queue and deliver a DOWN mail, then recover and
// deliver a RECOVERY mail carrying the true downtime.
func TestEngineEndToEnd(t *testing.T) {
	ctx := context.Background()
	dirs, st := harness(t)

	g, err := st.CreateGroup(ctx, model.Group{Name: "Head Office", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.CreateDevice(ctx, model.Device{
		Name: "Core switch", IPAddress: "10.0.0.1", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: true,
		FailureThreshold: intptr(1), RecoveryThreshold: intptr(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	prober := &flakyProber{}
	prober.up.Store(true) // a device must be seen working before it can go down
	sender := &capturingSender{}

	app, err := Start(ctx, Options{
		Dirs: dirs, Log: quietLog(),
		Prober: prober, Sender: sender,
		RefreshInterval:  200 * time.Millisecond,
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer app.Shutdown(5 * time.Second)

	bringUp(t, app, dev.ID)

	// --- goes down ---
	prober.up.Store(false)
	waitFor(t, "a DOWN mail", 10*time.Second, func() bool {
		for _, s := range sender.subjects() {
			if len(s) > 6 && s[:6] == "[DOWN]" {
				return true
			}
		}
		return false
	})

	inc, err := app.Store.OpenIncidentFor(ctx, dev.ID)
	if err != nil {
		t.Fatalf("expected an open incident: %v", err)
	}
	if !inc.AlertSent {
		t.Error("incident should record that the alert went out")
	}

	got, err := app.Store.GetDevice(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusDown {
		t.Errorf("device status = %s, want DOWN", got.Status)
	}
	if got.LastError == "" {
		t.Error("want the dial error recorded on the device")
	}

	// --- comes back ---
	time.Sleep(1100 * time.Millisecond) // let some downtime accrue
	prober.up.Store(true)

	waitFor(t, "an UP mail", 10*time.Second, func() bool {
		for _, s := range sender.subjects() {
			if len(s) > 4 && s[:4] == "[UP]" {
				return true
			}
		}
		return false
	})

	if _, err := app.Store.OpenIncidentFor(ctx, dev.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("incident should be closed, got %v", err)
	}
	log, err := app.Store.Incidents(ctx, dev.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 {
		t.Fatalf("incident log has %d rows, want 1", len(log))
	}
	if log[0].DurationSec == nil || *log[0].DurationSec < 1 {
		t.Errorf("duration = %v, want at least 1s of measured downtime", log[0].DurationSec)
	}
	if !log[0].RecoverySent {
		t.Error("recovery should be recorded on the incident")
	}

	// Heartbeats for both outcomes must have been written.
	hbs, err := app.Store.Heartbeats(ctx, dev.ID, time.Now().Add(-time.Hour), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var ups, downs int
	for _, h := range hbs {
		if h.Status == model.StatusUp {
			ups++
		} else {
			downs++
		}
	}
	if ups == 0 || downs == 0 {
		t.Errorf("heartbeats: %d up, %d down — want both", ups, downs)
	}
}

// TestGroupRecipientsRouteTheAlert covers the main reason to want groups at all.
func TestGroupRecipientsRouteTheAlert(t *testing.T) {
	ctx := context.Background()
	dirs, st := harness(t)

	g, err := st.CreateGroup(ctx, model.Group{
		Name: "Warehouse", Notify: true, Recipients: strptr("warehouse@example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.CreateDevice(ctx, model.Device{
		Name: "Floor 2 switch", IPAddress: "10.0.7.3", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: true,
		FailureThreshold: intptr(1), RecoveryThreshold: intptr(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	prober := &flakyProber{}
	prober.up.Store(true)
	sender := &capturingSender{}

	app, err := Start(ctx, Options{
		Dirs: dirs, Log: quietLog(),
		Prober: prober, Sender: sender,
		RefreshInterval:  200 * time.Millisecond,
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(5 * time.Second)

	bringUp(t, app, dev.ID)
	prober.up.Store(false)

	waitFor(t, "a routed alert", 10*time.Second, func() bool { return len(sender.messages()) > 0 })

	m := sender.messages()[0]
	if len(m.To) != 1 || m.To[0] != "warehouse@example.com" {
		t.Errorf("recipients = %v, want the group's warehouse@example.com", m.To)
	}
}

// TestGroupPauseSuppressesEverything checks that a maintenance window stops the
// probing, not just the mail.
func TestGroupPauseSuppressesEverything(t *testing.T) {
	ctx := context.Background()
	dirs, st := harness(t)

	until := time.Now().Add(time.Hour)
	g, err := st.CreateGroup(ctx, model.Group{Name: "Warehouse", Notify: true, PausedUntil: &until})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.CreateDevice(ctx, model.Device{
		Name: "NAS", IPAddress: "10.0.9.5", Port: 445,
		GroupID: &g.ID, Enabled: true, Notify: true, FailureThreshold: intptr(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	sender := &capturingSender{}
	app, err := Start(ctx, Options{
		Dirs: dirs, Log: quietLog(),
		Prober: &flakyProber{}, Sender: sender,
		RefreshInterval:  200 * time.Millisecond,
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(5 * time.Second)

	time.Sleep(1500 * time.Millisecond)

	if n := app.Scheduler.Running(); n != 0 {
		t.Errorf("running workers = %d, want 0 while the group is paused", n)
	}
	if msgs := sender.messages(); len(msgs) != 0 {
		t.Errorf("sent %d mails during a maintenance window", len(msgs))
	}
	hbs, err := app.Store.Heartbeats(ctx, dev.ID, time.Now().Add(-time.Hour), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hbs) != 0 {
		t.Errorf("wrote %d heartbeats for a paused device", len(hbs))
	}
	got, err := app.Store.GetDevice(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusUnknown {
		t.Errorf("status = %s, want UNKNOWN — a paused device's state must not move", got.Status)
	}
}

// TestGroupIntervalChangeReloadsMembers checks the reload path a group edit
// takes: the same channel a device edit uses.
func TestGroupIntervalChangeReloadsMembers(t *testing.T) {
	ctx := context.Background()
	dirs, st := harness(t)

	g, err := st.CreateGroup(ctx, model.Group{Name: "Head Office", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.CreateDevice(ctx, model.Device{
		Name: "switch", IPAddress: "10.0.0.1", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	prober := &flakyProber{}
	prober.up.Store(true)

	app, err := Start(ctx, Options{
		Dirs: dirs, Log: quietLog(),
		Prober: prober, Sender: &capturingSender{},
		RefreshInterval:  10 * time.Second, // long, so only an explicit Reload counts
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(5 * time.Second)

	eff, ok := app.EffectiveFor(dev.ID)
	if !ok {
		t.Fatal("device not scheduled")
	}
	if eff.Interval != time.Second || eff.Source["interval"] != "global" {
		t.Fatalf("initial interval = %s from %s, want 1s from global",
			eff.Interval, eff.Source["interval"])
	}

	// Change the group, then reload — as the API handler will.
	g.CheckIntervalSec = intptr(7)
	if _, err := app.Store.UpdateGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	app.Reload()

	waitFor(t, "the member to pick up the group's interval", 5*time.Second, func() bool {
		e, ok := app.EffectiveFor(dev.ID)
		return ok && e.Interval == 7*time.Second
	})

	eff, _ = app.EffectiveFor(dev.ID)
	if eff.Source["interval"] != "group:Head Office" {
		t.Errorf("source = %q, want group:Head Office", eff.Source["interval"])
	}
}

// TestRestartResumesWithoutRealerting is the restart-mid-outage case.
func TestRestartResumesWithoutRealerting(t *testing.T) {
	ctx := context.Background()
	dirs, st := harness(t)

	dev, err := st.CreateDevice(ctx, model.Device{
		Name: "switch", IPAddress: "10.0.0.1", Port: 22,
		Enabled: true, Notify: true,
		FailureThreshold: intptr(1), RecoveryThreshold: intptr(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	prober := &flakyProber{}
	prober.up.Store(true)

	opts := func(s notify.Sender) Options {
		return Options{
			Dirs: dirs, Log: quietLog(),
			Prober: prober, Sender: s,
			RefreshInterval:  200 * time.Millisecond,
			FlushInterval:    100 * time.Millisecond,
			NotifierInterval: 100 * time.Millisecond,
		}
	}

	// First run: comes up, goes down, alerts once.
	first := &capturingSender{}
	app1, err := Start(ctx, opts(first))
	if err != nil {
		t.Fatal(err)
	}
	bringUp(t, app1, dev.ID)
	prober.up.Store(false)
	waitFor(t, "the first DOWN mail", 10*time.Second, func() bool { return len(first.messages()) > 0 })
	if err := app1.Shutdown(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if n := len(first.messages()); n != 1 {
		t.Fatalf("first run sent %d mails, want exactly 1", n)
	}

	// Second run against the same database, still down.
	second := &capturingSender{}
	app2, err := Start(ctx, opts(second))
	if err != nil {
		t.Fatal(err)
	}
	defer app2.Shutdown(5 * time.Second)

	time.Sleep(2 * time.Second)
	if n := len(second.messages()); n != 0 {
		t.Errorf("restart re-alerted %d times for an outage already reported: %v",
			n, second.subjects())
	}

	// And the original incident is still the open one, not a second episode.
	log, err := app2.Store.Incidents(ctx, dev.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 {
		t.Errorf("incident count = %d, want 1 episode across the restart", len(log))
	}
}

// TestEnginePublishesTransitionsToTheHub covers the wiring between the
// evaluator and the API's event hub: the evaluator is the only goroutine that
// knows a transition happened, so if it does not publish, no dashboard badge
// ever moves however healthy the rest of the engine is.
func TestEnginePublishesTransitionsToTheHub(t *testing.T) {
	ctx := context.Background()
	dirs, st := harness(t)

	g, err := st.CreateGroup(ctx, model.Group{Name: "Head Office", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.CreateDevice(ctx, model.Device{
		Name: "core switch", IPAddress: "10.0.0.1", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: true,
		FailureThreshold: intptr(1), RecoveryThreshold: intptr(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	prober := &flakyProber{}
	prober.up.Store(true)

	app, err := Start(ctx, Options{
		Dirs: dirs, Log: quietLog(),
		Prober: prober, Sender: &capturingSender{},
		RefreshInterval:  200 * time.Millisecond,
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(5 * time.Second)

	events, unsubscribe := app.Hub.Subscribe(api.EventDeviceStatus, api.EventIncident,
		api.EventGroupStatus)
	defer unsubscribe()

	// Collect until the device has been seen up and then down: one transition
	// each way, an incident opening, and the group tally that goes with it.
	var (
		statuses  []api.DeviceStatusEvent
		incidents []api.IncidentEvent
		groups    []api.GroupStatusEvent
	)
	deadline := time.After(15 * time.Second)
	flipped := false

	for {
		select {
		case <-deadline:
			t.Fatalf("timed out; statuses=%+v incidents=%+v", statuses, incidents)
		case ev := <-events:
			switch data := ev.Data.(type) {
			case api.DeviceStatusEvent:
				statuses = append(statuses, data)
				if data.Status == model.StatusUp && !flipped {
					flipped = true
					prober.up.Store(false)
				}
			case api.IncidentEvent:
				incidents = append(incidents, data)
			case api.GroupStatusEvent:
				groups = append(groups, data)
			}
		}
		if len(statuses) >= 2 && len(incidents) >= 1 {
			break
		}
	}

	first, second := statuses[0], statuses[1]
	if first.DeviceID != dev.ID || first.Status != model.StatusUp ||
		first.PreviousStatus != model.StatusUnknown {
		t.Errorf("first event = %+v, want UNKNOWN -> UP for device %d", first, dev.ID)
	}
	if second.Status != model.StatusDown || second.PreviousStatus != model.StatusUp {
		t.Errorf("second event = %+v, want UP -> DOWN", second)
	}
	if second.Error == "" || second.GroupName != "Head Office" {
		t.Errorf("DOWN event should carry the dial error and the group: %+v", second)
	}

	inc := incidents[0]
	if inc.State != "opened" || inc.ID == 0 || inc.DeviceName != "core switch" {
		t.Errorf("incident event = %+v, want an opened incident with its id", inc)
	}
	// The id has to be on the event: without it the UI cannot tie the
	// incident to the transition that produced it.
	if second.IncidentID == nil || *second.IncidentID != inc.ID {
		t.Errorf("device_status incident_id = %v, want %d", second.IncidentID, inc.ID)
	}
	if len(groups) == 0 {
		t.Error("no group_status event: section headers would never update")
	}

	// Nothing is overdue in a run this short, and a device probed on time must
	// not report lag.
	if lag := app.SchedulerLagMS(); lag > 5000 {
		t.Errorf("scheduler lag = %dms, want a small figure for an on-time device", lag)
	}
}

// TestEngineServesTheAPI covers the wiring core owns rather than the handlers
// themselves: generating the token file, binding the listener, authenticating
// with the token it just wrote, and closing the listener on shutdown.
//
// Every other API test drives the handler through httptest with a stub engine,
// which never exercises any of that.
func TestEngineServesTheAPI(t *testing.T) {
	ctx := context.Background()
	dirs, st := harness(t)
	if _, err := st.CreateDevice(ctx, model.Device{
		Name: "switch", IPAddress: "10.0.0.1", Port: 22, Enabled: true, Notify: true,
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	prober := &flakyProber{}
	prober.up.Store(true)

	app, err := Start(ctx, Options{
		Dirs: dirs, Log: quietLog(), Version: "test",
		// Port 0: the OS picks one, so a developer's running service does not
		// make the test suite fail.
		APIAddr: "127.0.0.1:0",
		Prober:  prober, Sender: &capturingSender{},
		RefreshInterval:  200 * time.Millisecond,
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	base := "http://" + app.API.Addr()

	token, err := os.ReadFile(dirs.TokenFile())
	if err != nil {
		t.Fatalf("the service did not write a token file: %v", err)
	}
	bearer := "Bearer " + strings.TrimSpace(string(token))

	// Health, unauthenticated, is how the GUI tells "not running" from
	// "token wrong".
	res, err := http.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	var health struct {
		OK        bool   `json:"ok"`
		Version   string `json:"version"`
		Devices   int    `json:"devices"`
		Monitored int    `json:"monitored"`
	}
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if !health.OK || health.Version != "test" || health.Devices != 1 {
		t.Errorf("health = %+v, want the real engine's figures", health)
	}

	// An authenticated route with the token the service generated.
	req, err := http.NewRequest(http.MethodGet, base+"/api/devices", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", bearer)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/devices: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/devices = %d, want 200 with the generated token", res.StatusCode)
	}

	if err := app.Shutdown(5 * time.Second); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	// The listener must be gone, or restarting the service fails with
	// "address already in use" and the SCM reports a service that will not
	// start.
	if _, err := http.Get(base + "/api/health"); err == nil {
		t.Error("the API still answers after shutdown")
	}
}
