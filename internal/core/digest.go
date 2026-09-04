package core

import (
	"context"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/model"
	"github.com/gkgraphite/device-status-monitor/internal/notify"
	"github.com/gkgraphite/device-status-monitor/internal/store"
)

// Collapse rules. The window is a setting; these two are not, because they
// describe what "one event" means rather than a preference.
const (
	// DigestThreshold is how many devices in one group must fail together
	// before their mails collapse into one.
	//
	// Three, not two: a pair of devices failing together is often two devices
	// failing, and a two-line digest is worse than two mails. Three is where
	// "the site" becomes the better description.
	DigestThreshold = 3

	// GlobalDigestThreshold is the core-switch case: this many devices across
	// any groups collapse into a single mail, because failures spread over
	// unrelated groups have one cause upstream of all of them.
	GlobalDigestThreshold = 10

	// MaxHold bounds how long an alert can wait for company. The plan's
	// collapse window is 120 s; this is that ceiling, so a straggler cannot
	// delay the mail indefinitely by arriving just before every flush.
	MaxHold = 120 * time.Second
)

// The collapse window is the one place this system trades speed for
// readability, so it is worth being explicit about the cost: an alert arrives
// one window later than the state change. With the defaults that is 90 s to
// detect plus 15 s to collapse.
//
// Two things keep the bill small. A group whose every member has already
// failed flushes at once, because there is nothing left to wait for — which is
// the total-site outage, the case the digest exists for. And setting
// alert.collapse_sec to 0 restores immediate per-device mail for anyone who
// would rather have six messages than wait.

// pendingAlert is one alert waiting to see whether it has company.
type pendingAlert struct {
	eff        model.Effective
	kind       model.AlertKind
	event      notify.Event
	incidentID *int64
	queuedAt   time.Time
}

// digestKey groups alerts that could collapse together: the same group, in the
// same direction. A site going down and another coming back are two events
// however close together they happen.
type digestKey struct {
	groupID int64 // 0 for ungrouped
	kind    model.AlertKind
}

// alertBuffer holds alerts briefly so related ones can be sent as one mail.
//
// Only ever touched from the evaluator goroutine — the flush is a case in its
// select loop rather than a timer of its own — so there is no lock here and no
// way for a flush to interleave with a transition.
type alertBuffer struct {
	batches map[digestKey][]pendingAlert
}

func newAlertBuffer() *alertBuffer {
	return &alertBuffer{batches: map[digestKey][]pendingAlert{}}
}

func (b *alertBuffer) add(p pendingAlert) {
	var groupID int64
	if p.eff.GroupID != nil {
		groupID = *p.eff.GroupID
	}
	key := digestKey{groupID: groupID, kind: p.kind}
	b.batches[key] = append(b.batches[key], p)
}

func (b *alertBuffer) pending() int {
	n := 0
	for _, batch := range b.batches {
		n += len(batch)
	}
	return n
}

// due reports whether a batch has waited long enough.
//
// Two rules at once: flush when nothing new has arrived for `window`, and
// never hold anything longer than MaxHold. The first keeps a tight outage
// collapsing quickly; the second stops a slow trickle of failures from
// deferring the mail for ever.
func due(batch []pendingAlert, now time.Time, window time.Duration) bool {
	if len(batch) == 0 {
		return false
	}
	first, last := batch[0].queuedAt, batch[len(batch)-1].queuedAt
	return now.Sub(last) >= window || now.Sub(first) >= MaxHold
}

// queueAlert parks an alert for the collapse window, or sends it immediately
// when collapsing is switched off.
func (a *App) queueAlert(ctx context.Context, p pendingAlert) {
	policy := a.alertPolicy(ctx)
	if policy.CollapseWindow <= 0 {
		a.sendIndividual(ctx, p)
		return
	}
	a.alerts.add(p)
}

// flushAlerts sends whatever has waited long enough.
//
// Called from the evaluator's select loop on a ticker, and once more on
// shutdown so a pending alert is not lost to a restart.
func (a *App) flushAlerts(ctx context.Context, now time.Time, force bool) {
	if a.alerts.pending() == 0 {
		return
	}
	policy := a.alertPolicy(ctx)

	// The global fallback first: if this many devices are down at once, the
	// per-group split is the wrong story to tell.
	if downCount := a.pendingDown(); downCount >= GlobalDigestThreshold {
		a.flushGlobal(ctx)
		return
	}

	for key, batch := range a.alerts.batches {
		// Nothing left to wait for: every member of this group is already
		// pending, so more company cannot arrive.
		complete := key.groupID != 0 && len(batch) >= a.groupSize(ctx, key.groupID, len(batch)+1)

		if !force && !complete && !due(batch, now, policy.CollapseWindow) {
			continue
		}
		delete(a.alerts.batches, key)

		switch {
		// Ungrouped devices never digest: they have nothing in common but
		// having no group, which is not a diagnosis.
		case key.groupID == 0 || len(batch) < DigestThreshold:
			for _, p := range batch {
				a.sendIndividual(ctx, p)
			}
		case key.kind == model.AlertDown:
			a.sendGroupDown(ctx, key.groupID, batch)
		default:
			a.sendGroupRecovery(ctx, key.groupID, batch)
		}
	}
}

