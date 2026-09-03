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
	for {
		select {
		case <-ctx.Done():
			return
		case res := <-a.results:
			a.evaluate(ctx, res)
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
		subject, text, htmlBody := notify.DownAlert(ev)
		a.queue(ctx, model.AlertDown, incidentID, eff, subject, text, htmlBody)
		if incidentID != nil {
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
		subject, text, htmlBody := notify.RecoveryAlert(ev)
		a.queue(ctx, model.AlertRecovery, incidentID, eff, subject, text, htmlBody)

	case tr.SendReminder:
		ev.FirstFail = snap.OpenIncidentStart
		openFor := time.Since(snap.OpenIncidentStart)
		subject, text, htmlBody := notify.ReminderAlert(ev, openFor)
		a.queue(ctx, model.AlertDown, incidentID, eff, subject, text, htmlBody)
	}
}

func (a *App) queue(ctx context.Context, kind model.AlertKind, incidentID *int64,
	eff model.Effective, subject, text, htmlBody string) {

	if _, err := notify.Enqueue(ctx, a.Store, kind, incidentID, eff.Recipients,
		subject, text, htmlBody); err != nil {
		a.log.Error("queue alert", "kind", kind, "device", eff.Name, "err", err)
		return
	}
	a.log.Info("alert queued", "kind", kind, "device", eff.Name,
		"recipients", len(eff.Recipients), "routing", eff.Source["recipients"])
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
