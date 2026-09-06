package notify

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/model"
	"github.com/pwshehan/device-status-monitor/internal/store"
)

// cancellingSender delivers the mail and then cancels the context, standing in
// for a service shutdown that lands in the window between the mail leaving and
// its delivery being recorded.
type cancellingSender struct {
	cancel context.CancelFunc
	sent   int
}

func (c *cancellingSender) Send(context.Context, Config, Message) error {
	c.sent++
	c.cancel()
	return nil
}

// TestDeliveredMailIsRecordedEvenIfShutdownRacesIt covers the one window in
// which an alert can be delivered twice.
//
// The send has already happened by the time sent_at is written, so a cancelled
// context there does not undo anything — it only loses the *record*, and the
// row is picked up and sent again on the next start. A duplicate outage email
// after a restart is a small thing, but it is the kind of small thing that
// teaches people to distrust the alerts.
func TestDeliveredMailIsRecordedEvenIfShutdownRacesIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SeedSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSettings(context.Background(), map[string]string{
		store.KeySMTPHost:     "127.0.0.1",
		store.KeySMTPPort:     "2525",
		store.KeySMTPSecurity: SecurityNone,
		store.KeySMTPFrom:     "monitor@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := st.Enqueue(context.Background(), model.Alert{
		Kind: model.AlertDown, Subject: "[DOWN] switch",
		BodyText: "down", Recipients: "ops@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	sender := &cancellingSender{cancel: cancel}
	w := &Worker{
		Store: st, Sender: sender,
		KeyPath: filepath.Join(t.TempDir(), "secret.key"),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	w.drain(ctx)

	if sender.sent != 1 {
		t.Fatalf("sender called %d times, want 1", sender.sent)
	}

	// The mail is gone; the row must say so, or the next start sends it again.
	due, err := st.DueAlerts(context.Background(), time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("%d alerts still queued after delivery — a restart would send them again",
			len(due))
	}
}
