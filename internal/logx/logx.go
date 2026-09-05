// Package logx sets up structured logging. A Windows service has no console,
// so the file sink is the primary one and the console sink only exists in dev
// and CLI modes.
package logx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Options configures the logger.
type Options struct {
	File    string // rotating log file; "" disables the file sink
	Console bool   // also write to stderr
	Level   string // debug | info | warn | error

	// EventLogSource enables the Windows Event Log sink. Set when running as a
	// service, where nobody is watching a console and the log file is in a
	// folder an administrator would have to be told about.
	//
	// Only warnings and errors go there: an Info entry per probe would fill
	// the Application log and make it useless for everyone else on the
	// machine.
	EventLogSource string
}

// Setup builds a logger, installs it as the default and returns a closer for
// its sinks.
func Setup(o Options) (*slog.Logger, io.Closer) {
	var sinks []io.Writer
	var closers []io.Closer

	if o.File != "" {
		rot := &lumberjack.Logger{
			Filename:   o.File,
			MaxSize:    10, // MB
			MaxBackups: 7,
			MaxAge:     30, // days
			Compress:   true,
		}
		sinks = append(sinks, rot)
		closers = append(closers, rot)
	}
	if o.Console || len(sinks) == 0 {
		sinks = append(sinks, os.Stderr)
	}

	handlers := []slog.Handler{
		slog.NewTextHandler(io.MultiWriter(sinks...), &slog.HandlerOptions{
			Level: parseLevel(o.Level),
		}),
	}

	if o.EventLogSource != "" {
		// A missing source is the normal case before `monitor-service install`
		// has run, so it is not worth failing startup over — the file sink is
		// still there, and this one silently does not exist.
		if h, closer, err := newEventLogHandler(o.EventLogSource, slog.LevelWarn); err == nil {
			handlers = append(handlers, h)
			closers = append(closers, closer)
		}
	}

	l := slog.New(newFanOut(handlers))
	slog.SetDefault(l)
	return l, multiCloser(closers)
}

// fanOut sends every record to each handler that wants it.
//
// slog has no built-in multiplexer, and the alternative — one io.Writer that
// fans out — cannot work here, because the Event Log needs the record's level
// to choose an entry type and a severity, not a formatted line.
type fanOut struct{ handlers []slog.Handler }

func newFanOut(handlers []slog.Handler) slog.Handler {
	if len(handlers) == 1 {
		return handlers[0]
	}
	return &fanOut{handlers: handlers}
}

func (f *fanOut) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f *fanOut) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		// Every handler gets its own clone: Handle may retain the record, and
		// a shared one would be a data race waiting to happen.
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f *fanOut) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &fanOut{handlers: next}
}

func (f *fanOut) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return &fanOut{handlers: next}
}

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var firstErr error
	for _, c := range m {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
