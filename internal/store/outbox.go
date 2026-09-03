package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/model"
)

// Enqueue adds a mail to the durable outbox, due immediately.
func (s *Store) Enqueue(ctx context.Context, a model.Alert) (int64, error) {
	now := time.Now()
	if a.NextAttemptAt.IsZero() {
		a.NextAttemptAt = now
	}
	res, err := s.w.ExecContext(ctx, `
		INSERT INTO alert_outbox (incident_id, kind, subject, body_text, body_html,
			recipients, next_attempt_at, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		nint64(a.IncidentID), string(a.Kind), a.Subject, a.BodyText, a.BodyHTML,
		a.Recipients, ts(a.NextAttemptAt), ts(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DueAlerts returns unsent mails whose next attempt has come around.
func (s *Store) DueAlerts(ctx context.Context, now time.Time, limit int) ([]model.Alert, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.r.QueryContext(ctx, `
		SELECT id, incident_id, kind, subject, body_text, COALESCE(body_html,''),
		       recipients, attempts, next_attempt_at, last_error, created_at
		FROM alert_outbox
		WHERE sent_at IS NULL AND next_attempt_at <= ?
		ORDER BY next_attempt_at
		LIMIT ?`, ts(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Alert
	for rows.Next() {
		var a model.Alert
		var incID sql.NullInt64
		var kind string
		var next, created int64
		if err := rows.Scan(&a.ID, &incID, &kind, &a.Subject, &a.BodyText, &a.BodyHTML,
			&a.Recipients, &a.Attempts, &next, &a.LastError, &created); err != nil {
			return nil, err
		}
		a.IncidentID = toInt64Ptr(incID)
		a.Kind = model.AlertKind(kind)
		a.NextAttemptAt = toTime(next)
		a.CreatedAt = toTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkSent records a successful delivery.
func (s *Store) MarkSent(ctx context.Context, id int64, at time.Time) error {
	_, err := s.w.ExecContext(ctx,
		`UPDATE alert_outbox SET sent_at = ?, attempts = attempts + 1, last_error = '' WHERE id = ?`,
		ts(at), id)
	return err
}

// backoff is the retry schedule for a failed send. Past the last step the row
// is left for the operator to see rather than retried forever.
var backoff = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	1 * time.Hour,
	4 * time.Hour,
	4 * time.Hour,
	4 * time.Hour,
	4 * time.Hour,
}

// MaxAttempts is the point at which an alert stops being retried.
var MaxAttempts = len(backoff)

// MarkFailed records a failed attempt and schedules the next one. It reports
// whether the alert has been given up on.
func (s *Store) MarkFailed(ctx context.Context, id int64, attempts int, sendErr error, now time.Time) (exhausted bool, err error) {
	msg := sendErr.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	next := attempts // attempts is the count *before* this failure
	if next >= len(backoff) {
		// Out of retries: stop scheduling, keep the row and its last error.
		_, err = s.w.ExecContext(ctx,
			`UPDATE alert_outbox SET attempts = attempts + 1, last_error = ? WHERE id = ?`,
			msg, id)
		return true, err
	}
	_, err = s.w.ExecContext(ctx,
		`UPDATE alert_outbox SET attempts = attempts + 1, last_error = ?, next_attempt_at = ? WHERE id = ?`,
		msg, ts(now.Add(backoff[next])), id)
	return false, err
}

// PendingAlerts counts queued mail, for /api/health.
func (s *Store) PendingAlerts(ctx context.Context) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alert_outbox WHERE sent_at IS NULL`).Scan(&n)
	return n, err
}

// RecipientList splits a stored recipient string.
func RecipientList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
