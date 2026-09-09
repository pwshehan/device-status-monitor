package api

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/pwshehan/local-device-monitor/internal/notify"
	"github.com/pwshehan/local-device-monitor/internal/store"
)

// settingsResponse is the settings form's shape.
//
// There is no password field, ever. The stored value is a sealed blob and the
// API's contract is that it goes in and never comes out: HasPassword is enough
// for the form to render a "password set — leave blank to keep" affordance,
// and a plaintext round trip would put mail credentials in every HTTP log and
// browser cache between here and the window.
type settingsResponse struct {
	SMTP struct {
		Host        string `json:"host"`
		Port        int    `json:"port"`
		Security    string `json:"security"`
		Username    string `json:"username"`
		From        string `json:"from"`
		HasPassword bool   `json:"has_password"`
		Password    string `json:"password"`
	} `json:"smtp"`

	Alerts struct {
		Recipients  string `json:"recipients"`
		ReminderSec int    `json:"reminder_sec"`
		CollapseSec int    `json:"collapse_sec"`
		MaxPerHour  int    `json:"max_per_hour"`
	} `json:"alerts"`

	Defaults struct {
		CheckIntervalSec  int `json:"check_interval_sec"`
		TimeoutSec        int `json:"timeout_sec"`
		FailureThreshold  int `json:"failure_threshold"`
		RecoveryThreshold int `json:"recovery_threshold"`
	} `json:"defaults"`

	Retention struct {
		RawDays    int `json:"raw_days"`
		RollupDays int `json:"rollup_days"`
	} `json:"retention"`

	Updates struct {
		Enabled      bool `json:"enabled"`
		AutoDownload bool `json:"auto_download"`
	} `json:"updates"`
}

// redacted is what the password field always contains on the way out.
const redacted = "***"

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	all, err := s.st.AllSettings(r.Context())
	if err != nil {
		s.storeErr(w, r, err, "settings", "")
		return
	}

	var res settingsResponse
	res.SMTP.Host = all[store.KeySMTPHost]
	res.SMTP.Port = atoiOr(all[store.KeySMTPPort], 587)
	res.SMTP.Security = orDefault(all[store.KeySMTPSecurity], notify.SecurityStartTLS)
	res.SMTP.Username = all[store.KeySMTPUsername]
	res.SMTP.From = all[store.KeySMTPFrom]
	res.SMTP.HasPassword = strings.TrimSpace(all[store.KeySMTPPasswordEnc]) != ""
	if res.SMTP.HasPassword {
		res.SMTP.Password = redacted
	}

	res.Alerts.Recipients = all[store.KeyAlertRecipients]
	res.Alerts.ReminderSec = atoiOr(all[store.KeyAlertReminderSec], 0)
	res.Alerts.CollapseSec = atoiOr(all[store.KeyAlertCollapseSec], 15)
	res.Alerts.MaxPerHour = atoiOr(all[store.KeyAlertMaxPerHour], 20)

	res.Defaults.CheckIntervalSec = atoiOr(all[store.KeyDefaultInterval], 30)
	res.Defaults.TimeoutSec = atoiOr(all[store.KeyDefaultTimeout], 3)
	res.Defaults.FailureThreshold = atoiOr(all[store.KeyDefaultFailureThreshold], 3)
	res.Defaults.RecoveryThreshold = atoiOr(all[store.KeyDefaultRecoveryThreshold], 1)

	res.Retention.RawDays = atoiOr(all[store.KeyRetentionRawDays], 14)
	res.Retention.RollupDays = atoiOr(all[store.KeyRetentionRollupDays], 400)

	res.Updates.Enabled = boolOr(all[store.KeyUpdatesEnabled], true)
	res.Updates.AutoDownload = boolOr(all[store.KeyUpdatesAutoDownload], true)

	writeJSON(w, http.StatusOK, res)
}

