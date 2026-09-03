package store

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrDuplicate is returned when a write violates a UNIQUE constraint: a second
// device on the same ip:port, or a second group with the same name. The API
// turns it into a 409 rather than a 500.
var ErrDuplicate = errors.New("duplicate")

// ErrConstraint is returned when a write violates a CHECK or FOREIGN KEY
// constraint — an out-of-range port, or a group_id that does not exist. The
// API turns it into a 422.
var ErrConstraint = errors.New("constraint violation")

// mapErr classifies a driver error so callers above the store never have to
// match on SQLite message text.
//
// Matching on the message rather than a driver error code is deliberate: the
// alternative is importing modernc.org/sqlite's error type here, and the
// message wording of these three constraint classes is stable across SQLite
// versions in a way that the extended result codes are not worth the coupling.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"),
		strings.Contains(msg, "PRIMARY KEY must be unique"):
		return fmt.Errorf("%w: %s", ErrDuplicate, msg)
	case strings.Contains(msg, "CHECK constraint failed"),
		strings.Contains(msg, "FOREIGN KEY constraint failed"),
		strings.Contains(msg, "NOT NULL constraint failed"):
		return fmt.Errorf("%w: %s", ErrConstraint, msg)
	}
	return err
}
