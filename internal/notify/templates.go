package notify

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/pwshehan/local-device-monitor/internal/model"
	"github.com/pwshehan/local-device-monitor/internal/probe"
)

// Event carries everything the templates need about one transition.
type Event struct {
	Eff        model.Effective
	Class      probe.Class
	ErrMsg     string
	FirstFail  time.Time
	DetectedAt time.Time
	Failures   int

	// Recovery only.
	RecoveredAt time.Time
	Downtime    time.Duration
	LatencyMS   int64
	Uptime24h   *float64
}

// DownAlert renders the outage mail.
func DownAlert(e Event) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("[DOWN] %s (%s)", e.Eff.Name, e.Eff.Addr)

	var t strings.Builder
	fmt.Fprintf(&t, "%s is not responding.\n\n", e.Eff.Name)
	fmt.Fprintf(&t, "Address          %s\n", e.Eff.Addr)
	if e.Eff.GroupName != "" {
		fmt.Fprintf(&t, "Group            %s\n", e.Eff.GroupName)
	}
	fmt.Fprintf(&t, "First failure    %s\n", stamp(e.FirstFail))
	fmt.Fprintf(&t, "Confirmed down   %s (after %d consecutive failures)\n", stamp(e.DetectedAt), e.Failures)
	fmt.Fprintf(&t, "Reason           %s\n", reason(e.Class, e.ErrMsg))
	fmt.Fprintf(&t, "Check interval   every %s, %s timeout\n", human(e.Eff.Interval), human(e.Eff.Timeout))
	t.WriteString("\nYou will get a second message when it recovers.\n")

	rows := [][2]string{
		{"Address", e.Eff.Addr},
		{"First failure", stamp(e.FirstFail)},
		{"Confirmed down", fmt.Sprintf("%s (after %d consecutive failures)", stamp(e.DetectedAt), e.Failures)},
		{"Reason", reason(e.Class, e.ErrMsg)},
	}
	if e.Eff.GroupName != "" {
		rows = append(rows[:1], append([][2]string{{"Group", e.Eff.GroupName}}, rows[1:]...)...)
	}
	htmlBody = page(
		fmt.Sprintf("%s is not responding", html.EscapeString(e.Eff.Name)),
		"#a83227", rows,
		"You will get a second message when it recovers.")
	return subject, t.String(), htmlBody
}

// RecoveryAlert renders the recovery mail. The duration comes from the incident
// row, which is why it is accurate even if the service restarted mid-outage.
func RecoveryAlert(e Event) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("[UP] %s recovered after %s", e.Eff.Name, human(e.Downtime))

	var t strings.Builder
	fmt.Fprintf(&t, "%s is responding again.\n\n", e.Eff.Name)
	fmt.Fprintf(&t, "Address          %s\n", e.Eff.Addr)
	if e.Eff.GroupName != "" {
		fmt.Fprintf(&t, "Group            %s\n", e.Eff.GroupName)
	}
	fmt.Fprintf(&t, "Went down        %s\n", stamp(e.FirstFail))
	fmt.Fprintf(&t, "Recovered        %s\n", stamp(e.RecoveredAt))
	fmt.Fprintf(&t, "Total downtime   %s\n", human(e.Downtime))
	fmt.Fprintf(&t, "Latency now      %d ms\n", e.LatencyMS)
	if e.Uptime24h != nil {
		fmt.Fprintf(&t, "Uptime, 24 h     %.2f%%\n", *e.Uptime24h)
	}

	rows := [][2]string{
		{"Address", e.Eff.Addr},
		{"Went down", stamp(e.FirstFail)},
		{"Recovered", stamp(e.RecoveredAt)},
		{"Total downtime", human(e.Downtime)},
		{"Latency now", fmt.Sprintf("%d ms", e.LatencyMS)},
	}
	if e.Uptime24h != nil {
		rows = append(rows, [2]string{"Uptime, 24 h", fmt.Sprintf("%.2f%%", *e.Uptime24h)})
	}
	if e.Eff.GroupName != "" {
		rows = append(rows[:1], append([][2]string{{"Group", e.Eff.GroupName}}, rows[1:]...)...)
	}
	htmlBody = page(
		fmt.Sprintf("%s recovered", html.EscapeString(e.Eff.Name)),
		"#2c7d55", rows, "")
	return subject, t.String(), htmlBody
}

// ReminderAlert renders a still-down reminder.
func ReminderAlert(e Event, openFor time.Duration) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("[STILL DOWN] %s — %s so far", e.Eff.Name, human(openFor))

	var t strings.Builder
	fmt.Fprintf(&t, "%s (%s) is still not responding.\n\n", e.Eff.Name, e.Eff.Addr)
	fmt.Fprintf(&t, "Down since       %s\n", stamp(e.FirstFail))
	fmt.Fprintf(&t, "Elapsed          %s\n", human(openFor))
	fmt.Fprintf(&t, "Reason           %s\n", reason(e.Class, e.ErrMsg))

	htmlBody = page(
		fmt.Sprintf("%s is still down", html.EscapeString(e.Eff.Name)),
		"#a83227",
		[][2]string{
			{"Address", e.Eff.Addr},
			{"Down since", stamp(e.FirstFail)},
			{"Elapsed", human(openFor)},
			{"Reason", reason(e.Class, e.ErrMsg)},
		}, "")
	return subject, t.String(), htmlBody
}

// TestMessage proves the SMTP credentials work.
func TestMessage(host string) (subject, text, htmlBody string) {
	subject = "Local Device Monitor test message"
	text = fmt.Sprintf(
		"This is a test from Local Device Monitor on %s.\n\nIf you are reading it, alert delivery works.\n",
		host)
	htmlBody = page("SMTP test succeeded", "#0d6a73",
		[][2]string{{"Sent from", host}, {"Sent at", stamp(time.Now())}},
		"If you are reading this, alert delivery works.")
	return subject, text, htmlBody
}

// --- helpers -----------------------------------------------------------------

func reason(class probe.Class, msg string) string {
	switch class {
	case probe.ClassTimeout:
		return "No response before the timeout — the host is silent"
	case probe.ClassRefused:
		return "Connection refused — the host answered, but nothing is listening on that port"
	case probe.ClassDNS:
		return "Host name could not be resolved"
	case probe.ClassUnreachable:
		return "Network unreachable — no route to the host"
	default:
		if msg != "" {
			return msg
		}
		return "Unknown error"
	}
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04:05 MST")
}

// human renders a duration the way a person reads it: "4m 12s", "3h 07m".
func human(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// page wraps rows in table-based HTML, because mail clients are not browsers.
func page(heading, accent string, rows [][2]string, footer string) string {
	var b strings.Builder
	b.WriteString(`<div style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;font-size:14px;color:#101a1e">`)
	fmt.Fprintf(&b, `<p style="margin:0 0 16px;font-size:17px;font-weight:600;color:%s">%s</p>`, accent, heading)
	b.WriteString(`<table cellpadding="0" cellspacing="0" style="border-collapse:collapse">`)
	for _, r := range rows {
		fmt.Fprintf(&b,
			`<tr><td style="padding:4px 18px 4px 0;color:#47585f;white-space:nowrap">%s</td>`+
				`<td style="padding:4px 0;font-family:ui-monospace,Consolas,monospace">%s</td></tr>`,
			html.EscapeString(r[0]), html.EscapeString(r[1]))
	}
	b.WriteString(`</table>`)
	if footer != "" {
		fmt.Fprintf(&b, `<p style="margin:16px 0 0;color:#47585f">%s</p>`, html.EscapeString(footer))
	}
	b.WriteString(`</div>`)
	return b.String()
}