// settingsBody is the write-through body. Every field is optional; an absent
// field is left as it was.
type settingsBody struct {
	SMTP struct {
		Host     Opt[string] `json:"host"`
		Port     Opt[int]    `json:"port"`
		Security Opt[string] `json:"security"`
		Username Opt[string] `json:"username"`
		From     Opt[string] `json:"from"`
		Password Opt[string] `json:"password"`
	} `json:"smtp"`

	Alerts struct {
		Recipients  Opt[string] `json:"recipients"`
		ReminderSec Opt[int]    `json:"reminder_sec"`
		CollapseSec Opt[int]    `json:"collapse_sec"`
		MaxPerHour  Opt[int]    `json:"max_per_hour"`
	} `json:"alerts"`

	Defaults struct {
		CheckIntervalSec  Opt[int] `json:"check_interval_sec"`
		TimeoutSec        Opt[int] `json:"timeout_sec"`
		FailureThreshold  Opt[int] `json:"failure_threshold"`
		RecoveryThreshold Opt[int] `json:"recovery_threshold"`
	} `json:"defaults"`

	Retention struct {
		RawDays    Opt[int] `json:"raw_days"`
		RollupDays Opt[int] `json:"rollup_days"`
	} `json:"retention"`

	Updates struct {
		Enabled      Opt[bool] `json:"enabled"`
		AutoDownload Opt[bool] `json:"auto_download"`
	} `json:"updates"`
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body settingsBody
	if err := decode(w, r, &body); err != nil {
		return
	}

	kv := map[string]string{}
	putStr := func(key string, o Opt[string]) {
		if o.Set {
			kv[key] = strings.TrimSpace(o.Value)
		}
	}
	putInt := func(key string, o Opt[int]) {
		if o.Set && o.Valid {
			kv[key] = strconv.Itoa(o.Value)
		}
	}
	// Flags are stored as "1"/"0" rather than "true"/"false" to match the rest
	// of the table, which is all numbers as text.
	putBool := func(key string, o Opt[bool]) {
		if o.Set && o.Valid {
			kv[key] = boolStr(o.Value)
		}
	}

	putStr(store.KeySMTPHost, body.SMTP.Host)
	putStr(store.KeySMTPUsername, body.SMTP.Username)
	putStr(store.KeySMTPFrom, body.SMTP.From)
	putInt(store.KeySMTPPort, body.SMTP.Port)
	putStr(store.KeyAlertRecipients, body.Alerts.Recipients)
	putInt(store.KeyAlertReminderSec, body.Alerts.ReminderSec)
	putInt(store.KeyAlertCollapseSec, body.Alerts.CollapseSec)
	putInt(store.KeyAlertMaxPerHour, body.Alerts.MaxPerHour)
	putInt(store.KeyDefaultInterval, body.Defaults.CheckIntervalSec)
	putInt(store.KeyDefaultTimeout, body.Defaults.TimeoutSec)
	putInt(store.KeyDefaultFailureThreshold, body.Defaults.FailureThreshold)
	putInt(store.KeyDefaultRecoveryThreshold, body.Defaults.RecoveryThreshold)
	putInt(store.KeyRetentionRawDays, body.Retention.RawDays)
	putInt(store.KeyRetentionRollupDays, body.Retention.RollupDays)
	putBool(store.KeyUpdatesEnabled, body.Updates.Enabled)
	putBool(store.KeyUpdatesAutoDownload, body.Updates.AutoDownload)

	if body.SMTP.Security.Set {
		mode := strings.ToLower(strings.TrimSpace(body.SMTP.Security.Value))
		switch mode {
		case notify.SecurityStartTLS, notify.SecurityTLS, notify.SecurityNone:
			kv[store.KeySMTPSecurity] = mode
		default:
			invalid(w, "security must be starttls, tls or none", "smtp.security")
			return
		}
	}

	if s.reject(w, validateSettings(kv)) {
		return
	}

	// The password takes a different path: sealed by the notifier, written as
	// a blob, never in kv. An omitted or empty field leaves the stored one
	// alone, which is what makes "leave blank to keep" work; an explicit null
	// clears it.
	pw := body.SMTP.Password
	changedPassword := false
	switch {
	case pw.Set && !pw.Valid:
		if err := s.eng.SaveSMTPPassword(r.Context(), ""); err != nil {
			s.storeErr(w, r, err, "settings", "smtp.password")
			return
		}
		changedPassword = true
	case pw.Set && pw.Valid && pw.Value != "" && pw.Value != redacted:
		if err := s.eng.SaveSMTPPassword(r.Context(), pw.Value); err != nil {
			s.storeErr(w, r, err, "settings", "smtp.password")
			return
		}
		changedPassword = true
	}

	if len(kv) > 0 {
		if err := s.st.PutSettings(r.Context(), kv); err != nil {
			s.storeErr(w, r, err, "settings", "")
			return
		}
	}

	changed := make([]string, 0, len(kv)+1)
	for k := range kv {
		changed = append(changed, k)
	}
	if changedPassword {
		changed = append(changed, store.KeySMTPPasswordEnc)
	}
	slices.Sort(changed)

	// The default.* tier is the bottom of the inheritance chain, so a settings
	// write can change the interval of every device that inherits.
	s.eng.Reload()
	s.hub.Publish(Event{Type: EventSettings, Data: SettingsEvent{ChangedKeys: changed}})

	s.handleGetSettings(w, r)
}

