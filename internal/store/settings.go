package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/gkgraphite/device-status-monitor/internal/model"
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
