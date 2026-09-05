//go:build !windows

package logx

import (
	"errors"
	"io"
	"log/slog"
)

// newEventLogHandler has nothing to attach to off Windows. The caller treats
// the error as "no Event Log here" and carries on with the file sink.
func newEventLogHandler(string, slog.Level) (slog.Handler, io.Closer, error) {
	return nil, nil, errors.New("the Event Log is Windows-only")
}
