package core

import (
	"context"
	"strconv"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/model"
	"github.com/gkgraphite/device-status-monitor/internal/notify"
	"github.com/gkgraphite/device-status-monitor/internal/probe"
	"github.com/gkgraphite/device-status-monitor/internal/state"
	"github.com/gkgraphite/device-status-monitor/internal/store"
)

// runEvaluator drains probe results and applies the state machine.
//
// Deliberately a single goroutine: every transition, incident and alert
// decision happens here, in order, so there are no locks around device state
// and no way for two probes of the same device to race each other.
func (a *App) runEvaluator(ctx context.Context) {
	// The collapse buffer is flushed from this loop rather than its own
	// goroutine, so the buffer needs no lock and a flush can never interleave
	// with a transition being applied.
	flush := time.NewTicker(2 * time.Second)
	defer flush.Stop()

	for {
		select {
		case <-ctx.Done():
			// Anything still waiting for company goes out now: a pending
			// alert lost to a restart is an outage nobody was told about.
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			a.flushAlerts(flushCtx, time.Now(), true)
			cancel()
			return
		case res := <-a.results:
			a.evaluate(ctx, res)
		case <-flush.C:
			a.flushAlerts(ctx, time.Now(), false)
		}
	}
}

func (a *App) evaluate(ctx context.Context, res probe.Result) {
	eff, ok := a.EffectiveFor(res.DeviceID)
	if !ok {
		// The device was deleted or disabled while its probe was in flight.
		return
	}

	a.mu.Lock()
	snap := a.snaps[res.DeviceID]
	if snap == nil {
		snap = &state.Snapshot{Status: model.StatusUnknown}
		a.snaps[res.DeviceID] = snap
	}
	// Recorded before the transition is applied, and for ignored results too:
	// this is the clock scheduler lag is measured against, and a probe that
	// was cancelled still means the device was reached for.
	a.lastCheck[res.DeviceID] = res.At
	a.mu.Unlock()

	params := state.Params{Reminder: a.reminderInterval(ctx)}
	tr := state.Apply(snap, eff, res, params, time.Now())
	if tr.Ignored {
		return
	}

	status := model.StatusDown
	if res.OK {
		status = model.StatusUp
	}
	a.record(model.Heartbeat{
		DeviceID:  res.DeviceID,
		Status:    status,
		LatencyMS: res.LatencyMS,
		ErrorMsg:  res.ErrMsg(),
		CheckedAt: res.At,
	})
	a.publishHeartbeat(eff, res, status)

	if tr.OpenIncident {
		id, err := a.Store.OpenIncident(ctx, res.DeviceID, tr.IncidentStart, res.At, tr.Cause, tr.SendDown)
		if err != nil {
			a.log.Error("open incident", "device", res.DeviceID, "err", err)
		} else {
			snap.OpenIncidentID = id
			tr.IncidentID = id
		}
		a.log.Warn("device down",
			"device", eff.Name, "addr", eff.Addr, "group", eff.GroupName,
			"since", tr.IncidentStart, "reason", res.ErrMsg())
	}

	if tr.CloseIncident && tr.IncidentID != 0 {
		if err := a.Store.CloseIncident(ctx, tr.IncidentID, res.At, tr.SendRecovery); err != nil {
			a.log.Error("close incident", "device", res.DeviceID, "err", err)
		}
		a.log.Info("device recovered",
			"device", eff.Name, "addr", eff.Addr, "downtime", tr.Downtime.Round(time.Second))
	}

	// Published after the incident id is known and before the mail is queued,
	// so a dashboard sees the transition at the same moment the database does.
	if tr.Changed {
		a.publishTransition(ctx, eff, res, tr)
	}

	if err := a.Store.SaveState(ctx, res.DeviceID, *snap, store.LiveResult{
		CheckedAt:     res.At,
		LatencyMS:     res.LatencyMS,
		ErrMsg:        res.ErrMsg(),
		StatusChanged: tr.Changed,
	}); err != nil && ctx.Err() == nil {
		a.log.Error("save device state", "device", res.DeviceID, "err", err)
	}

	a.dispatch(ctx, eff, res, snap, tr)
}

