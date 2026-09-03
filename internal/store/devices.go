package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/model"
	"github.com/gkgraphite/device-status-monitor/internal/state"
)

const deviceCols = `id, name, ip_address, port, group_id,
	check_interval_sec, timeout_sec, failure_threshold, recovery_threshold,
	enabled, notify, paused_until, tags,
	status, consecutive_failures, consecutive_successes, first_failure_at,
	last_check_at, last_latency_ms, last_error, last_status_change_at,
	created_at, updated_at`

func scanDevice(sc interface{ Scan(...any) error }) (model.Device, error) {
	var d model.Device
	var groupID, interval, timeout, failT, recT, paused sql.NullInt64
	var firstFail, lastCheck, lastLatency, lastChange sql.NullInt64
	var enabled, notify int
	var created, updated int64

	err := sc.Scan(&d.ID, &d.Name, &d.IPAddress, &d.Port, &groupID,
		&interval, &timeout, &failT, &recT,
		&enabled, &notify, &paused, &d.Tags,
		&d.Status, &d.ConsecutiveFailures, &d.ConsecutiveSuccesses, &firstFail,
		&lastCheck, &lastLatency, &d.LastError, &lastChange,
		&created, &updated)
	if err != nil {
		return d, err
	}
	d.GroupID = toInt64Ptr(groupID)
	d.CheckIntervalSec = toIntPtr(interval)
	d.TimeoutSec = toIntPtr(timeout)
	d.FailureThreshold = toIntPtr(failT)
	d.RecoveryThreshold = toIntPtr(recT)
	d.Enabled = enabled != 0
	d.Notify = notify != 0
	d.PausedUntil = toTimePtr(paused)
	d.FirstFailureAt = toTimePtr(firstFail)
	d.LastCheckAt = toTimePtr(lastCheck)
	d.LastLatencyMS = toInt64Ptr(lastLatency)
	d.LastStatusChangeAt = toTimePtr(lastChange)
	d.CreatedAt = toTime(created)
	d.UpdatedAt = toTime(updated)
	return d, nil
}

// DeviceFilter narrows a device listing. Zero values mean "no filter".
type DeviceFilter struct {
	GroupID     *int64
	Ungrouped   bool // only devices with no group
	Status      model.Status
	Tag         string
	EnabledOnly bool
}

