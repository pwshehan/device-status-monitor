package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/model"
)

const groupCols = `id, name, description, color, sort_order,
	check_interval_sec, timeout_sec, failure_threshold, recovery_threshold,
	notify, recipients, paused_until, created_at, updated_at`

func scanGroup(sc interface{ Scan(...any) error }) (model.Group, error) {
	var g model.Group
	var interval, timeout, failT, recT, paused sql.NullInt64
	var recipients sql.NullString
	var notify int
	var created, updated int64

	err := sc.Scan(&g.ID, &g.Name, &g.Description, &g.Color, &g.SortOrder,
		&interval, &timeout, &failT, &recT,
		&notify, &recipients, &paused, &created, &updated)
	if err != nil {
		return g, err
	}
	g.CheckIntervalSec = toIntPtr(interval)
	g.TimeoutSec = toIntPtr(timeout)
	g.FailureThreshold = toIntPtr(failT)
	g.RecoveryThreshold = toIntPtr(recT)
	g.Notify = notify != 0
	g.Recipients = toStrPtr(recipients)
	g.PausedUntil = toTimePtr(paused)
	g.CreatedAt = toTime(created)
	g.UpdatedAt = toTime(updated)
	return g, nil
}

// ListGroups returns every group in display order.
func (s *Store) ListGroups(ctx context.Context) ([]model.Group, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+groupCols+` FROM groups ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetGroup returns one group by id.
func (s *Store) GetGroup(ctx context.Context, id int64) (model.Group, error) {
	g, err := scanGroup(s.r.QueryRowContext(ctx,
		`SELECT `+groupCols+` FROM groups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// CreateGroup inserts a group and returns it with its assigned id.
func (s *Store) CreateGroup(ctx context.Context, g model.Group) (model.Group, error) {
	now := time.Now()
	res, err := s.w.ExecContext(ctx, `
		INSERT INTO groups (name, description, color, sort_order,
			check_interval_sec, timeout_sec, failure_threshold, recovery_threshold,
			notify, recipients, paused_until, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		g.Name, g.Description, g.Color, g.SortOrder,
		nint(g.CheckIntervalSec), nint(g.TimeoutSec), nint(g.FailureThreshold), nint(g.RecoveryThreshold),
		b2i(g.Notify), nstr(g.Recipients), nts(g.PausedUntil), ts(now), ts(now))
	if err != nil {
		return g, mapErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return g, err
	}
	return s.GetGroup(ctx, id)
}

// UpdateGroup writes every mutable field of g.
func (s *Store) UpdateGroup(ctx context.Context, g model.Group) (model.Group, error) {
	res, err := s.w.ExecContext(ctx, `
		UPDATE groups SET name=?, description=?, color=?, sort_order=?,
			check_interval_sec=?, timeout_sec=?, failure_threshold=?, recovery_threshold=?,
			notify=?, recipients=?, paused_until=?, updated_at=?
		WHERE id=?`,
		g.Name, g.Description, g.Color, g.SortOrder,
		nint(g.CheckIntervalSec), nint(g.TimeoutSec), nint(g.FailureThreshold), nint(g.RecoveryThreshold),
		b2i(g.Notify), nstr(g.Recipients), nts(g.PausedUntil), ts(time.Now()), g.ID)
	if err != nil {
		return g, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return g, ErrNotFound
	}
	return s.GetGroup(ctx, g.ID)
}

// DeleteGroup removes a group. Its devices survive and become ungrouped: the
// foreign key is ON DELETE SET NULL, never CASCADE. Deleting a label must
// never delete the things it labelled. The count of orphaned devices is
// returned so the caller can say so out loud.
func (s *Store) DeleteGroup(ctx context.Context, id int64) (orphaned int64, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM devices WHERE group_id = ?`, id).Scan(&orphaned); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return orphaned, tx.Commit()
}

// ReorderGroups writes sort_order to match the given id order.
func (s *Store) ReorderGroups(ctx context.Context, ids []int64) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `UPDATE groups SET sort_order=?, updated_at=? WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := ts(time.Now())
	for i, id := range ids {
		if _, err := stmt.ExecContext(ctx, i, now, id); err != nil {
			return fmt.Errorf("reorder group %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// PauseGroup sets or clears a group's maintenance window. Passing nil resumes.
//
// This never touches member rows: a device that was individually paused before
// the window opened must still be paused after the window closes.
func (s *Store) PauseGroup(ctx context.Context, id int64, until *time.Time) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE groups SET paused_until=?, updated_at=? WHERE id=?`,
		nts(until), ts(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
