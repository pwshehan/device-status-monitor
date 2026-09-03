package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
)

// dayFormat is the local calendar date used as the rollup key.
const dayFormat = "2006-01-02"

// MaxUptimeDays bounds an uptime window, matching retention.rollup_days.
const MaxUptimeDays = 400

// DayUptime is one device-day of availability.
//
// Source says where the numbers came from: "rollup" once the janitor has
// aggregated the day, "raw" for days still in the heartbeat table — today
// always, and every day before the first rollup run. Without it, the UI cannot
// tell a genuinely quiet day from a day whose raw rows have been pruned and
// never rolled up.
type DayUptime struct {
	Day          string
	ChecksTotal  int64
	ChecksUp     int64
	UptimePct    float64
	AvgLatencyMS *float64
	P95LatencyMS *int64
	DowntimeSec  int64
	Source       string
}

// GroupDayUptime is one group-day: aggregate availability across members, plus
// the member that had the worst day.
//
// Two numbers because they answer different questions. A site of twenty
// devices where one was dead all day is 95% available in aggregate, and the
// strip is coloured by that; the worst member is what the tooltip names.
type GroupDayUptime struct {
	Day             string
	Devices         int
	ChecksTotal     int64
	ChecksUp        int64
	UptimePct       float64
	WorstDevicePct  *float64
	WorstDeviceName string
	DowntimeSec     int64
	Source          string
}

// DeviceUptime returns the last days calendar days for one device, oldest
// first, with days that have no data at all left out.
func (s *Store) DeviceUptime(ctx context.Context, deviceID int64, days int) ([]DayUptime, error) {
	rows, err := s.deviceDays(ctx, []int64{deviceID}, days)
	if err != nil {
		return nil, err
	}
	out := make([]DayUptime, 0, len(rows))
	for _, r := range rows {
		out = append(out, DayUptime{
			Day:          r.day,
			ChecksTotal:  r.checksTotal,
			ChecksUp:     r.checksUp,
			UptimePct:    r.uptimePct,
			AvgLatencyMS: r.avgLatency,
			P95LatencyMS: r.p95Latency,
			DowntimeSec:  r.downtimeSec,
			Source:       r.source,
		})
	}
	sortByDay(out, func(d DayUptime) string { return d.Day })
	return out, nil
}

// GroupUptime derives a group's history from its current members.
//
// There is no group rollup table on purpose: membership is mutable, so a
// stored group history would describe a past that never happened the moment a
// device moves. Ninety days times twenty members is a few hundred rows, which
// is cheap enough to fold per request.
func (s *Store) GroupUptime(ctx context.Context, groupID int64, days int) ([]GroupDayUptime, error) {
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return nil, err
	}
	members, err := s.ListDevices(ctx, DeviceFilter{GroupID: &groupID})
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return []GroupDayUptime{}, nil
	}
	ids := make([]int64, 0, len(members))
	for _, d := range members {
		ids = append(ids, d.ID)
	}

	rows, err := s.deviceDays(ctx, ids, days)
	if err != nil {
		return nil, err
	}

	byDay := map[string]*GroupDayUptime{}
	sources := map[string]map[string]bool{}
	for _, r := range rows {
		agg := byDay[r.day]
		if agg == nil {
			agg = &GroupDayUptime{Day: r.day}
			byDay[r.day] = agg
			sources[r.day] = map[string]bool{}
		}
		agg.Devices++
		agg.ChecksTotal += r.checksTotal
		agg.ChecksUp += r.checksUp
		agg.DowntimeSec += r.downtimeSec
		if agg.WorstDevicePct == nil || r.uptimePct < *agg.WorstDevicePct {
			pct := r.uptimePct
			agg.WorstDevicePct = &pct
			agg.WorstDeviceName = r.deviceName
		}
		sources[r.day][r.source] = true
	}

	out := make([]GroupDayUptime, 0, len(byDay))
	for day, agg := range byDay {
		if agg.ChecksTotal > 0 {
			agg.UptimePct = float64(agg.ChecksUp) * 100 / float64(agg.ChecksTotal)
		}
		switch {
		case len(sources[day]) > 1:
			agg.Source = "mixed"
		case sources[day]["rollup"]:
			agg.Source = "rollup"
		default:
			agg.Source = "raw"
		}
		out = append(out, *agg)
	}
	sortByDay(out, func(d GroupDayUptime) string { return d.Day })
	return out, nil
}

// deviceDayKey identifies one device-day.
type deviceDayKey struct {
	device int64
	day    string
}

// deviceDay is one device's day, from either source.
type deviceDay struct {
	day         string
	deviceID    int64
	deviceName  string
	checksTotal int64
	checksUp    int64
	uptimePct   float64
	avgLatency  *float64
	p95Latency  *int64
	downtimeSec int64
	source      string
}

