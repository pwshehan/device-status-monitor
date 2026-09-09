package state

import (
	"errors"
	"testing"
	"time"

	"github.com/pwshehan/local-device-monitor/internal/model"
	"github.com/pwshehan/local-device-monitor/internal/probe"
)

var base = time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)

func eff(notify bool) model.Effective {
	return model.Effective{
		DeviceID:          1,
		Name:              "Core switch",
		Addr:              "10.0.0.1:22",
		Interval:          30 * time.Second,
		Timeout:           3 * time.Second,
		FailureThreshold:  3,
		RecoveryThreshold: 1,
		Notify:            notify,
	}
}

func ok(at time.Time) probe.Result {
	return probe.Result{DeviceID: 1, OK: true, LatencyMS: 2, Class: probe.ClassOK, At: at}
}

func fail(at time.Time) probe.Result {
	return probe.Result{DeviceID: 1, Class: probe.ClassTimeout, LatencyMS: 3000,
		Err: errors.New("i/o timeout"), At: at}
}

// tick advances by the check interval.
func tick(n int) time.Time { return base.Add(time.Duration(n) * 30 * time.Second) }

func TestFirstSuccessAdoptsUpSilently(t *testing.T) {
	s := &Snapshot{Status: model.StatusUnknown}
	tr := Apply(s, eff(true), ok(base), Params{}, base)

	if !tr.Changed || tr.To != model.StatusUp {
		t.Fatalf("want change to UP, got %+v", tr)
	}
	if tr.SendRecovery || tr.SendDown {
		t.Error("adopting UP from UNKNOWN must not send mail")
	}
}

func TestThreeStrikesThenDown(t *testing.T) {
	s := &Snapshot{Status: model.StatusUp}

	// Two failures inside the threshold: recorded, but no state change, no mail.
	for i := 1; i <= 2; i++ {
		tr := Apply(s, eff(true), fail(tick(i)), Params{}, tick(i))
		if tr.Changed {
			t.Fatalf("failure %d changed state early: %+v", i, tr)
		}
		if tr.SendDown {
			t.Fatalf("failure %d sent mail early", i)
		}
		if s.Status != model.StatusUp {
			t.Fatalf("failure %d: status = %s", i, s.Status)
		}
	}

	// The third trips it.
	tr := Apply(s, eff(true), fail(tick(3)), Params{}, tick(3))
	if !tr.Changed || tr.To != model.StatusDown {
		t.Fatalf("want DOWN on the third failure, got %+v", tr)
	}
	if !tr.OpenIncident || !tr.SendDown {
		t.Fatalf("want an incident and a DOWN mail, got %+v", tr)
	}
	// Backdated to the first failure of the streak, not the third probe.
	if !tr.IncidentStart.Equal(tick(1)) {
		t.Errorf("incident start = %v, want %v (the first failure)", tr.IncidentStart, tick(1))
	}
	if tr.Cause == "" {
		t.Error("want the dial error recorded as the cause")
	}
}

func TestFlapInsideThresholdNeverAlerts(t *testing.T) {
	s := &Snapshot{Status: model.StatusUp}
	for i := 1; i <= 20; i++ {
		// Two failures then a success, forever: never reaches three in a row.
		var tr Transition
		switch i % 3 {
		case 0:
			tr = Apply(s, eff(true), ok(tick(i)), Params{}, tick(i))
		default:
			tr = Apply(s, eff(true), fail(tick(i)), Params{}, tick(i))
		}
		if tr.SendDown || tr.OpenIncident {
			t.Fatalf("probe %d alerted on a flap: %+v", i, tr)
		}
	}
	if s.Status != model.StatusUp {
		t.Errorf("status = %s, want UP throughout", s.Status)
	}
}

func TestRecoveryClosesIncidentWithTrueDuration(t *testing.T) {
	s := &Snapshot{Status: model.StatusUp}
	for i := 1; i <= 3; i++ {
		Apply(s, eff(true), fail(tick(i)), Params{}, tick(i))
	}
	if s.OpenIncidentStart != tick(1) {
		t.Fatalf("open incident start = %v", s.OpenIncidentStart)
	}
	s.OpenIncidentID = 42 // as the evaluator would set after the insert

	recoveredAt := tick(10) // 4m30s after the first failure
	tr := Apply(s, eff(true), ok(recoveredAt), Params{}, recoveredAt)

	if !tr.Changed || tr.To != model.StatusUp {
		t.Fatalf("want UP, got %+v", tr)
	}
	if !tr.CloseIncident || !tr.SendRecovery {
		t.Fatalf("want the incident closed and a recovery mail, got %+v", tr)
	}
	// The id must ride on the transition: Apply has already cleared it from the
	// snapshot, so the caller has no other way to know what to close.
	if tr.IncidentID != 42 {
		t.Errorf("tr.IncidentID = %d, want 42", tr.IncidentID)
	}
	if want := recoveredAt.Sub(tick(1)); tr.Downtime != want {
		t.Errorf("downtime = %s, want %s", tr.Downtime, want)
	}
	if s.OpenIncidentID != 0 || s.AlertSent {
		t.Errorf("incident state not cleared: %+v", s)
	}
}

