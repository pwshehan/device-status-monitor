package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Rollup is one device-day of pre-aggregated history.
//
// The point of the table is that a 90-day view never touches raw rows: 200
// devices at 30 s is 576 000 heartbeats a day, and reading three months of
// those to colour ninety blocks would be absurd.
type Rollup struct {
	DeviceID     int64
	Day          string
	ChecksTotal  int64
	ChecksUp     int64
	UptimePct    float64
	AvgLatencyMS *float64
	P95LatencyMS *int64
	DowntimeSec  int64
}

// RollupCandidate is a device-day that has raw rows but no rollup yet.
type RollupCandidate struct {
	DeviceID    int64
	Day         string
	ChecksTotal int64
	ChecksUp    int64
	AvgLatency  *float64
}

// PendingRollups finds complete days that still need aggregating.
//
// `before` is local midnight: today is deliberately excluded, because a
// half-finished day would be rolled up and then never revisited, freezing the
// morning's numbers as the whole day's. The uptime queries read today from raw
// rows instead (§3, and store.deviceDays).
func (s *Store) PendingRollups(ctx context.Context, before time.Time, limit int) ([]RollupCandidate, error) {
	if limit <= 0 {
		limit = 500
	}
	var out []RollupCandidate
	// GROUP BY repeats the date expression rather than referring to the output
	// alias. SQLite resolves a name in GROUP BY against the *source* columns
	// first, and rollups_daily has a column called `day` — so grouping by the
	// alias silently groups by r.day, which is NULL for every unrolled row.
	// Every day then collapses into one group reporting one arbitrary date.
	err := s.eachRow(ctx, `
		SELECT h.device_id,
		       date(h.checked_at, 'unixepoch', 'localtime') AS local_day,
		       COUNT(*),
		       COALESCE(SUM(h.status = 'UP'), 0),
		       AVG(CASE WHEN h.status = 'UP' THEN h.latency_ms END)
		FROM heartbeats h
		LEFT JOIN rollups_daily r
		       ON r.device_id = h.device_id
		      AND r.day = date(h.checked_at, 'unixepoch', 'localtime')
		WHERE h.checked_at < ? AND r.device_id IS NULL
		GROUP BY h.device_id, date(h.checked_at, 'unixepoch', 'localtime')
		ORDER BY date(h.checked_at, 'unixepoch', 'localtime')
		LIMIT ?`,
		[]any{ts(before), limit},
		func(rows *sql.Rows) error {
			var c RollupCandidate
			var avg sql.NullFloat64
			if err := rows.Scan(&c.DeviceID, &c.Day, &c.ChecksTotal, &c.ChecksUp, &avg); err != nil {
				return err
			}
			if avg.Valid {
				c.AvgLatency = &avg.Float64
			}
			out = append(out, c)
			return nil
		})
	return out, err
}