// ListDevices returns devices matching f, grouped then ordered by name.
func (s *Store) ListDevices(ctx context.Context, f DeviceFilter) ([]model.Device, error) {
	var where []string
	var args []any

	if f.GroupID != nil {
		where = append(where, "group_id = ?")
		args = append(args, *f.GroupID)
	}
	if f.Ungrouped {
		where = append(where, "group_id IS NULL")
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.Tag != "" {
		// Tags are a comma-separated list; pad both sides so a match is exact
		// rather than a substring of a longer tag.
		where = append(where, "(',' || REPLACE(tags, ', ', ',') || ',') LIKE ?")
		args = append(args, "%,"+f.Tag+",%")
	}
	if f.EnabledOnly {
		where = append(where, "enabled = 1")
	}

	q := `SELECT ` + deviceCols + ` FROM devices`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY group_id IS NULL, group_id, name`

	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDevice returns one device by id.
func (s *Store) GetDevice(ctx context.Context, id int64) (model.Device, error) {
	d, err := scanDevice(s.r.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// CreateDevice inserts a device and returns it with its assigned id.
func (s *Store) CreateDevice(ctx context.Context, d model.Device) (model.Device, error) {
	now := time.Now()
	res, err := s.w.ExecContext(ctx, `
		INSERT INTO devices (name, ip_address, port, group_id,
			check_interval_sec, timeout_sec, failure_threshold, recovery_threshold,
			enabled, notify, paused_until, tags, status, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.Name, d.IPAddress, d.Port, nint64(d.GroupID),
		nint(d.CheckIntervalSec), nint(d.TimeoutSec), nint(d.FailureThreshold), nint(d.RecoveryThreshold),
		b2i(d.Enabled), b2i(d.Notify), nts(d.PausedUntil), d.Tags,
		string(model.StatusUnknown), ts(now), ts(now))
	if err != nil {
		return d, mapErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return d, err
	}
	return s.GetDevice(ctx, id)
}

// UpdateDevice writes every configurable field of d, leaving live state alone.
func (s *Store) UpdateDevice(ctx context.Context, d model.Device) (model.Device, error) {
	res, err := s.w.ExecContext(ctx, `
		UPDATE devices SET name=?, ip_address=?, port=?, group_id=?,
			check_interval_sec=?, timeout_sec=?, failure_threshold=?, recovery_threshold=?,
			enabled=?, notify=?, paused_until=?, tags=?, updated_at=?
		WHERE id=?`,
		d.Name, d.IPAddress, d.Port, nint64(d.GroupID),
		nint(d.CheckIntervalSec), nint(d.TimeoutSec), nint(d.FailureThreshold), nint(d.RecoveryThreshold),
		b2i(d.Enabled), b2i(d.Notify), nts(d.PausedUntil), d.Tags, ts(time.Now()), d.ID)
	if err != nil {
		return d, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return d, ErrNotFound
	}
	return s.GetDevice(ctx, d.ID)
}

// DeleteDevice removes a device and, by cascade, its history.
func (s *Store) DeleteDevice(ctx context.Context, id int64) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PauseDevice sets or clears one device's own pause. Passing nil resumes.
func (s *Store) PauseDevice(ctx context.Context, id int64, until *time.Time) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE devices SET paused_until=?, updated_at=? WHERE id=?`,
		nts(until), ts(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// BulkOp names a bulk device operation.
type BulkOp string

const (
	BulkMove   BulkOp = "move"
	BulkPause  BulkOp = "pause"
	BulkResume BulkOp = "resume"
	BulkDelete BulkOp = "delete"
)

// Bulk applies one operation to many devices in a single transaction, so a
// partial failure moves nothing. This is what makes grouping usable after
// adding forty devices.
func (s *Store) Bulk(ctx context.Context, op BulkOp, ids []int64, groupID *int64, until *time.Time) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)

	var q string
	switch op {
	case BulkMove:
		q = `UPDATE devices SET group_id=?, updated_at=? WHERE id IN (` + placeholders + `)`
		args = append(args, nint64(groupID), ts(time.Now()))
	case BulkPause:
		q = `UPDATE devices SET paused_until=?, updated_at=? WHERE id IN (` + placeholders + `)`
		args = append(args, nts(until), ts(time.Now()))
	case BulkResume:
		q = `UPDATE devices SET paused_until=NULL, updated_at=? WHERE id IN (` + placeholders + `)`
		args = append(args, ts(time.Now()))
	case BulkDelete:
		q = `DELETE FROM devices WHERE id IN (` + placeholders + `)`
	default:
		return 0, fmt.Errorf("unknown bulk op %q", op)
	}
	for _, id := range ids {
		args = append(args, id)
	}

	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// LoadEffective resolves the three-tier chain for every enabled device.
//
// Groups and defaults are read once and folded in Go rather than COALESCEd in
// SQL, so model.Resolve stays the only place the precedence rules live.
func (s *Store) LoadEffective(ctx context.Context) ([]model.Effective, error) {
	res, err := s.NewResolver(ctx)
	if err != nil {
		return nil, fmt.Errorf("load inheritance tiers: %w", err)
	}
	devices, err := s.ListDevices(ctx, DeviceFilter{EnabledOnly: true})
	if err != nil {
		return nil, fmt.Errorf("load devices: %w", err)
	}

	out := make([]model.Effective, 0, len(devices))
	for _, d := range devices {
		out = append(out, res.Resolve(d))
	}
	return out, nil
}

// LoadSnapshots reads every device's live state, so a restart resumes rather
// than re-alerting. Devices with an open incident carry it forward.
func (s *Store) LoadSnapshots(ctx context.Context) (map[int64]*state.Snapshot, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT d.id, d.status, d.consecutive_failures, d.consecutive_successes,
		       d.first_failure_at,
		       i.id, i.started_at, i.alert_sent, i.last_alert_at
		FROM devices d
		LEFT JOIN incidents i ON i.device_id = d.id AND i.resolved_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]*state.Snapshot{}
	for rows.Next() {
		var id int64
		var status string
		var snap state.Snapshot
		var firstFail, incID, incStart, lastAlert sql.NullInt64
		var alertSent sql.NullInt64

		if err := rows.Scan(&id, &status, &snap.ConsecutiveFailures, &snap.ConsecutiveSuccesses,
			&firstFail, &incID, &incStart, &alertSent, &lastAlert); err != nil {
			return nil, err
		}
		snap.Status = model.Status(status)
		if t := toTimePtr(firstFail); t != nil {
			snap.FirstFailureAt = *t
		}
		if incID.Valid {
			snap.OpenIncidentID = incID.Int64
			if t := toTimePtr(incStart); t != nil {
				snap.OpenIncidentStart = *t
			}
			snap.AlertSent = alertSent.Valid && alertSent.Int64 != 0
			if t := toTimePtr(lastAlert); t != nil {
				snap.LastAlertAt = *t
			}
		}
		out[id] = &snap
	}
	return out, rows.Err()
}

// SaveState persists a device's live state after one probe.
func (s *Store) SaveState(ctx context.Context, id int64, snap state.Snapshot, res LiveResult) error {
	var statusChange any
	if res.StatusChanged {
		statusChange = ts(res.CheckedAt)
	}
	_, err := s.w.ExecContext(ctx, `
		UPDATE devices SET
			status=?, consecutive_failures=?, consecutive_successes=?,
			first_failure_at=?, last_check_at=?, last_latency_ms=?, last_error=?,
			last_status_change_at=COALESCE(?, last_status_change_at),
			updated_at=?
		WHERE id=?`,
		string(snap.Status), snap.ConsecutiveFailures, snap.ConsecutiveSuccesses,
		nts(&snap.FirstFailureAt), ts(res.CheckedAt), res.LatencyMS, res.ErrMsg,
		statusChange, ts(time.Now()), id)
	return err
}

// LiveResult is the per-probe detail SaveState records alongside the snapshot.
type LiveResult struct {
	CheckedAt     time.Time
	LatencyMS     int64
	ErrMsg        string
	StatusChanged bool
}