func TestUnknownToDownIsSilentBothWays(t *testing.T) {
	s := &Snapshot{Status: model.StatusUnknown}
	var tr Transition
	for i := 1; i <= 3; i++ {
		tr = Apply(s, eff(true), fail(tick(i)), Params{}, tick(i))
	}
	if !tr.Changed || tr.To != model.StatusDown {
		t.Fatalf("want DOWN, got %+v", tr)
	}
	if !tr.OpenIncident {
		t.Error("downtime should still be recorded for a never-seen-up device")
	}
	if tr.SendDown {
		t.Error("a device that was never up must not send a DOWN mail")
	}

	// ...and its first success must not claim a recovery.
	s.OpenIncidentID = 9
	tr = Apply(s, eff(true), ok(tick(4)), Params{}, tick(4))
	if !tr.CloseIncident {
		t.Error("want the incident closed")
	}
	if tr.SendRecovery {
		t.Error("no DOWN mail was sent, so no RECOVERY mail either")
	}
}

func TestNotifyOffSuppressesBothMails(t *testing.T) {
	s := &Snapshot{Status: model.StatusUp}
	var tr Transition
	for i := 1; i <= 3; i++ {
		tr = Apply(s, eff(false), fail(tick(i)), Params{}, tick(i))
	}
	if !tr.OpenIncident {
		t.Error("notify=0 still records the outage")
	}
	if tr.SendDown {
		t.Error("notify=0 must not mail")
	}

	s.OpenIncidentID = 3
	tr = Apply(s, eff(false), ok(tick(4)), Params{}, tick(4))
	if tr.SendRecovery {
		t.Error("notify=0 must not mail on recovery either")
	}
}

func TestCanceledProbeIsIgnored(t *testing.T) {
	s := &Snapshot{Status: model.StatusUp, ConsecutiveFailures: 2}
	before := *s

	tr := Apply(s, eff(true), probe.Result{Class: probe.ClassCanceled, At: base}, Params{}, base)
	if !tr.Ignored {
		t.Fatal("a cancelled probe must be ignored")
	}
	if *s != before {
		t.Errorf("snapshot mutated on a cancelled probe: %+v -> %+v", before, *s)
	}
}

func TestRestartMidOutageDoesNotDoubleAlert(t *testing.T) {
	// Reloaded from the database: already DOWN, alert already sent.
	s := &Snapshot{
		Status:              model.StatusDown,
		ConsecutiveFailures: 7,
		FirstFailureAt:      tick(1),
		OpenIncidentID:      11,
		OpenIncidentStart:   tick(1),
		AlertSent:           true,
		LastAlertAt:         tick(3),
	}
	for i := 20; i <= 25; i++ {
		tr := Apply(s, eff(true), fail(tick(i)), Params{}, tick(i))
		if tr.SendDown || tr.OpenIncident || tr.Changed {
			t.Fatalf("probe %d re-alerted after a restart: %+v", i, tr)
		}
	}
}

func TestReminderRespectsInterval(t *testing.T) {
	s := &Snapshot{
		Status: model.StatusDown, FirstFailureAt: tick(1), OpenIncidentID: 1,
		OpenIncidentStart: tick(1), AlertSent: true, LastAlertAt: tick(3),
	}
	p := Params{Reminder: 30 * time.Minute}

	// Too soon.
	if tr := Apply(s, eff(true), fail(tick(4)), p, tick(4)); tr.SendReminder {
		t.Error("reminder fired before the interval elapsed")
	}
	// Past the interval.
	late := tick(3).Add(31 * time.Minute)
	if tr := Apply(s, eff(true), fail(late), p, late); !tr.SendReminder {
		t.Error("want a reminder once the interval elapsed")
	}
	// And it resets, so it does not fire every probe from then on.
	if tr := Apply(s, eff(true), fail(late.Add(time.Minute)), p, late.Add(time.Minute)); tr.SendReminder {
		t.Error("reminder should not fire on every subsequent probe")
	}
	// Disabled by default.
	if tr := Apply(s, eff(true), fail(late.Add(time.Hour)), Params{}, late.Add(time.Hour)); tr.SendReminder {
		t.Error("reminder must be off when Reminder is 0")
	}
}

func TestHigherRecoveryThresholdWaits(t *testing.T) {
	e := eff(true)
	e.RecoveryThreshold = 3

	s := &Snapshot{Status: model.StatusDown, OpenIncidentID: 5, OpenIncidentStart: tick(1), AlertSent: true}
	for i := 1; i <= 2; i++ {
		if tr := Apply(s, e, ok(tick(i)), Params{}, tick(i)); tr.Changed {
			t.Fatalf("success %d recovered too early: %+v", i, tr)
		}
	}
	if tr := Apply(s, e, ok(tick(3)), Params{}, tick(3)); !tr.Changed || tr.To != model.StatusUp {
		t.Fatalf("want UP on the third success, got %+v", tr)
	}
}