// P95Latency returns the 95th percentile of successful latencies for a
// device-day.
//
// Done with ORDER BY and OFFSET because SQLite has no percentile function, and
// the alternative — reading the day's latencies into Go — would pull half a
// million numbers across the wire to produce one. This runs once per
// device-day, not per request.
func (s *Store) P95Latency(ctx context.Context, deviceID int64, day string) (*int64, error) {
	var count int64
	if err := s.r.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM heartbeats
		WHERE device_id = ? AND status = 'UP'
		  AND date(checked_at, 'unixepoch', 'localtime') = ?`,
		deviceID, day).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}

	// Nearest-rank: the smallest value at or above 95% of the sorted samples.
	offset := (count * 95 / 100)
	if offset >= count {
		offset = count - 1
	}

	var p95 int64
	err := s.r.QueryRowContext(ctx, `
		SELECT latency_ms FROM heartbeats
		WHERE device_id = ? AND status = 'UP'
		  AND date(checked_at, 'unixepoch', 'localtime') = ?
		ORDER BY latency_ms
		LIMIT 1 OFFSET ?`,
		deviceID, day, offset).Scan(&p95)
	if err != nil {
		return nil, err
	}
	return &p95, nil
}

// DowntimeForDay returns how many seconds a device was down on a local day.
//
// Derived from the incident log clipped to the day, never from counting DOWN
// heartbeats. Counting rows would be wrong in three ways at once: it assumes
// the interval never changed, it loses the time between the last probe before
// a restart and the first one after, and it cannot see an outage that began
// yesterday and ended today.
func (s *Store) DowntimeForDay(ctx context.Context, deviceID int64, day string) (int64, error) {
	start, err := time.ParseInLocation(dayFormat, day, time.Local)
	if err != nil {
		return 0, fmt.Errorf("parse day %q: %w", day, err)
	}
	end := start.AddDate(0, 0, 1)

	// Positional parameters repeated rather than numbered ones: the driver
	// binds by position, and MIN/MAX with two arguments are SQLite's scalar
	// forms, which is what clips each incident to this day's bounds.
	var seconds sql.NullFloat64
	err = s.r.QueryRowContext(ctx, `
		SELECT SUM(
		         MIN(COALESCE(resolved_at, ?), ?) - MAX(started_at, ?)
		       )
		FROM incidents
		WHERE device_id = ?
		  AND started_at < ?
		  AND COALESCE(resolved_at, ?) > ?`,
		ts(end), ts(end), ts(start),
		deviceID,
		ts(end),
		ts(end), ts(start)).
		Scan(&seconds)
	if err != nil {
		return 0, err
	}
	if !seconds.Valid || seconds.Float64 < 0 {
		return 0, nil
	}
	return int64(seconds.Float64), nil
}

// WriteRollup upserts one device-day.
//
// An upsert rather than an insert so a re-run is harmless: the janitor may
// find the same day again after a restart, and a day whose incidents were
// still open when it was first rolled up gets a corrected downtime figure.
func (s *Store) WriteRollup(ctx context.Context, r Rollup) error {
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO rollups_daily
			(device_id, day, checks_total, checks_up, uptime_pct,
			 avg_latency_ms, p95_latency_ms, downtime_sec)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(device_id, day) DO UPDATE SET
			checks_total = excluded.checks_total,
			checks_up = excluded.checks_up,
			uptime_pct = excluded.uptime_pct,
			avg_latency_ms = excluded.avg_latency_ms,
			p95_latency_ms = excluded.p95_latency_ms,
			downtime_sec = excluded.downtime_sec`,
		r.DeviceID, r.Day, r.ChecksTotal, r.ChecksUp, r.UptimePct,
		nfloat(r.AvgLatencyMS), nint64(r.P95LatencyMS), r.DowntimeSec)
	return mapErr(err)
}

// CountRollups returns how many device-days are stored, for diagnostics.
func (s *Store) CountRollups(ctx context.Context) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM rollups_daily`).Scan(&n)
	return n, err
}

// PruneChunk is how many raw rows one delete removes.
//
// SQLite takes a write lock for the whole statement, so a single unbounded
// DELETE of a week's heartbeats would stall the writer — and the writer is
// what records probe results. Chunking keeps each lock short.
const PruneChunk = 10_000

// PruneHeartbeats deletes raw rows older than `before`, in chunks.
//
// Returns the number deleted. Stops early if the context is cancelled, which
// makes a shutdown mid-prune harmless: the remainder goes on the next pass.
func (s *Store) PruneHeartbeats(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, nil
		}
		res, err := s.w.ExecContext(ctx, `
			DELETE FROM heartbeats WHERE id IN (
				SELECT id FROM heartbeats WHERE checked_at < ? LIMIT ?
			)`, ts(before), PruneChunk)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < PruneChunk {
			return total, nil
		}
	}
}

// PruneRollups deletes device-days older than the retention window.
func (s *Store) PruneRollups(ctx context.Context, beforeDay string) (int64, error) {
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM rollups_daily WHERE day < ?`, beforeDay)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneResolvedIncidents deletes closed incidents past the rollup window.
//
// Open incidents are never touched, whatever their age: an outage that has run
// for a year is still the outage this device is in, and deleting it would lose
// the start time the recovery mail needs.
func (s *Store) PruneResolvedIncidents(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM incidents WHERE resolved_at IS NOT NULL AND resolved_at < ?`,
		ts(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneSentAlerts clears delivered mail from the outbox.
//
// Failed rows are kept regardless of age: they are the only record that an
// alert never went out, and that is worth more than the space.
func (s *Store) PruneSentAlerts(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.w.ExecContext(ctx,
		`DELETE FROM alert_outbox WHERE sent_at IS NOT NULL AND sent_at < ?`,
		ts(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Checkpoint truncates the write-ahead log.
//
// Without this the WAL grows until the next SQLite-internal checkpoint, which
// on a database that is written every few seconds and read constantly can be a
// long time. TRUNCATE is the mode that actually returns the space.
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// Vacuum rewrites the database file, returning space that deleted rows left
// behind. Blocking and slow, so the janitor only calls it rarely and when
// nothing else is due.
func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.w.ExecContext(ctx, `VACUUM`)
	return err
}

// FreePages reports how much of the file is empty, which is what decides
// whether a VACUUM is worth its cost.
func (s *Store) FreePages(ctx context.Context) (free int64, total int64, err error) {
	if err = s.r.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil {
		return 0, 0, err
	}
	if err = s.r.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&total); err != nil {
		return 0, 0, err
	}
	return free, total, nil
}

func nfloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
