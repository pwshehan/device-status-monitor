package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/pwshehan/local-device-monitor/internal/model"
)

// Counts is the dashboard's top row.
//
// Paused counts a device whose own pause or whose group's maintenance window
// is open, which is the same max() rule model.Resolve applies — the tile and
// the scheduler must never disagree about what "paused" means.
type Counts struct {
	Devices  int
	Up       int
	Down     int
	Unknown  int
	Paused   int
	Disabled int
	Groups   int
}

// Counts tallies devices by status.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	now := ts(time.Now())
	err := s.r.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(d.status = 'UP'), 0),
		       COALESCE(SUM(d.status = 'DOWN'), 0),
		       COALESCE(SUM(d.status = 'UNKNOWN'), 0),
		       COALESCE(SUM(COALESCE(d.paused_until, 0) > ?
		                 OR COALESCE(g.paused_until, 0) > ?), 0),
		       COALESCE(SUM(d.enabled = 0), 0)
		FROM devices d
		LEFT JOIN groups g ON g.id = d.group_id`, now, now).
		Scan(&c.Devices, &c.Up, &c.Down, &c.Unknown, &c.Paused, &c.Disabled)
	if err != nil {
		return c, err
	}
	err = s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups`).Scan(&c.Groups)
	return c, err
}

// GroupStat is one section of the grouped dashboard: the group itself plus the
// tallies and today's aggregate availability across its members.
type GroupStat struct {
	// Group is nil for the Ungrouped bucket, which is a real section of the
	// dashboard but not a row in the groups table.
	Group *model.Group

	Members  int
	Up       int
	Down     int
	Unknown  int
	Paused   int
	Disabled int

	ChecksToday int64
	UpToday     int64
	// UptimeToday is nil when nothing has been checked today, which reads
	// differently from 0% and must not be rendered as an outage.
	UptimeToday *float64
}

// GroupStats returns one entry per group in display order, followed by the
// Ungrouped bucket when any device is in it.
func (s *Store) GroupStats(ctx context.Context) ([]GroupStat, error) {
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return nil, err
	}

	type tally struct {
		members, up, down, unknown, paused, disabled int

		checks, upChecks int64
	}
	byGroup := map[int64]*tally{}
	var ungrouped tally

	pick := func(id sql.NullInt64) *tally {
		if !id.Valid {
			return &ungrouped
		}
		t := byGroup[id.Int64]
		if t == nil {
			t = &tally{}
			byGroup[id.Int64] = t
		}
		return t
	}

	if err := s.eachRow(ctx, `
		SELECT d.group_id, COUNT(*),
		       COALESCE(SUM(d.status = 'UP'), 0),
		       COALESCE(SUM(d.status = 'DOWN'), 0),
		       COALESCE(SUM(d.status = 'UNKNOWN'), 0),
		       COALESCE(SUM(COALESCE(d.paused_until, 0) > ?
		                 OR COALESCE(g.paused_until, 0) > ?), 0),
		       COALESCE(SUM(d.enabled = 0), 0)
		FROM devices d
		LEFT JOIN groups g ON g.id = d.group_id
		GROUP BY d.group_id`,
		[]any{ts(time.Now()), ts(time.Now())},
		func(rows *sql.Rows) error {
			var id sql.NullInt64
			var members, up, down, unknown, paused, disabled int
			if err := rows.Scan(&id, &members, &up, &down, &unknown,
				&paused, &disabled); err != nil {
				return err
			}
			t := pick(id)
			t.members, t.up, t.down = members, up, down
			t.unknown, t.paused, t.disabled = unknown, paused, disabled
			return nil
		}); err != nil {
		return nil, err
	}

	// Today's aggregate is the share of all member checks that succeeded, not
	// the mean of member percentages — a site of twenty devices where one was
	// dead all day is 95% available, and averaging percentages would not say
	// that.
	if err := s.eachRow(ctx, `
		SELECT d.group_id, COUNT(*), COALESCE(SUM(h.status = 'UP'), 0)
		FROM heartbeats h
		JOIN devices d ON d.id = h.device_id
		WHERE h.checked_at >= ?
		GROUP BY d.group_id`,
		[]any{ts(StartOfToday())},
		func(rows *sql.Rows) error {
			var id sql.NullInt64
			var checks, up int64
			if err := rows.Scan(&id, &checks, &up); err != nil {
				return err
			}
			t := pick(id)
			t.checks, t.upChecks = checks, up
			return nil
		}); err != nil {
		return nil, err
	}

	build := func(g *model.Group, t tally) GroupStat {
		st := GroupStat{
			Group: g, Members: t.members, Up: t.up, Down: t.down,
			Unknown: t.unknown, Paused: t.paused, Disabled: t.disabled,
			ChecksToday: t.checks, UpToday: t.upChecks,
		}
		if t.checks > 0 {
			pct := float64(t.upChecks) * 100 / float64(t.checks)
			st.UptimeToday = &pct
		}
		return st
	}

	out := make([]GroupStat, 0, len(groups)+1)
	for i := range groups {
		g := &groups[i]
		t := byGroup[g.ID]
		if t == nil {
			t = &tally{}
		}
		out = append(out, build(g, *t))
	}
	if ungrouped.members > 0 {
		out = append(out, build(nil, ungrouped))
	}
	return out, nil
}

