// Package store owns the SQLite database: connection setup, migrations and all
// queries. It is the only package that speaks SQL.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, so the Windows build cross-compiles
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store holds two handles to the same file.
//
// SQLite permits exactly one writer, so rather than letting a pool of
// connections fight over SQLITE_BUSY, writes go through a handle capped at one
// connection and reads get a normal pool. WAL is what lets those readers keep
// working while a write is in flight.
type Store struct {
	w    *sql.DB
	r    *sql.DB
	path string
}

const pragmas = "?_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)"

// Open connects to the database at path, applies pragmas and runs migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path + pragmas

	w, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)

	r, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open reader: %w", err)
	}
	r.SetMaxOpenConns(4)

	s := &Store{w: w, r: r, path: path}
	if err := w.PingContext(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close releases both handles after checkpointing the WAL.
func (s *Store) Close() error {
	if s.w != nil {
		_, _ = s.w.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	var errs []string
	if s.r != nil {
		if err := s.r.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if s.w != nil {
		if err := s.w.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Ping reports whether the database is reachable, for /api/health.
func (s *Store) Ping(ctx context.Context) error { return s.r.PingContext(ctx) }

// migrate applies any embedded migration whose version exceeds user_version.
// Each migration runs in its own transaction, so a failure leaves the previous
// version intact rather than half-applied.
func (s *Store) migrate(ctx context.Context) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var current int
	if err := s.w.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	for i, name := range names {
		version := i + 1
		if version <= current {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.w.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%s: %w", name, err)
		}
		// PRAGMA does not accept a bound parameter.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%s: set user_version: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("%s: commit: %w", name, err)
		}
	}
	return nil
}

// SchemaVersion returns the applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.r.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v)
	return v, err
}

// --- timestamp helpers -------------------------------------------------------
//
// Timestamps are stored as INTEGER unix-epoch seconds in UTC. Text datetimes
// are timezone-ambiguous, sort lexically and index larger.

func ts(t time.Time) int64 { return t.UTC().Unix() }

func nts(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Unix()
}

func toTime(v int64) time.Time { return time.Unix(v, 0).UTC() }

func toTimePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := toTime(v.Int64)
	return &t
}

func toIntPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}

func toInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	i := v.Int64
	return &i
}

func toStrPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	str := v.String
	return &str
}

func nint(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nint64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nstr(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