// dispatch queues whatever mail the transition calls for.
func (a *App) dispatch(ctx context.Context, eff model.Effective, res probe.Result,
	snap *state.Snapshot, tr state.Transition) {

	if !tr.SendDown && !tr.SendRecovery && !tr.SendReminder {
		return
	}
	if len(eff.Recipients) == 0 {
		a.log.Warn("alert suppressed: no recipients configured",
			"device", eff.Name, "group", eff.GroupName)
		return
	}

	// Prefer the transition's id: on recovery the snapshot has already let go
	// of the incident this mail is about.
	var incidentID *int64
	if id := tr.IncidentID; id != 0 {
		incidentID = &id
	} else if snap.OpenIncidentID != 0 {
		id := snap.OpenIncidentID
		incidentID = &id
	}

	ev := notify.Event{
		Eff:        eff,
		Class:      res.Class,
		ErrMsg:     res.ErrMsg(),
		FirstFail:  tr.IncidentStart,
		DetectedAt: res.At,
		Failures:   snap.ConsecutiveFailures,
		LatencyMS:  res.LatencyMS,
	}

	switch {
	case tr.SendDown:
		// Parked rather than sent: if two more devices in this group fail
		// within the collapse window, all three become one mail. See
		// digest.go for why that matters more than the seconds it costs.
		a.queueAlert(ctx, pendingAlert{
			eff: eff, kind: model.AlertDown, event: ev,
			incidentID: incidentID, queuedAt: time.Now(),
		})
		if incidentID != nil {
			// Marked as alerted at the moment the decision is made, not when
			// the mail goes out: this is what stops a restart mid-collapse
			// from alerting a second time for the same outage.
			if err := a.Store.MarkIncidentAlerted(ctx, *incidentID, time.Now()); err != nil {
				a.log.Error("mark incident alerted", "incident", *incidentID, "err", err)
			}
		}

	case tr.SendRecovery:
		ev.RecoveredAt = res.At
		ev.Downtime = tr.Downtime
		if up, err := a.Store.Uptime24h(ctx, eff.DeviceID); err == nil {
			ev.Uptime24h = up
		}
		a.queueAlert(ctx, pendingAlert{
			eff: eff, kind: model.AlertRecovery, event: ev,
			incidentID: incidentID, queuedAt: time.Now(),
		})

	case tr.SendReminder:
		// Reminders never collapse. One is already a summary of a state that
		// has not changed, and batching them would mean a mail about several
		// unrelated ongoing outages that nobody asked for.
		ev.FirstFail = snap.OpenIncidentStart
		openFor := time.Since(snap.OpenIncidentStart)
		subject, text, htmlBody := notify.ReminderAlert(ev, openFor)
		a.send(ctx, model.AlertDown, incidentID, eff.Recipients, subject, text, htmlBody)
	}
}

// send queues one mail, subject to the hourly cap.
//
// Every outbound alert goes through here, which is the only way a rate limit
// can be honest: a limiter that individual and digest paths could each bypass
// would not be a limit.
func (a *App) send(ctx context.Context, kind model.AlertKind, incidentID *int64,
	recipients []string, subject, text, htmlBody string) {

	if len(recipients) == 0 {
		a.log.Warn("alert suppressed: no recipients configured", "subject", subject)
		return
	}
	if !a.allowMail(ctx, recipients) {
		return
	}

	if _, err := notify.Enqueue(ctx, a.Store, kind, incidentID, recipients,
		subject, text, htmlBody); err != nil {
		a.log.Error("queue alert", "kind", kind, "subject", subject, "err", err)
		return
	}
	a.log.Info("alert queued", "kind", kind, "subject", subject, "recipients", len(recipients))
}

// allowMail enforces the hourly cap.
//
// When the cap is reached, one notice goes out saying so and the rest is
// withheld. Alerts are never lost by this: the incidents are in the database
// and on the dashboard either way, and mail that stops without explanation is
// worse than mail that says it has stopped — silence reads as "all clear".
func (a *App) allowMail(ctx context.Context, recipients []string) bool {
	policy := a.alertPolicy(ctx)
	if policy.MaxPerHour <= 0 {
		return true
	}

	window := time.Hour
	sent, err := a.Store.AlertsSentSince(ctx, time.Now().Add(-window))
	if err != nil {
		// Failing open: a database problem must not also silence alerting.
		if ctx.Err() == nil {
			a.log.Warn("count recent alerts, allowing mail", "err", err)
		}
		return true
	}
	if sent < int64(policy.MaxPerHour) {
		return true
	}

	// The notice itself is rate limited to once per window, and counts against
	// nothing — otherwise hitting the cap would generate a mail per attempt.
	if time.Since(a.throttledAt) < window {
		a.log.Warn("alert mail withheld: hourly cap reached",
			"cap", policy.MaxPerHour, "sent", sent)
		return false
	}
	a.throttledAt = time.Now()

	subject, text, htmlBody := notify.Throttled(int(sent), window)
	if _, err := notify.Enqueue(ctx, a.Store, model.AlertDigest, nil, recipients,
		subject, text, htmlBody); err != nil {
		a.log.Error("queue throttle notice", "err", err)
	}
	a.log.Error("alert mail paused: hourly cap reached",
		"cap", policy.MaxPerHour, "sent", sent)
	return false
}

// reminderInterval reads alert.reminder_sec. A bad value means "off" rather
// than an error, because this runs on every probe.
func (a *App) reminderInterval(ctx context.Context) time.Duration {
	v, err := a.Store.Setting(ctx, store.KeyAlertReminderSec)
	if err != nil || v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// CheckNow probes a device immediately and returns the result without touching
// its state or history.
func (a *App) CheckNow(ctx context.Context, deviceID int64) (probe.Result, bool) {
	eff, ok := a.EffectiveFor(deviceID)
	if !ok {
		return probe.Result{}, false
	}
	return a.Scheduler.CheckNow(ctx, eff), true
}
