package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/model"
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
