package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/model"
)

// Settings keys. The default.* tier is the bottom of the effective-value chain.
const (
	KeySchemaVersion = "schema_version"

	KeySMTPHost        = "smtp.host"
	KeySMTPPort        = "smtp.port"
	KeySMTPSecurity    = "smtp.security"
	KeySMTPUsername    = "smtp.username"
	KeySMTPPasswordEnc = "smtp.password_enc"
	KeySMTPFrom        = "smtp.from"

	KeyAlertRecipients  = "alert.recipients"
	KeyAlertReminderSec = "alert.reminder_sec"

	// How long an alert waits for company before going out, and the ceiling on
	// outbound mail per hour. Both are settings rather than constants because
	// the right answer depends on how many devices there are and how much mail
	// the operator will tolerate.
	KeyAlertCollapseSec = "alert.collapse_sec"
	KeyAlertMaxPerHour  = "alert.max_per_hour"

	KeyDefaultInterval          = "default.check_interval_sec"
	KeyDefaultTimeout           = "default.timeout_sec"
	KeyDefaultFailureThreshold  = "default.failure_threshold"
	KeyDefaultRecoveryThreshold = "default.recovery_threshold"

	KeyRetentionRawDays    = "retention.raw_days"
	KeyRetentionRollupDays = "retention.rollup_days"
)

// seedDefaults are written on first start and never overwritten.
var seedDefaults = map[string]string{
	KeySMTPPort:                 "587",
	KeySMTPSecurity:             "starttls",
	KeyAlertReminderSec:         "0",
	KeyAlertCollapseSec:         "15",
	KeyAlertMaxPerHour:          "20",
	KeyDefaultInterval:          "30",
	KeyDefaultTimeout:           "3",
	KeyDefaultFailureThreshold:  "3",
	KeyDefaultRecoveryThreshold: "1",
	KeyRetentionRawDays:         "14",
	KeyRetentionRollupDays:      "400",
}

// SeedSettings inserts any missing default setting, leaving existing values
// untouched so an upgrade never resets the user's configuration.
func (s *Store) SeedSettings(ctx context.Context) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for k, v := range seedDefaults {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`,
			k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AllSettings returns every setting as a map.
func (s *Store) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// Setting returns one value, or "" when unset.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.r.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// PutSettings upserts several settings in one transaction.
func (s *Store) PutSettings(ctx context.Context, kv map[string]string) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for k, v := range kv {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Defaults reads the bottom tier of the effective-value chain. A malformed or
// missing value falls back to the built-in default rather than failing: a typo
// in one setting must not stop every device being monitored.
func (s *Store) Defaults(ctx context.Context) (model.Defaults, error) {
	all, err := s.AllSettings(ctx)
	if err != nil {
		return model.Defaults{}, err
	}
	return model.Defaults{
		CheckIntervalSec:  atoiOr(all[KeyDefaultInterval], 30),
		TimeoutSec:        atoiOr(all[KeyDefaultTimeout], 3),
		FailureThreshold:  atoiOr(all[KeyDefaultFailureThreshold], 3),
		RecoveryThreshold: atoiOr(all[KeyDefaultRecoveryThreshold], 1),
		Recipients:        all[KeyAlertRecipients],
	}, nil
}

func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// Retention is how long each kind of history is kept.
type Retention struct {
	RawDays    int
	RollupDays int
}

// Retention reads the two retention windows, falling back to the built-in
// defaults on a malformed value — a typo in one setting must not stop the
// janitor, or the database grows without bound while somebody investigates.
func (s *Store) Retention(ctx context.Context) (Retention, error) {
	all, err := s.AllSettings(ctx)
	if err != nil {
		return Retention{RawDays: 14, RollupDays: 400}, err
	}
	return Retention{
		RawDays:    atoiOr(all[KeyRetentionRawDays], 14),
		RollupDays: atoiOr(all[KeyRetentionRollupDays], 400),
	}, nil
}

// AlertPolicy is the collapse and rate-limit configuration.
type AlertPolicy struct {
	// CollapseWindow is how long an alert waits for company before going out.
	// Zero disables collapsing entirely, and every device alerts on its own.
	CollapseWindow time.Duration

	// MaxPerHour caps outbound mail. Zero means no cap.
	MaxPerHour int
}

// AlertPolicy reads the collapse window and the hourly mail cap.
func (s *Store) AlertPolicy(ctx context.Context) (AlertPolicy, error) {
	all, err := s.AllSettings(ctx)
	if err != nil {
		return AlertPolicy{CollapseWindow: 15 * time.Second, MaxPerHour: 20}, err
	}
	return AlertPolicy{
		CollapseWindow: time.Duration(atoiOrZero(all[KeyAlertCollapseSec], 15)) * time.Second,
		MaxPerHour:     atoiOrZero(all[KeyAlertMaxPerHour], 20),
	}, nil
}

// AlertsSentSince counts mail queued in a window, for the rate limit.
//
// Counted from the outbox rather than an in-memory tally so a restart cannot
// be used — accidentally — to reset the budget and let a flapping device send
// another twenty mails.
func (s *Store) AlertsSentSince(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alert_outbox WHERE created_at >= ?`, ts(since)).Scan(&n)
	return n, err
}

// atoiOrZero is atoiOr but accepts 0 as a meaningful value: for a collapse
// window or a mail cap, zero means "off" rather than "use the default".
func atoiOrZero(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
