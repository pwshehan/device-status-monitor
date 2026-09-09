package notify

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/pwshehan/local-device-monitor/internal/model"
	"github.com/pwshehan/local-device-monitor/internal/secret"
	"github.com/pwshehan/local-device-monitor/internal/store"
)

// Worker drains the durable outbox.
//
// The queue is the point: when the network the monitor watches goes down, the
// mail saying so cannot be delivered either. Queueing and retrying means the
// alert arrives late instead of never.
type Worker struct {
	Store    *store.Store
	Sender   Sender
	KeyPath  string        // for unsealing the SMTP password
	Interval time.Duration // poll interval, default 30s
	Log      *slog.Logger
}

// Run polls until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	log := w.logger()

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := w.drain(ctx); n > 0 {
				log.Debug("outbox drained", "sent", n)
			}
		}
	}
}

// drain sends everything currently due and returns how many went out.
func (w *Worker) drain(ctx context.Context) int {
	log := w.logger()
	now := time.Now()

	due, err := w.Store.DueAlerts(ctx, now, 20)
	if err != nil {
		log.Error("read outbox", "err", err)
		return 0
	}
	if len(due) == 0 {
		return 0
	}

	cfg, err := w.Config(ctx)
	if err != nil {
		// Unconfigured SMTP is a configuration problem, not a delivery failure:
		// burning retry attempts on it would exhaust the queue before anyone
		// fixes the settings. Leave the rows due and say so once per pass.
		log.Warn("alerts queued but SMTP is not usable", "queued", len(due), "err", err)
		return 0
	}

	var sent int
	for _, a := range due {
		if ctx.Err() != nil {
			return sent
		}
		err := w.Sender.Send(ctx, cfg, Message{
			To:      store.RecipientList(a.Recipients),
			Subject: a.Subject,
			Text:    a.BodyText,
			HTML:    a.BodyHTML,
		})
		if err == nil {
			// Recorded on a context that shutdown cannot cancel. The mail has
			// already left by this point, so cancelling here does not undo
			// anything — it only loses the record, and the row would be picked
			// up and delivered a second time on the next start. A duplicate
			// outage email after a restart is small, but it is exactly the kind
			// of small thing that teaches people to distrust the alerts.
			markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err := w.Store.MarkSent(markCtx, a.ID, time.Now())
			cancel()
			if err != nil {
				log.Error("mark alert sent", "id", a.ID, "err", err)
			}
			sent++
			log.Info("alert sent", "kind", a.Kind, "subject", a.Subject, "to", a.Recipients)
			continue
		}

		// The failure path needs no such care: losing the record of a failed
		// attempt costs one earlier retry, which is the harmless direction.
		exhausted, markErr := w.Store.MarkFailed(ctx, a.ID, a.Attempts, err, time.Now())
		if markErr != nil {
			log.Error("mark alert failed", "id", a.ID, "err", markErr)
		}
		if exhausted {
			log.Error("giving up on alert", "id", a.ID, "kind", a.Kind,
				"attempts", a.Attempts+1, "err", err)
		} else {
			log.Warn("alert send failed, will retry", "id", a.ID,
				"attempt", a.Attempts+1, "err", err)
		}
	}
	return sent
}

// Config reads and unseals the SMTP configuration from settings.
func (w *Worker) Config(ctx context.Context) (Config, error) {
	all, err := w.Store.AllSettings(ctx)
	if err != nil {
		return Config{}, err
	}
	port, _ := strconv.Atoi(all[store.KeySMTPPort])
	cfg := Config{
		Host:     all[store.KeySMTPHost],
		Port:     port,
		Security: all[store.KeySMTPSecurity],
		Username: all[store.KeySMTPUsername],
		From:     all[store.KeySMTPFrom],
	}
	if cfg.Security == "" {
		cfg.Security = SecurityStartTLS
	}

	if enc := all[store.KeySMTPPasswordEnc]; enc != "" {
		pw, err := secret.Open(w.KeyPath, enc)
		if err != nil {
			return cfg, err
		}
		cfg.Password = string(pw)
	}
	return cfg, cfg.Validate()
}

// SavePassword seals the SMTP password and stores it. The plaintext never
// reaches the database.
func (w *Worker) SavePassword(ctx context.Context, plaintext string) error {
	if plaintext == "" {
		return w.Store.PutSettings(ctx, map[string]string{store.KeySMTPPasswordEnc: ""})
	}
	sealed, err := secret.Seal(w.KeyPath, []byte(plaintext))
	if err != nil {
		return err
	}
	return w.Store.PutSettings(ctx, map[string]string{store.KeySMTPPasswordEnc: sealed})
}

// SendTest delivers a test message immediately, bypassing the outbox, and
// returns the SMTP error verbatim so the UI can show what the server said.
func (w *Worker) SendTest(ctx context.Context, host string, to []string) error {
	cfg, err := w.Config(ctx)
	if err != nil {
		return err
	}
	if len(to) == 0 {
		return errors.New("no recipients configured")
	}
	subject, text, htmlBody := TestMessage(host)
	return w.Sender.Send(ctx, cfg, Message{To: to, Subject: subject, Text: text, HTML: htmlBody})
}

// Enqueue queues an alert built from an event.
func Enqueue(ctx context.Context, st *store.Store, kind model.AlertKind, incidentID *int64,
	recipients []string, subject, text, htmlBody string) (int64, error) {

	return st.Enqueue(ctx, model.Alert{
		IncidentID: incidentID,
		Kind:       kind,
		Subject:    subject,
		BodyText:   text,
		BodyHTML:   htmlBody,
		Recipients: joinRecipients(recipients),
	})
}

func joinRecipients(to []string) string {
	out := ""
	for i, r := range to {
		if i > 0 {
			out += ","
		}
		out += r
	}
	return out
}

func (w *Worker) logger() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