func (a *App) pendingDown() int {
	n := 0
	for key, batch := range a.alerts.batches {
		if key.kind == model.AlertDown {
			n += len(batch)
		}
	}
	return n
}

// flushGlobal collapses everything pending into one mail per direction.
func (a *App) flushGlobal(ctx context.Context) {
	var down, recovered []pendingAlert
	for key, batch := range a.alerts.batches {
		if key.kind == model.AlertDown {
			down = append(down, batch...)
		} else {
			recovered = append(recovered, batch...)
		}
		delete(a.alerts.batches, key)
	}

	if len(down) > 0 {
		groups := map[string]bool{}
		members := make([]notify.DigestMember, 0, len(down))
		for _, p := range down {
			groups[p.eff.GroupName] = true
			members = append(members, notify.DigestMember{
				Name:   p.eff.Name,
				Addr:   p.eff.Addr,
				Reason: p.event.ErrMsg,
				Group:  p.eff.GroupName,
			})
		}
		subject, text, htmlBody := notify.GlobalDown(len(groups), members)
		a.log.Warn("collapsing alerts into one global digest",
			"devices", len(down), "groups", len(groups))
		a.send(ctx, model.AlertDigest, nil, a.digestRecipients(ctx, down), subject, text, htmlBody)
	}

	// Recoveries in the same pass go out per group, since a recovery digest
	// spanning every site says nothing useful.
	byGroup := map[int64][]pendingAlert{}
	for _, p := range recovered {
		var id int64
		if p.eff.GroupID != nil {
			id = *p.eff.GroupID
		}
		byGroup[id] = append(byGroup[id], p)
	}
	for id, batch := range byGroup {
		if id != 0 && len(batch) >= DigestThreshold {
			a.sendGroupRecovery(ctx, id, batch)
			continue
		}
		for _, p := range batch {
			a.sendIndividual(ctx, p)
		}
	}
}

func (a *App) sendGroupDown(ctx context.Context, groupID int64, batch []pendingAlert) {
	group := batch[0].eff.GroupName
	total := a.groupSize(ctx, groupID, len(batch))

	members := make([]notify.DigestMember, 0, len(batch))
	for _, p := range batch {
		members = append(members, notify.DigestMember{
			Name:   p.eff.Name,
			Addr:   p.eff.Addr,
			Reason: p.event.ErrMsg,
		})
	}
	subject, text, htmlBody := notify.GroupDown(group, total, members)
	a.log.Warn("collapsing alerts into one group digest",
		"group", group, "devices", len(batch), "of", total)
	a.send(ctx, model.AlertDigest, nil, a.digestRecipients(ctx, batch), subject, text, htmlBody)
}

func (a *App) sendGroupRecovery(ctx context.Context, groupID int64, batch []pendingAlert) {
	_ = groupID
	group := batch[0].eff.GroupName

	members := make([]notify.DigestMember, 0, len(batch))
	for _, p := range batch {
		members = append(members, notify.DigestMember{
			Name:     p.eff.Name,
			Addr:     p.eff.Addr,
			Downtime: p.event.Downtime,
		})
	}
	subject, text, htmlBody := notify.GroupRecovery(group, members)
	a.log.Info("collapsing recoveries into one group digest",
		"group", group, "devices", len(batch))
	a.send(ctx, model.AlertDigest, nil, a.digestRecipients(ctx, batch), subject, text, htmlBody)
}

// sendIndividual renders and queues one device's own mail.
func (a *App) sendIndividual(ctx context.Context, p pendingAlert) {
	var subject, text, htmlBody string
	switch p.kind {
	case model.AlertDown:
		subject, text, htmlBody = notify.DownAlert(p.event)
	case model.AlertRecovery:
		subject, text, htmlBody = notify.RecoveryAlert(p.event)
	default:
		return
	}
	a.send(ctx, p.kind, p.incidentID, p.eff.Recipients, subject, text, htmlBody)
}

// digestRecipients is the union of the batch's recipient lists.
//
// A digest can only happen inside one group, or globally; in the first case
// every member resolves to the same list, and in the second the union is the
// only defensible answer — the one mail has to reach everyone who would have
// received one of the mails it replaced.
func (a *App) digestRecipients(ctx context.Context, batch []pendingAlert) []string {
	_ = ctx
	seen := map[string]bool{}
	var out []string
	for _, p := range batch {
		for _, r := range p.eff.Recipients {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// groupSize is the group's member count, for "6 of 8".
func (a *App) groupSize(ctx context.Context, groupID int64, fallback int) int {
	st, err := a.Store.GroupStatFor(ctx, groupID)
	if err != nil || st.Members == 0 {
		return fallback
	}
	return st.Members
}

// alertPolicy reads the collapse window and mail cap, cached briefly.
//
// Cached because this is consulted on every transition, and a settings read is
// a query; five seconds of staleness on a collapse window costs nothing.
func (a *App) alertPolicy(ctx context.Context) store.AlertPolicy {
	if time.Since(a.policyAt) < 5*time.Second {
		return a.policy
	}
	policy, err := a.Store.AlertPolicy(ctx)
	if err != nil && ctx.Err() == nil {
		a.log.Warn("read alert policy, using previous", "err", err)
		return a.policy
	}
	a.policy, a.policyAt = policy, time.Now()
	return policy
}