// validateSettings checks the numeric ranges and the recipient list. The
// global tier has no "inherit" option, so unlike a device override these
// cannot be null.
func validateSettings(kv map[string]string) error {
	type rule struct {
		key      string
		field    string
		min, max int
	}
	rules := []rule{
		{store.KeySMTPPort, "smtp.port", 1, 65535},
		{store.KeyAlertReminderSec, "alerts.reminder_sec", 0, 60 * 60 * 24 * 7},
		// Zero is valid for both: no collapsing, and no cap. The upper bound on
		// the window is the ceiling the buffer enforces anyway (core.MaxHold).
		{store.KeyAlertCollapseSec, "alerts.collapse_sec", 0, 120},
		{store.KeyAlertMaxPerHour, "alerts.max_per_hour", 0, 1000},
		{store.KeyDefaultInterval, "defaults.check_interval_sec", MinIntervalSec, 60 * 60 * 24},
		{store.KeyDefaultTimeout, "defaults.timeout_sec", MinTimeoutSec, MaxTimeoutSec},
		{store.KeyDefaultFailureThreshold, "defaults.failure_threshold", 1, 100},
		{store.KeyDefaultRecoveryThreshold, "defaults.recovery_threshold", 1, 100},
		{store.KeyRetentionRawDays, "retention.raw_days", 1, 400},
		{store.KeyRetentionRollupDays, "retention.rollup_days", 1, 3650},
	}
	for _, ru := range rules {
		raw, ok := kv[ru.key]
		if !ok {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return fieldErr(ru.field, "must be a number")
		}
		if v < ru.min || v > ru.max {
			return fieldErr(ru.field, "must be between %d and %d", ru.min, ru.max)
		}
	}
	if list, ok := kv[store.KeyAlertRecipients]; ok {
		if err := validateRecipients("alerts.recipients", list); err != nil {
			return err
		}
	}
	if from, ok := kv[store.KeySMTPFrom]; ok && from != "" {
		if err := validateRecipients("smtp.from", from); err != nil {
			return err
		}
	}
	// A default timeout longer than the default interval would have every
	// inheriting device's probes overlapping.
	if iv, ok1 := kv[store.KeyDefaultInterval]; ok1 {
		if to, ok2 := kv[store.KeyDefaultTimeout]; ok2 {
			ivN, _ := strconv.Atoi(iv)
			toN, _ := strconv.Atoi(to)
			if toN > ivN {
				return fieldErr("defaults.timeout_sec",
					"timeout must not exceed the default check interval")
			}
		}
	}
	return nil
}

// testEmailBody optionally overrides who the test goes to. With no recipients
// it falls back to the configured list, which is the case worth testing.
type testEmailBody struct {
	To []string `json:"to"`
}

// handleTestEmail sends immediately, bypassing the outbox, and returns the
// SMTP error verbatim.
//
// Verbatim matters: "535 5.7.8 Username and Password not accepted" tells the
// user they need an app password, and any friendlier wrapping of it would not.
func (s *Server) handleTestEmail(w http.ResponseWriter, r *http.Request) {
	var body testEmailBody
	if r.ContentLength != 0 {
		if err := decode(w, r, &body); err != nil {
			return
		}
	}

	to := body.To
	if len(to) == 0 {
		list, err := s.st.Setting(r.Context(), store.KeyAlertRecipients)
		if err != nil {
			s.storeErr(w, r, err, "settings", "")
			return
		}
		to = store.RecipientList(list)
	}
	if len(to) == 0 {
		invalid(w, "no recipients: set alerts.recipients or pass \"to\"", "alerts.recipients")
		return
	}
	if s.reject(w, validateRecipients("to", strings.Join(to, ","))) {
		return
	}

	if err := s.eng.SendTestEmail(r.Context(), to); err != nil {
		// Not a 500: the service is fine, the mail server said no. 502 with
		// the raw text is the honest answer, and the UI shows it as-is.
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": errorDetail{
				Code:    "smtp_error",
				Message: err.Error(),
				Field:   "smtp",
			},
			"sent": false,
			"to":   to,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "to": to})
}

func atoiOr(s string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return v
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// boolOr and boolStr are the two halves of how a flag is stored: "1" or "0",
// with anything unrecognised falling back rather than being read as false. A
// malformed value must not silently turn a feature off.
func boolOr(s string, fallback bool) bool {
	switch s {
	case "1", "true":
		return true
	case "0", "false":
		return false
	default:
		return fallback
	}
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
