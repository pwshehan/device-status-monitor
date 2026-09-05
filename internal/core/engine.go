package core

import (
	"context"
	"os"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/api"
	"github.com/gkgraphite/device-status-monitor/internal/model"
	"github.com/gkgraphite/device-status-monitor/internal/probe"
	"github.com/gkgraphite/device-status-monitor/internal/state"
)

// App satisfies api.Engine. The methods are gathered here rather than spread
// through core.go so the API's surface onto the engine is one file long and
// obvious.
var _ api.Engine = (*App)(nil)

// Running is how many device workers are active.
func (a *App) Running() int { return a.Scheduler.Running() }

// SchedulerLagMS is how overdue the most overdue device's probe is.
//
// Measured as now - last_check - interval across every scheduled device, and
// zero when everything is on time. This is the number that says whether the
// concurrency limit or the writer has become the bottleneck: a lag that climbs
// and stays up means probes are queueing behind the semaphore, which no count
// of devices or heartbeats would reveal.
//
// A device that has never been checked is not counted: it is waiting out its
// startup jitter, which is deliberate spreading, not lag.
func (a *App) SchedulerLagMS() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	var worst time.Duration
	for id, eff := range a.effs {
		last, seen := a.lastCheck[id]
		if !seen {
			continue
		}
		if overdue := now.Sub(last) - eff.Interval; overdue > worst {
			worst = overdue
		}
	}
	if worst < 0 {
		return 0
	}
	return worst.Milliseconds()
}

// SendTestEmail delivers a test message immediately, bypassing the outbox.
func (a *App) SendTestEmail(ctx context.Context, to []string) error {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "this machine"
	}
	return a.Notifier.SendTest(ctx, host, to)
}

// SaveSMTPPassword seals a new SMTP password. The plaintext never reaches the
// database.
func (a *App) SaveSMTPPassword(ctx context.Context, plaintext string) error {
	return a.Notifier.SavePassword(ctx, plaintext)
}

// --- event publishing --------------------------------------------------------

// publishHeartbeat announces one probe result. This is the high-rate feed the
// dashboard's status strips read.
func (a *App) publishHeartbeat(eff model.Effective, res probe.Result, status model.Status) {
	a.Hub.Publish(api.Event{Type: api.EventHeartbeat, Data: api.HeartbeatEvent{
		DeviceID:  eff.DeviceID,
		Status:    status,
		LatencyMS: res.LatencyMS,
		T:         res.At.Unix(),
		Class:     string(res.Class),
		Error:     res.ErrMsg(),
	}})
}

// publishTransition announces a state change, the incident it opened or
// closed, and the group tally that changed with it.
//
// Called from the evaluator goroutine, which is the only place that knows a
// transition happened. Publish never blocks, so a stalled browser tab cannot
// slow down monitoring — it loses events instead.
func (a *App) publishTransition(ctx context.Context, eff model.Effective,
	res probe.Result, tr state.Transition) {

	a.Hub.Publish(api.Event{Type: api.EventDeviceStatus, Data: api.DeviceStatusEvent{
		DeviceID:       eff.DeviceID,
		Name:           eff.Name,
		GroupID:        eff.GroupID,
		GroupName:      eff.GroupName,
		Status:         tr.To,
		PreviousStatus: tr.From,
		At:             res.At.UTC().Format(time.RFC3339),
		LatencyMS:      res.LatencyMS,
		Class:          string(res.Class),
		Error:          res.ErrMsg(),
		IncidentID:     incidentIDPtr(tr.IncidentID),
	}})

	switch {
	case tr.OpenIncident:
		a.Hub.Publish(api.Event{Type: api.EventIncident, Data: api.IncidentEvent{
			ID:         tr.IncidentID,
			DeviceID:   eff.DeviceID,
			DeviceName: eff.Name,
			GroupID:    eff.GroupID,
			State:      "opened",
			StartedAt:  tr.IncidentStart.UTC().Format(time.RFC3339),
			Cause:      tr.Cause,
		}})
	case tr.CloseIncident:
		resolved := res.At.UTC().Format(time.RFC3339)
		secs := int64(tr.Downtime.Seconds())
		a.Hub.Publish(api.Event{Type: api.EventIncident, Data: api.IncidentEvent{
			ID:          tr.IncidentID,
			DeviceID:    eff.DeviceID,
			DeviceName:  eff.Name,
			GroupID:     eff.GroupID,
			State:       "resolved",
			StartedAt:   tr.IncidentStart.UTC().Format(time.RFC3339),
			ResolvedAt:  &resolved,
			DurationSec: &secs,
		}})
	}

	// The group header shows "6/8 up", so it has to be recomputed when a
	// member moves. Only on a transition, never per probe: this is a query,
	// and transitions are rare where probes are not.
	if eff.GroupID == nil {
		return
	}
	st, err := a.Store.GroupStatFor(ctx, *eff.GroupID)
	if err != nil {
		if ctx.Err() == nil {
			a.log.Debug("group tallies for event", "group", *eff.GroupID, "err", err)
		}
		return
	}
	a.Hub.Publish(api.Event{Type: api.EventGroupStatus, Data: api.NewGroupStatusEvent(st)})
}

func incidentIDPtr(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

// JanitorStatus reports the last maintenance pass, for /api/health.
func (a *App) JanitorStatus() api.JanitorStatus {
	at, res := a.Janitor.LastPass()
	status := api.JanitorStatus{
		At:               at,
		RolledUp:         res.RolledUp,
		HeartbeatsPruned: res.HeartbeatsPruned,
		RollupsPruned:    res.RollupsPruned,
		Vacuumed:         res.Vacuumed,
	}
	if res.Err != nil {
		status.Err = res.Err.Error()
	}
	return status
}
