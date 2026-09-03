// Package logx sets up structured logging. A Windows service has no console,
// so the file sink is the primary one and the console sink only exists in dev
// and CLI modes.
package logx

import (
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
}

// Setup builds a logger, installs it as the default and returns a closer for
// the file sink.
func Setup(o Options) (*slog.Logger, io.Closer) {
	var sinks []io.Writer
	var closer io.Closer

	if o.File != "" {
		rot := &lumberjack.Logger{
			Filename:   o.File,
			MaxSize:    10, // MB
			MaxBackups: 7,
			MaxAge:     30, // days
			Compress:   true,
		}
		sinks = append(sinks, rot)
		closer = rot
	}
	if o.Console || len(sinks) == 0 {
		sinks = append(sinks, os.Stderr)
	}

	h := slog.NewTextHandler(io.MultiWriter(sinks...), &slog.HandlerOptions{
		Level: parseLevel(o.Level),
	})
	l := slog.New(h)
	slog.SetDefault(l)
	return l, closer
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