// GroupStatFor returns the tallies for one group, for the group_status event.
func (s *Store) GroupStatFor(ctx context.Context, groupID int64) (GroupStat, error) {
	all, err := s.GroupStats(ctx)
	if err != nil {
		return GroupStat{}, err
	}
	for _, st := range all {
		if st.Group != nil && st.Group.ID == groupID {
			return st, nil
		}
	}
	return GroupStat{}, ErrNotFound
}

// Offender is one row of the dashboard's worst-offenders tile.
type Offender struct {
	DeviceID  int64
	Name      string
	GroupName string
	Status    model.Status
	Checks    int64
	Failures  int64
	UptimePct float64
	LastError string
}

// WorstOffenders returns the devices with the lowest 24-hour availability.
//
// Devices with a clean 24 hours are left out rather than listed at 100%: the
// tile answers "what has been flapping", so a quiet day should render as an
// empty list, not a leaderboard.
func (s *Store) WorstOffenders(ctx context.Context, limit int) ([]Offender, error) {
	if limit <= 0 {
		limit = 5
	}
	var out []Offender
	err := s.eachRow(ctx, `
		SELECT d.id, d.name, COALESCE(g.name, ''), d.status, d.last_error,
		       COUNT(h.id) AS checks,
		       SUM(h.status = 'DOWN') AS failures,
		       SUM(h.status = 'UP') * 100.0 / COUNT(h.id) AS uptime_pct
		FROM heartbeats h
		JOIN devices d ON d.id = h.device_id
		LEFT JOIN groups g ON g.id = d.group_id
		WHERE h.checked_at >= ?
		GROUP BY d.id
		HAVING failures > 0
		ORDER BY uptime_pct, failures DESC
		LIMIT ?`,
		[]any{ts(time.Now().Add(-24 * time.Hour)), limit},
		func(rows *sql.Rows) error {
			var o Offender
			var status string
			if err := rows.Scan(&o.DeviceID, &o.Name, &o.GroupName, &status, &o.LastError,
				&o.Checks, &o.Failures, &o.UptimePct); err != nil {
				return err
			}
			o.Status = model.Status(status)
			out = append(out, o)
			return nil
		})
	return out, err
}

// OpenIncident is an unresolved outage carrying the names needed to render it.
type OpenIncident struct {
	model.Incident
	DeviceName string
	GroupID    *int64
	GroupName  string
}

// OpenIncidents lists current outages, longest-running first.
func (s *Store) OpenIncidents(ctx context.Context, limit int) ([]OpenIncident, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []OpenIncident
	err := s.eachRow(ctx, `
		SELECT i.id, i.device_id, i.started_at, i.detected_at, i.resolved_at,
		       i.duration_sec, i.cause, i.alert_sent, i.recovery_sent,
		       d.name, d.group_id, COALESCE(g.name, '')
		FROM incidents i
		JOIN devices d ON d.id = i.device_id
		LEFT JOIN groups g ON g.id = d.group_id
		WHERE i.resolved_at IS NULL
		ORDER BY i.started_at
		LIMIT ?`,
		[]any{limit},
		func(rows *sql.Rows) error {
			var oi OpenIncident
			var resolved, duration, groupID sql.NullInt64
			var alertSent, recoverySent int
			var started, detected int64

			if err := rows.Scan(&oi.ID, &oi.DeviceID, &started, &detected, &resolved,
				&duration, &oi.Cause, &alertSent, &recoverySent,
				&oi.DeviceName, &groupID, &oi.GroupName); err != nil {
				return err
			}
			oi.StartedAt = toTime(started)
			oi.DetectedAt = toTime(detected)
			oi.ResolvedAt = toTimePtr(resolved)
			oi.DurationSec = toInt64Ptr(duration)
			oi.AlertSent = alertSent != 0
			oi.RecoverySent = recoverySent != 0
			oi.GroupID = toInt64Ptr(groupID)
			out = append(out, oi)
			return nil
		})
	return out, err
}

// DBSizeBytes reports the size of the database from its page count, for the
// service-status screen.
func (s *Store) DBSizeBytes(ctx context.Context) (int64, error) {
	var pageCount, pageSize int64
	if err := s.r.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, err
	}
	if err := s.r.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, err
	}
	return pageCount * pageSize, nil
}

// eachRow runs a query and calls scan once per row, taking care of Close and
// rows.Err. The reporting queries here are all shaped this way, and forgetting
// either one is the classic way to leak a connection out of the read pool.
func (s *Store) eachRow(ctx context.Context, q string, args []any, scan func(*sql.Rows) error) error {
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// StartOfToday is local midnight. Rollup days are local dates, so everything
// that has to line up with them converts here and nowhere else.
func StartOfToday() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}
