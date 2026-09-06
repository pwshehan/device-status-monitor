package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/model"
)

// InsertHeartbeats writes a batch of heartbeats in one transaction.
//
// Batching is the whole point: 200 devices on a 30 s interval is ~7 inserts a
// second, and one transaction per insert would fsync the WAL that often for no
// benefit. The writer goroutine accumulates and calls this.
func (s *Store) InsertHeartbeats(ctx context.Context, batch []model.Heartbeat) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO heartbeats (device_id, status, latency_ms, error_msg, checked_at)
		VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, h := range batch {
		var errMsg any
		if h.ErrorMsg != "" {
			errMsg = h.ErrorMsg
		}
		if _, err := stmt.ExecContext(ctx,
			h.DeviceID, string(h.Status), h.LatencyMS, errMsg, ts(h.CheckedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Heartbeats returns the raw series for one device within a window, oldest
// first. limit caps the row count; 0 means no cap.
func (s *Store) Heartbeats(ctx context.Context, deviceID int64, from, to time.Time, limit int) ([]model.Heartbeat, error) {
	q := `SELECT status, latency_ms, COALESCE(error_msg, ''), checked_at
	      FROM heartbeats
	      WHERE device_id = ? AND checked_at >= ? AND checked_at <= ?
	      ORDER BY checked_at`
	args := []any{deviceID, ts(from), ts(to)}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Heartbeat
	for rows.Next() {
		h := model.Heartbeat{DeviceID: deviceID}
		var checkedAt int64
		var status string
		if err := rows.Scan(&status, &h.LatencyMS, &h.ErrorMsg, &checkedAt); err != nil {
			return nil, err
		}
		h.Status = model.Status(status)
		h.CheckedAt = toTime(checkedAt)
		out = append(out, h)
	}
	return out, rows.Err()
}

// CountHeartbeats returns the number of stored raw rows, for /api/health.
func (s *Store) CountHeartbeats(ctx context.Context) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM heartbeats`).Scan(&n)
	return n, err
}

// Uptime24h returns the share of successful checks over the last 24 hours, or
// nil when the device has no checks in that window.
func (s *Store) Uptime24h(ctx context.Context, deviceID int64) (*float64, error) {
	var total int64
	var up sql.NullFloat64
	err := s.r.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(status = 'UP') * 100.0 / NULLIF(COUNT(*), 0)
		FROM heartbeats
		WHERE device_id = ? AND checked_at >= ?`,
		deviceID, ts(time.Now().Add(-24*time.Hour))).Scan(&total, &up)
	if err != nil {
		return nil, err
	}
	if total == 0 || !up.Valid {
		return nil, nil
	}
	return &up.Float64, nil
}

// DefaultMaxPoints is how many points a series returns when the caller does
// not say.
const DefaultMaxPoints = 1000

// MaxSeriesPoints caps it. Past a couple of thousand points a latency chart is
// drawing several samples per pixel.
const MaxSeriesPoints = 5000

// Sample is one bucket of a decimated latency series.
//
// Four numbers per bucket rather than one, because a mean alone hides exactly
// what the chart is for: MaxLatencyMS keeps a single 900 ms spike visible
// inside a bucket of otherwise 4 ms probes, and Downs keeps a brief outage
// from being averaged away.
type Sample struct {
	At           time.Time
	AvgLatencyMS *float64
	MaxLatencyMS int64
	Checks       int64
	Downs        int64
}

// HeartbeatSeries returns at most maxPoints buckets covering [from, to].
//
// Decimation is done by SQLite, not by reading everything and thinning it in
// Go: a 90-day window at a 10 s interval is 777 000 rows, and the caller asked
// for a thousand points. Bucketing on (checked_at - from) / width keeps the
// bucket boundaries aligned to the requested window rather than to the epoch,
// so panning the chart does not reshuffle which samples land together.
func (s *Store) HeartbeatSeries(ctx context.Context, deviceID int64, from, to time.Time, maxPoints int) ([]Sample, error) {
	if maxPoints <= 0 {
		maxPoints = DefaultMaxPoints
	}
	if maxPoints > MaxSeriesPoints {
		maxPoints = MaxSeriesPoints
	}
	width := int64(to.Sub(from).Seconds()) / int64(maxPoints)
	if width < 1 {
		width = 1
	}

	var out []Sample
	err := s.eachRow(ctx, `
		SELECT MIN(checked_at),
		       AVG(CASE WHEN status = 'UP' THEN latency_ms END),
		       MAX(latency_ms),
		       COUNT(*),
		       COALESCE(SUM(status = 'DOWN'), 0)
		FROM heartbeats
		WHERE device_id = ? AND checked_at >= ? AND checked_at <= ?
		GROUP BY (checked_at - ?) / ?
		ORDER BY 1`,
		[]any{deviceID, ts(from), ts(to), ts(from), width},
		func(rows *sql.Rows) error {
			var at int64
			var avg sql.NullFloat64
			var sm Sample
			if err := rows.Scan(&at, &avg, &sm.MaxLatencyMS, &sm.Checks, &sm.Downs); err != nil {
				return err
			}
			sm.At = toTime(at)
			if avg.Valid {
				sm.AvgLatencyMS = &avg.Float64
			}
			out = append(out, sm)
			return nil
		})
	return out, err
}

// RecentChecks returns the last `limit` check outcomes for each device, oldest
// first.
//
// One query for every device on the dashboard rather than one per device: a
// row strip that cost a request each would be 200 requests on a full page, and
// it is refetched every time any device changes state.
//
// Only the outcome is returned, not the latency — this feeds a strip whose
// whole job is "has this been steady?", and the latency of a check that
// succeeded three minutes ago is on the device's own page.
func (s *Store) RecentChecks(ctx context.Context, deviceIDs []int64, limit int) (map[int64][]model.Status, error) {
	if len(deviceIDs) == 0 {
		return map[int64][]model.Status{}, nil
	}
	if limit <= 0 {
		limit = 40
	}

	in := inClause(len(deviceIDs))
	args := make([]any, 0, len(deviceIDs)+1)
	for _, id := range deviceIDs {
		args = append(args, id)
	}
	args = append(args, limit)

	out := make(map[int64][]model.Status, len(deviceIDs))
	err := s.eachRow(ctx, `
		SELECT device_id, status FROM (
			SELECT device_id, status, checked_at,
			       ROW_NUMBER() OVER (
			           PARTITION BY device_id ORDER BY checked_at DESC, id DESC
			       ) AS rn
			FROM heartbeats
			WHERE device_id IN (`+in+`)
		)
		WHERE rn <= ?
		ORDER BY device_id, checked_at, rn DESC`,
		args,
		func(rows *sql.Rows) error {
			var id int64
			var status string
			if err := rows.Scan(&id, &status); err != nil {
				return err
			}
			out[id] = append(out[id], model.Status(status))
			return nil
		})
	return out, err
}
