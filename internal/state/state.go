// Package state holds the device state machine. It is deliberately pure: no
// database, no clock, no network. Everything it needs arrives as arguments,
// which is what makes the transition rules cheap to test exhaustively.
package state

import (
	"time"

	"github.com/pwshehan/local-device-monitor/internal/model"
	"github.com/pwshehan/local-device-monitor/internal/probe"
)

// Snapshot is a device's live state as the evaluator holds it in memory. It is
// loaded from the database at startup, so a service restart mid-outage resumes
// rather than re-alerting.
type Snapshot struct {
	Status               model.Status
	ConsecutiveFailures  int
	ConsecutiveSuccesses int

	// FirstFailureAt is the start of the current failure streak, zero when the
	// last probe succeeded. An incident starts here, not at the probe that
	// tripped the threshold, so reported downtime is the real downtime.
	FirstFailureAt time.Time

	OpenIncidentID    int64 // 0 = no open incident
	OpenIncidentStart time.Time
	AlertSent         bool      // a DOWN mail went out for the open incident
	LastAlertAt       time.Time // for reminder re-sends
}

// Transition is what the evaluator must persist and dispatch after one probe.
// The state machine decides; the caller writes.
type Transition struct {
	Ignored bool // shutdown, not a data point: record nothing

	From, To model.Status
	Changed  bool

	OpenIncident  bool // insert an incident starting at IncidentStart
	CloseIncident bool // resolve IncidentID
	IncidentStart time.Time
	Downtime      time.Duration

	// IncidentID is the incident this transition acts on. Carried on the
	// transition rather than read back off the snapshot, because Apply clears
	// the snapshot's open incident as part of recovering.
	IncidentID int64

	SendDown     bool
	SendRecovery bool
	SendReminder bool
	Cause        string
}

// Params are the knobs Apply needs that are not per-probe.
type Params struct {
	Reminder time.Duration // 0 = no reminder while an incident stays open
}

// Apply folds one probe result into s and reports what changed.
//
// The rules, in one place:
//
//   - A cancelled probe is a shutdown artefact and is ignored entirely.
//   - Failures accumulate; at FailureThreshold the device goes DOWN and an
//     incident opens, backdated to the first failure of the streak.
//   - Successes accumulate; at RecoveryThreshold a DOWN device goes UP and the
//     incident closes with its true duration.
//   - UNKNOWN is silent in both directions. A device we have never seen working
//     cannot be said to have "gone down", and must not send a "recovered" mail
//     the first time it answers. Its downtime is still recorded.
func Apply(s *Snapshot, eff model.Effective, res probe.Result, p Params, now time.Time) Transition {
	if res.Class == probe.ClassCanceled {
		return Transition{Ignored: true}
	}

	t := Transition{From: s.Status, To: s.Status}

	if res.OK {
		s.ConsecutiveSuccesses++
		s.ConsecutiveFailures = 0
		s.FirstFailureAt = time.Time{}

		if s.Status == model.StatusDown && s.ConsecutiveSuccesses >= eff.RecoveryThreshold {
			t.To, t.Changed = model.StatusUp, true
			if s.OpenIncidentID != 0 {
				t.CloseIncident = true
				t.IncidentID = s.OpenIncidentID
				t.Downtime = now.Sub(s.OpenIncidentStart)
				t.IncidentStart = s.OpenIncidentStart
				// Only announce a recovery we announced the outage for.
				t.SendRecovery = eff.Notify && s.AlertSent
			}
			s.Status = model.StatusUp
			s.OpenIncidentID, s.OpenIncidentStart = 0, time.Time{}
			s.AlertSent, s.LastAlertAt = false, time.Time{}
			return t
		}

		if s.Status == model.StatusUnknown {
			// First successful check: adopt UP silently.
			t.To, t.Changed = model.StatusUp, true
			s.Status = model.StatusUp
		}
		return t
	}

	// Failure.
	s.ConsecutiveFailures++
	s.ConsecutiveSuccesses = 0
	if s.FirstFailureAt.IsZero() {
		s.FirstFailureAt = res.At
	}
	t.Cause = res.ErrMsg()

	if s.Status != model.StatusDown && s.ConsecutiveFailures >= eff.FailureThreshold {
		wasUnknown := s.Status == model.StatusUnknown

		t.To, t.Changed = model.StatusDown, true
		t.OpenIncident = true
		t.IncidentStart = s.FirstFailureAt
		// A device that has never been seen up gets its downtime recorded but
		// no mail; the dashboard is the feedback channel for a device somebody
		// just added with the wrong port.
		t.SendDown = eff.Notify && !wasUnknown

		s.Status = model.StatusDown
		s.OpenIncidentStart = s.FirstFailureAt
		s.AlertSent = t.SendDown
		if t.SendDown {
			s.LastAlertAt = now
		}
		return t
	}

	// Already down: consider a reminder.
	if s.Status == model.StatusDown && p.Reminder > 0 && s.AlertSent &&
		!s.LastAlertAt.IsZero() && now.Sub(s.LastAlertAt) >= p.Reminder {
		if eff.Notify {
			t.SendReminder = true
			t.IncidentID = s.OpenIncidentID
			s.LastAlertAt = now
		}
	}
	return t
}
