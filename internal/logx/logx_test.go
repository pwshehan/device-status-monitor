package logx

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// recorder is a handler that keeps what it was given, so the fan-out can be
// checked without a real Event Log.
type recorder struct {
	level slog.Level

	mu      sync.Mutex
	records []slog.Record
	attrs   []slog.Attr
}

func (r *recorder) Enabled(_ context.Context, l slog.Level) bool { return l >= r.level }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
	return nil
}

func (r *recorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attrs = append(r.attrs, attrs...)
	return r
}

func (r *recorder) WithGroup(string) slog.Handler { return r }

func (r *recorder) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, rec.Message)
	}
	return out
}

func TestFanOutRespectsEachHandlersLevel(t *testing.T) {
	// The shape the service runs with: everything to the file, warnings and
	// worse also to the Event Log. An Info entry per probe would fill the
	// Application log and make it useless for everyone else on the machine.
	verbose := &recorder{level: slog.LevelDebug}
	serious := &recorder{level: slog.LevelWarn}

	log := slog.New(newFanOut([]slog.Handler{verbose, serious}))

	log.Debug("probing")
	log.Info("device up")
	log.Warn("device down")
	log.Error("cannot open database")

	if got := len(verbose.messages()); got != 4 {
		t.Errorf("file sink saw %d records, want all 4: %v", got, verbose.messages())
	}
	if got := serious.messages(); len(got) != 2 {
		t.Errorf("event log sink saw %v, want only the warning and the error", got)
	} else if got[0] != "device down" || got[1] != "cannot open database" {
		t.Errorf("event log sink saw %v, want the warning and the error", got)
	}
}

func TestFanOutKeepsAttributesPerHandler(t *testing.T) {
	a := &recorder{level: slog.LevelDebug}
	b := &recorder{level: slog.LevelDebug}

	log := slog.New(newFanOut([]slog.Handler{a, b})).With("service", "monitor")
	log.Error("failed")

	// Both sinks must see the record; a handler that swallowed it because
	// another one already had it would lose half the log.
	if len(a.messages()) != 1 || len(b.messages()) != 1 {
		t.Errorf("records = %v / %v, want one each", a.messages(), b.messages())
	}
}

func TestSetupWritesToTheFileAndClosesCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "monitor.log")

	log, closer := Setup(Options{File: path, Level: "info"})
	log.Info("engine started", "devices", 3)
	log.Debug("this one is below the level")

	if closer == nil {
		t.Fatal("no closer returned; the file handle would leak on shutdown")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(body), "engine started") {
		t.Errorf("log does not contain the message: %q", body)
	}
	if strings.Contains(string(body), "below the level") {
		t.Errorf("debug record written at info level: %q", body)
	}
}

func TestSetupSurvivesAMissingEventLogSource(t *testing.T) {
	// Before `monitor-service install` has registered the source — and always,
	// off Windows — the Event Log sink cannot open. That must not stop the
	// service starting, because the file sink is the one that matters.
	path := filepath.Join(t.TempDir(), "monitor.log")

	log, closer := Setup(Options{
		File:           path,
		Level:          "info",
		EventLogSource: "LocalMonitorSvc-does-not-exist",
	})
	if log == nil {
		t.Fatal("no logger returned")
	}
	log.Error("still writes")
	if closer != nil {
		_ = closer.Close()
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "still writes") {
		t.Errorf("file sink stopped working when the Event Log was unavailable: %q", body)
	}
}
