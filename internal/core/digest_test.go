package core

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/appdir"
	"github.com/pwshehan/device-status-monitor/internal/model"
	"github.com/pwshehan/device-status-monitor/internal/notify"
	"github.com/pwshehan/device-status-monitor/internal/probe"
	"github.com/pwshehan/device-status-monitor/internal/store"
)

// siteProber fails whichever devices the test names, and answers the rest.
type siteProber struct {
	down atomic.Value // map[int64]bool
}

func (p *siteProber) setDown(ids map[int64]bool) { p.down.Store(ids) }

func (p *siteProber) Probe(_ context.Context, id int64, _ string, _ time.Duration) probe.Result {
	down, _ := p.down.Load().(map[int64]bool)
	if down[id] {
		return probe.Result{
			DeviceID: id, Class: probe.ClassTimeout, LatencyMS: 2000,
			Err: fmt.Errorf("i/o timeout"), At: time.Now(),
		}
	}
	return probe.Result{
		DeviceID: id, OK: true, LatencyMS: 4, Class: probe.ClassOK, At: time.Now(),
	}
}

// siteHarness builds a database with one group of `members` devices, tuned so
// a transition takes about a second rather than a minute.
func siteHarness(t *testing.T, members int, collapseSec string) (appdir.Dirs, *store.Store, model.Group, []model.Device) {
	t.Helper()
	d, st := harness(t)

	ctx := context.Background()
	if err := st.PutSettings(ctx, map[string]string{
		store.KeyAlertCollapseSec: collapseSec,
	}); err != nil {
		t.Fatal(err)
	}

	g, err := st.CreateGroup(ctx, model.Group{
		Name: "Warehouse", Notify: true, Recipients: strptr("warehouse@example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}

	devices := make([]model.Device, 0, members)
	for i := 0; i < members; i++ {
		dev, err := st.CreateDevice(ctx, model.Device{
			Name:      fmt.Sprintf("wh-%02d", i),
			IPAddress: fmt.Sprintf("10.9.0.%d", i+1),
			Port:      22, GroupID: &g.ID, Enabled: true, Notify: true,
			FailureThreshold: intptr(1), RecoveryThreshold: intptr(1),
		})
		if err != nil {
			t.Fatal(err)
		}
		devices = append(devices, dev)
	}
	return d, st, g, devices
}

func subjects(s *capturingSender) []string { return s.subjects() }

func countWith(subjects []string, prefix string) int {
	n := 0
	for _, s := range subjects {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

// TestSiteOutageCollapsesIntoOneMail is the acceptance check for §7's digest:
// a site going dark is one event, and six mails about it is how people learn
// to filter alerts.
func TestSiteOutageCollapsesIntoOneMail(t *testing.T) {
	ctx := context.Background()
	dataDirs, st, _, devices := siteHarness(t, 6, "2")
	_ = st.Close()

	prober := &siteProber{}
	prober.setDown(map[int64]bool{})
	sender := &capturingSender{}

	app, err := Start(ctx, Options{
		UpdateInterval: -1,
		Dirs:           dataDirs, Log: quietLog(),
		Prober: prober, Sender: sender,
		RefreshInterval:  200 * time.Millisecond,
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 100 * time.Millisecond,
		JanitorInterval:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(5 * time.Second)

	// Every device has to be seen working first: the state machine will not
	// claim a device "went down" if it was never up.
	for _, d := range devices {
		bringUp(t, app, d.ID)
	}

	// The whole site fails at once, as it would if its switch lost power.
	down := map[int64]bool{}
	for _, d := range devices {
		down[d.ID] = true
	}
	prober.setDown(down)

	waitFor(t, "the collapsed site alert", 20*time.Second, func() bool {
		return countWith(subjects(sender), "[DOWN] Warehouse") > 0
	})
	// Give any stragglers a chance to produce a second mail, so this test
	// fails rather than passing by being quick.
	time.Sleep(3 * time.Second)

	got := subjects(sender)
	if n := countWith(got, "[DOWN] Warehouse"); n != 1 {
		t.Errorf("site digests = %d, want exactly 1: %v", n, got)
	}
	if n := countWith(got, "[DOWN] wh-"); n != 0 {
		t.Errorf("individual mails = %d, want 0 — they should have collapsed: %v", n, got)
	}

	var digest notify.Message
	for _, m := range sender.messages() {
		if strings.HasPrefix(m.Subject, "[DOWN] Warehouse") {
			digest = m
		}
	}
	if !strings.Contains(digest.Subject, "6 of 6") {
		t.Errorf("subject = %q, want it to say 6 of 6", digest.Subject)
	}
	// Naming the members is the point: a digest that only counts them tells
	// nobody which rack to walk to.
	for _, d := range devices {
		if !strings.Contains(digest.Text, d.Name) {
			t.Errorf("digest does not name %s:\n%s", d.Name, digest.Text)
		}
	}
	if len(digest.To) != 1 || digest.To[0] != "warehouse@example.com" {
		t.Errorf("recipients = %v, want the group's list", digest.To)
	}

	// And the recovery collapses the same way.
	prober.setDown(map[int64]bool{})
	waitFor(t, "the collapsed recovery", 20*time.Second, func() bool {
		return countWith(subjects(sender), "[UP] Warehouse") > 0
	})
	time.Sleep(3 * time.Second)
	if n := countWith(subjects(sender), "[UP] Warehouse"); n != 1 {
		t.Errorf("recovery digests = %d, want 1: %v", n, subjects(sender))
	}
	if n := countWith(subjects(sender), "[UP] wh-"); n != 0 {
		t.Errorf("individual recoveries = %d, want 0", n)
	}
}

// TestTwoDevicesStillGetTheirOwnMails guards the other side of the threshold:
// a pair failing together is often two devices failing, and a two-line digest
// is worse than two mails.
func TestTwoDevicesStillGetTheirOwnMails(t *testing.T) {
	ctx := context.Background()
	dataDirs, st, _, devices := siteHarness(t, 4, "2")
	_ = st.Close()

	prober := &siteProber{}
	prober.setDown(map[int64]bool{})
	sender := &capturingSender{}

	app, err := Start(ctx, Options{
		UpdateInterval: -1,
		Dirs:           dataDirs, Log: quietLog(),
		Prober: prober, Sender: sender,
		RefreshInterval:  200 * time.Millisecond,
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 100 * time.Millisecond,
		JanitorInterval:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(5 * time.Second)

	for _, d := range devices {
		bringUp(t, app, d.ID)
	}

	prober.setDown(map[int64]bool{devices[0].ID: true, devices[1].ID: true})

	waitFor(t, "two individual alerts", 20*time.Second, func() bool {
		return countWith(subjects(sender), "[DOWN] wh-") >= 2
	})
	time.Sleep(2 * time.Second)

	got := subjects(sender)
	if n := countWith(got, "[DOWN] wh-"); n != 2 {
		t.Errorf("individual mails = %d, want 2: %v", n, got)
	}
	if n := countWith(got, "[DOWN] Warehouse"); n != 0 {
		t.Errorf("digests = %d, want 0 below the threshold: %v", n, got)
	}
}

// TestCollapseCanBeSwitchedOff covers the setting: zero means every device
// alerts on its own, immediately.
func TestCollapseCanBeSwitchedOff(t *testing.T) {
	ctx := context.Background()
	dataDirs, st, _, devices := siteHarness(t, 4, "0")
	_ = st.Close()

	prober := &siteProber{}
	prober.setDown(map[int64]bool{})
	sender := &capturingSender{}

	app, err := Start(ctx, Options{
		UpdateInterval: -1,
		Dirs:           dataDirs, Log: quietLog(),
		Prober: prober, Sender: sender,
		RefreshInterval:  200 * time.Millisecond,
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 100 * time.Millisecond,
		JanitorInterval:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(5 * time.Second)

	for _, d := range devices {
		bringUp(t, app, d.ID)
	}

	down := map[int64]bool{}
	for _, d := range devices {
		down[d.ID] = true
	}
	prober.setDown(down)

	waitFor(t, "four individual alerts", 20*time.Second, func() bool {
		return countWith(subjects(sender), "[DOWN] wh-") >= 4
	})
	if n := countWith(subjects(sender), "[DOWN] Warehouse"); n != 0 {
		t.Errorf("digests = %d, want 0 with collapsing off", n)
	}
}

// TestHourlyCapPausesMailAndSaysSo covers the rate limit. Alerts are never
// dropped silently: mail that stops without explanation reads as "all clear",
// which is the failure this whole system exists to prevent.
func TestHourlyCapPausesMailAndSaysSo(t *testing.T) {
	ctx := context.Background()
	dataDirs, st, _, devices := siteHarness(t, 4, "0")

	// A cap of two, so the third alert trips it.
	if err := st.PutSettings(ctx, map[string]string{store.KeyAlertMaxPerHour: "2"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	prober := &siteProber{}
	prober.setDown(map[int64]bool{})
	sender := &capturingSender{}

	app, err := Start(ctx, Options{
		UpdateInterval: -1,
		Dirs:           dataDirs, Log: quietLog(),
		Prober: prober, Sender: sender,
		RefreshInterval:  200 * time.Millisecond,
		FlushInterval:    100 * time.Millisecond,
		NotifierInterval: 100 * time.Millisecond,
		JanitorInterval:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(5 * time.Second)

	for _, d := range devices {
		bringUp(t, app, d.ID)
	}

	down := map[int64]bool{}
	for _, d := range devices {
		down[d.ID] = true
	}
	prober.setDown(down)

	waitFor(t, "the pause notice", 20*time.Second, func() bool {
		return countWith(subjects(sender), "[DOWN] Alert mail paused") > 0
	})
	time.Sleep(2 * time.Second)

	got := subjects(sender)
	if n := countWith(got, "[DOWN] wh-"); n > 2 {
		t.Errorf("%d device mails went out past a cap of 2: %v", n, got)
	}
	if n := countWith(got, "[DOWN] Alert mail paused"); n != 1 {
		t.Errorf("pause notices = %d, want exactly 1: %v", n, got)
	}

	// The incidents are all still recorded — only the mail was withheld, and
	// that is what makes the cap safe.
	open, err := app.Store.OpenIncidents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != len(devices) {
		t.Errorf("open incidents = %d, want %d: the cap must not lose state",
			len(open), len(devices))
	}
}
