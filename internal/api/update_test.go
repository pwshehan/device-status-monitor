package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/pwshehan/local-device-monitor/internal/store"
	"github.com/pwshehan/local-device-monitor/internal/update"
)

func TestUpdateNeedsAToken(t *testing.T) {
	e := newEnv(t)

	// Unlike /api/health, which is open so a window with a stale token can
	// still tell "service down" from "token wrong". Whether a machine is
	// behind on patches is not something to hand out unauthenticated.
	res, err := http.Get(e.http.URL + "/api/update")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/update without a token = %d, want %d",
			res.StatusCode, http.StatusUnauthorized)
	}
}

func TestUpdateReportsWhatTheCheckerFound(t *testing.T) {
	e := newEnv(t)
	published := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	e.eng.setUpdate(update.Status{
		Supported:     true,
		Enabled:       true,
		AutoDownload:  true,
		Current:       "1.0.0",
		State:         update.StateReady,
		Latest:        "1.1.0",
		NotesURL:      "https://github.com/" + update.Repo + "/releases/tag/v1.1.0",
		PublishedAt:   published,
		LastCheckedAt: published.Add(time.Hour),
	})

	var got updateResponse
	e.mustCall(http.MethodGet, "/api/update", nil, &got, http.StatusOK)

	if got.State != string(update.StateReady) {
		t.Errorf("state = %q, want %q", got.State, update.StateReady)
	}
	if got.Current != "1.0.0" || got.Latest != "1.1.0" {
		t.Errorf("current/latest = %q/%q, want 1.0.0/1.1.0", got.Current, got.Latest)
	}
	if got.PublishedAt == nil || *got.PublishedAt != "2026-03-01T12:00:00Z" {
		t.Errorf("published_at = %v, want 2026-03-01T12:00:00Z", got.PublishedAt)
	}
	if got.LastCheckedAt == nil {
		t.Error("last_checked_at is null after a check")
	}
}

func TestUpdateTimestampsAreNullRatherThanZero(t *testing.T) {
	e := newEnv(t)
	e.eng.setUpdate(update.Status{Supported: true, Enabled: true, State: update.StateIdle})

	var raw map[string]json.RawMessage
	e.mustCall(http.MethodGet, "/api/update", nil, &raw, http.StatusOK)

	// The zero time rendered as a string would show up in the UI as the year 1,
	// which is how "never checked" turns into a bug report.
	for _, key := range []string{"published_at", "last_checked_at"} {
		if string(raw[key]) != "null" {
			t.Errorf("%s = %s, want null when it has not happened", key, raw[key])
		}
	}
}

func TestCheckNowAsksTheChecker(t *testing.T) {
	e := newEnv(t)
	e.eng.setUpdate(update.Status{Supported: true, Enabled: true, State: update.StateAvailable, Latest: "2.0.0"})

	var got updateResponse
	e.mustCall(http.MethodPost, "/api/update/check", nil, &got, http.StatusOK)

	e.eng.mu.Lock()
	checks := e.eng.upChecks
	e.eng.mu.Unlock()
	if checks != 1 {
		t.Errorf("checks = %d, want 1", checks)
	}
	if got.Latest != "2.0.0" {
		t.Errorf("latest = %q, want 2.0.0", got.Latest)
	}
}

func TestInstallIsAcceptedRatherThanCompleted(t *testing.T) {
	e := newEnv(t)
	e.eng.setUpdate(update.Status{
		Supported: true, Enabled: true, State: update.StateReady,
		Current: "1.0.0", Latest: "1.1.0",
	})

	// 202, and the answer goes out before the installer starts: the installer
	// stops this service, so a response written afterwards would never arrive.
	var got updateResponse
	e.mustCall(http.MethodPost, "/api/update/install", nil, &got, http.StatusAccepted)

	if e.eng.installCount() != 1 {
		t.Errorf("installs = %d, want 1", e.eng.installCount())
	}
	if got.State != string(update.StateInstalling) {
		t.Errorf("state = %q, want %q", got.State, update.StateInstalling)
	}
}

func TestInstallWithNothingStagedIsARefusal(t *testing.T) {
	e := newEnv(t)
	e.eng.upInstall = update.ErrNotReady

	var raw json.RawMessage
	e.mustCall(http.MethodPost, "/api/update/install", nil, &raw, http.StatusConflict)

	if code := errorOf(t, raw).Code; code != "not_ready" {
		t.Errorf("code = %q, want not_ready", code)
	}
	if e.eng.installCount() != 0 {
		t.Error("an installer was run with nothing staged")
	}
}

func TestDownloadFailureReportsTheState(t *testing.T) {
	e := newEnv(t)
	e.eng.upDownload = update.ErrNotReady
	e.eng.setUpdate(update.Status{
		Supported: true, Enabled: true, State: update.StateError,
		Err: "checksum mismatch",
	})

	var got updateResponse
	e.mustCall(http.MethodPost, "/api/update/download", nil, &got, http.StatusConflict)

	if got.Error != "checksum mismatch" {
		t.Errorf("error = %q, want the checker's message", got.Error)
	}
}

func TestUpdateSettingsRoundTrip(t *testing.T) {
	e := newEnv(t)

	var got settingsResponse
	e.mustCall(http.MethodGet, "/api/settings", nil, &got, http.StatusOK)
	if !got.Updates.Enabled || !got.Updates.AutoDownload {
		t.Fatalf("seeded updates = %+v, want both on", got.Updates)
	}

	body := map[string]any{"updates": map[string]any{"enabled": false}}
	var after settingsResponse
	e.mustCall(http.MethodPut, "/api/settings", body, &after, http.StatusOK)

	if after.Updates.Enabled {
		t.Error("enabled is still on after being turned off")
	}
	if !after.Updates.AutoDownload {
		t.Error("auto_download changed when only enabled was sent")
	}

	// Stored as the same "1"/"0" the rest of the table uses.
	all, err := e.st.AllSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if all[store.KeyUpdatesEnabled] != "0" {
		t.Errorf("%s = %q, want \"0\"", store.KeyUpdatesEnabled, all[store.KeyUpdatesEnabled])
	}
}
