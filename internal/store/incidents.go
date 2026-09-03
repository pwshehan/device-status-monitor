package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/model"
)

const incidentCols = `id, device_id, started_at, detected_at, resolved_at,
	duration_sec, cause, alert_sent, recovery_sent`

func scanIncident(sc interface{ Scan(...any) error }) (model.Incident, error) {
	var inc model.Incident
	var resolved, duration sql.NullInt64
	var alertSent, recoverySent int
	var started, detected int64

	err := sc.Scan(&inc.ID, &inc.DeviceID, &started, &detected, &resolved,
		&duration, &inc.Cause, &alertSent, &recoverySent)
	if err != nil {
		return inc, err
	}
	inc.StartedAt = toTime(started)
	inc.DetectedAt = toTime(detected)
	inc.ResolvedAt = toTimePtr(resolved)
	inc.DurationSec = toInt64Ptr(duration)
	inc.AlertSent = alertSent != 0
	inc.RecoverySent = recoverySent != 0
	return inc, nil
}

// OpenIncident records the start of an outage. startedAt is the first failed
// probe of the streak, not the probe that tripped the threshold, so the
// duration reported on recovery is the real duration.
func (s *Store) OpenIncident(ctx context.Context, deviceID int64, startedAt, detectedAt time.Time, cause string, alertSent bool) (int64, error) {
	var lastAlert any
	if alertSent {
		lastAlert = ts(detectedAt)
	}
	res, err := s.w.ExecContext(ctx, `
		INSERT INTO incidents (device_id, started_at, detected_at, cause, alert_sent, last_alert_at)
		VALUES (?,?,?,?,?,?)`,
		deviceID, ts(startedAt), ts(detectedAt), cause, b2i(alertSent), lastAlert)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CloseIncident resolves an outage and stores its duration.
func (s *Store) CloseIncident(ctx context.Context, id int64, resolvedAt time.Time, recoverySent bool) error {
	_, err := s.w.ExecContext(ctx, `
		UPDATE incidents
		SET resolved_at = ?, duration_sec = ? - started_at, recovery_sent = ?
		WHERE id = ? AND resolved_at IS NULL`,
		ts(resolvedAt), ts(resolvedAt), b2i(recoverySent), id)
	return err
}

// MarkIncidentAlerted records that a DOWN mail was queued, for reminders.
func (s *Store) MarkIncidentAlerted(ctx context.Context, id int64, at time.Time) error {
	_, err := s.w.ExecContext(ctx,
		`UPDATE incidents SET alert_sent = 1, last_alert_at = ? WHERE id = ?`, ts(at), id)
	return err
}

// OpenIncidentFor returns the device's unresolved incident, if any.
func (s *Store) OpenIncidentFor(ctx context.Context, deviceID int64) (model.Incident, error) {
	inc, err := scanIncident(s.r.QueryRowContext(ctx,
		`SELECT `+incidentCols+` FROM incidents
		 WHERE device_id = ? AND resolved_at IS NULL
		 ORDER BY started_at DESC LIMIT 1`, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return inc, ErrNotFound
	}
	return inc, err
}

// Incidents returns a device's outage log, newest first.
func (s *Store) Incidents(ctx context.Context, deviceID int64, limit int) ([]model.Incident, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+incidentCols+` FROM incidents
		 WHERE device_id = ? ORDER BY started_at DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// CountOpenIncidents returns how many devices are currently in an outage.
func (s *Store) CountOpenIncidents(ctx context.Context) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM incidents WHERE resolved_at IS NULL`).Scan(&n)
	return n, err
}