// deviceDays reads per-device, per-day availability, preferring rollups and
// filling any day the janitor has not aggregated yet from raw heartbeats.
//
// The window is computed here, in Go, as local midnight N-1 days ago, and both
// queries are filtered by it — rather than letting SQLite's date() do the
// arithmetic in one place and Go in another. One clock, one timezone
// conversion, and AddDate gets the 23- and 25-hour days right.
func (s *Store) deviceDays(ctx context.Context, ids []int64, days int) ([]deviceDay, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if days <= 0 {
		days = 90
	}
	if days > MaxUptimeDays {
		days = MaxUptimeDays
	}
	cutoff := StartOfToday().AddDate(0, 0, -(days - 1))
	sinceDay := cutoff.Format(dayFormat)

	in := inClause(len(ids))
	idArgs := make([]any, 0, len(ids))
	for _, id := range ids {
		idArgs = append(idArgs, id)
	}

	rolled := map[deviceDayKey]bool{}
	var out []deviceDay

	if err := s.eachRow(ctx, `
		SELECT r.device_id, d.name, r.day, r.checks_total, r.checks_up,
		       r.uptime_pct, r.avg_latency_ms, r.p95_latency_ms, r.downtime_sec
		FROM rollups_daily r
		JOIN devices d ON d.id = r.device_id
		WHERE r.device_id IN (`+in+`) AND r.day >= ?`,
		withArgs(idArgs, sinceDay),
		func(rows *sql.Rows) error {
			var r deviceDay
			var avg sql.NullFloat64
			var p95 sql.NullInt64
			if err := rows.Scan(&r.deviceID, &r.deviceName, &r.day,
				&r.checksTotal, &r.checksUp, &r.uptimePct, &avg, &p95,
				&r.downtimeSec); err != nil {
				return err
			}
			if avg.Valid {
				r.avgLatency = &avg.Float64
			}
			r.p95Latency = toInt64Ptr(p95)
			r.source = "rollup"
			rolled[deviceDayKey{r.deviceID, r.day}] = true
			out = append(out, r)
			return nil
		}); err != nil {
		return nil, err
	}

	// Raw days, including today's partial.
	var raw []deviceDay
	if err := s.eachRow(ctx, `
		SELECT h.device_id, d.name,
		       date(h.checked_at, 'unixepoch', 'localtime') AS day,
		       COUNT(*), COALESCE(SUM(h.status = 'UP'), 0),
		       AVG(CASE WHEN h.status = 'UP' THEN h.latency_ms END)
		FROM heartbeats h
		JOIN devices d ON d.id = h.device_id
		WHERE h.device_id IN (`+in+`) AND h.checked_at >= ?
		GROUP BY h.device_id, day`,
		withArgs(idArgs, ts(cutoff)),
		func(rows *sql.Rows) error {
			var r deviceDay
			var avg sql.NullFloat64
			if err := rows.Scan(&r.deviceID, &r.deviceName, &r.day,
				&r.checksTotal, &r.checksUp, &avg); err != nil {
				return err
			}
			if avg.Valid {
				r.avgLatency = &avg.Float64
			}
			if r.checksTotal > 0 {
				r.uptimePct = float64(r.checksUp) * 100 / float64(r.checksTotal)
			}
			r.source = "raw"
			raw = append(raw, r)
			return nil
		}); err != nil {
		return nil, err
	}

	pending := make([]deviceDay, 0, len(raw))
	for _, r := range raw {
		if !rolled[deviceDayKey{r.deviceID, r.day}] {
			pending = append(pending, r)
		}
	}
	if len(pending) > 0 {
		// A rolled-up day already carries its downtime; a raw day has to get it
		// from the incident log, which is the only exact record of how long a
		// device was actually down.
		down, err := s.downtimeByDay(ctx, idArgs, in, cutoff)
		if err != nil {
			return nil, err
		}
		for i := range pending {
			pending[i].downtimeSec = down[deviceDayKey{pending[i].deviceID, pending[i].day}]
		}
		out = append(out, pending...)
	}
	return out, nil
}

// downtimeByDay clips every incident overlapping the window to local calendar
// days and sums the seconds per device-day.
func (s *Store) downtimeByDay(ctx context.Context, idArgs []any, in string, cutoff time.Time) (map[deviceDayKey]int64, error) {
	out := map[deviceDayKey]int64{}
	now := time.Now()

	err := s.eachRow(ctx, `
		SELECT device_id, started_at, resolved_at
		FROM incidents
		WHERE device_id IN (`+in+`) AND (resolved_at IS NULL OR resolved_at >= ?)`,
		withArgs(idArgs, ts(cutoff)),
		func(rows *sql.Rows) error {
			var deviceID, started int64
			var resolved sql.NullInt64
			if err := rows.Scan(&deviceID, &started, &resolved); err != nil {
				return err
			}
			from := toTime(started).Local()
			to := now
			if resolved.Valid {
				to = toTime(resolved.Int64).Local()
			}
			if from.Before(cutoff) {
				from = cutoff
			}
			for day := dayStart(from); day.Before(to); day = day.AddDate(0, 0, 1) {
				next := day.AddDate(0, 0, 1)
				lo, hi := day, next
				if from.After(lo) {
					lo = from
				}
				if to.Before(hi) {
					hi = to
				}
				if hi.After(lo) {
					out[deviceDayKey{deviceID, day.Format(dayFormat)}] += int64(hi.Sub(lo).Seconds())
				}
			}
			return nil
		})
	return out, err
}

// dayStart is local midnight of t's calendar day.
func dayStart(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// inClause builds "?,?,?" for an IN list. Device ids come from the database or
// from a parsed request, never interpolated as text.
func inClause(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// sortByDay orders rows oldest first. Days are zero-padded ISO dates, so a
// lexical sort is a chronological one.
func sortByDay[T any](rows []T, day func(T) string) {
	slices.SortFunc(rows, func(a, b T) int { return strings.Compare(day(a), day(b)) })
}

// withArgs copies base and appends extra, so two queries sharing an id list
// cannot scribble on each other's argument slots.
func withArgs(base []any, extra ...any) []any {
	out := make([]any, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}
