//go:build windows

package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"golang.org/x/sys/windows/svc/eventlog"
)

// eventIDs group entries in Event Viewer. Fixed numbers rather than a hash of
// the message, so a filter or a monitoring rule can pin one.
const (
	eventIDError   = 1
	eventIDWarning = 2
	eventIDInfo    = 3
)

// eventLogHandler forwards serious records to the Windows Event Log.
//
// A service has no console and its log file lives under ProgramData, which is
// not where anyone looks first when a service will not start. The Event Log is
// where Windows administrators already look, so warnings and errors go there
// too — deliberately only those, because a monitor that writes an Info entry
// per probe would make the System log useless.
type eventLogHandler struct {
	log   *eventlog.Log
	level slog.Level
	attrs []slog.Attr
	group string
}

func newEventLogHandler(source string, level slog.Level) (slog.Handler, io.Closer, error) {
	el, err := eventlog.Open(source)
	if err != nil {
		// The source is registered by `monitor-service install`. Running
		// without it — in dev, or before installation — is not an error worth
		// failing startup over; the file and console sinks still work.
		return nil, nil, err
	}
	return &eventLogHandler{log: el, level: level}, el, nil
}

func (h *eventLogHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *eventLogHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)

	write := func(a slog.Attr) bool {
		if h.group != "" {
			b.WriteString(" " + h.group + ".")
		} else {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s=%v", a.Key, a.Value)
		return true
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)

	msg := b.String()
	switch {
	case r.Level >= slog.LevelError:
		return h.log.Error(eventIDError, msg)
	case r.Level >= slog.LevelWarn:
		return h.log.Warning(eventIDWarning, msg)
	default:
		return h.log.Info(eventIDInfo, msg)
	}
}

func (h *eventLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *eventLogHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.group = name
	return &next
}
