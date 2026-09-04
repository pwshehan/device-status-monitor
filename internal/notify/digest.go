package notify

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// DigestMember is one device inside a collapsed alert.
type DigestMember struct {
	Name   string
	Addr   string
	Reason string
	// Downtime is set on a recovery digest.
	Downtime time.Duration
	// Group is only rendered by the global digest, where members come from
	// several sites.
	Group string
}

// GroupDown renders one mail for a site that has gone dark.
//
// A site outage is one event, not six. Six separate mails for one failed
// switch is how people learn to filter the alerts, which is the worst outcome
// this system can produce — so the collapse exists to keep alert mail worth
// reading.
//
// `total` is the group's member count, because "6 of 8 unreachable" and "6 of
// 6 unreachable" call for different responses: the first is a subnet or a
// switch port, the second is the site.
func GroupDown(group string, total int, members []DigestMember) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("[DOWN] %s — %d of %d devices unreachable", group, len(members), total)

	var t strings.Builder
	fmt.Fprintf(&t, "%d of %d devices in %s stopped responding at about the same time.\n\n",
		len(members), total, group)
	if len(members) == total {
		t.WriteString("Every device in this group is affected, which usually means the site's " +
			"link or its switch rather than the devices themselves.\n\n")
	}
	for _, m := range members {
		fmt.Fprintf(&t, "  %-28s %-22s %s\n", m.Name, m.Addr, m.Reason)
	}
	t.WriteString("\nYou will get one more message when they recover.\n")

	rows := make([][2]string, 0, len(members))
	for _, m := range members {
		rows = append(rows, [2]string{m.Name, fmt.Sprintf("%s — %s", m.Addr, m.Reason)})
	}
	htmlBody = page(
		fmt.Sprintf("%s — %d of %d devices unreachable",
			html.EscapeString(group), len(members), total),
		"#a83227", rows,
		"Collapsed into one message because these failed together.")
	return subject, t.String(), htmlBody
}

// GroupRecovery renders the other half of a collapsed site outage.
func GroupRecovery(group string, members []DigestMember) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("[UP] %s — %d devices recovered", group, len(members))

	var t strings.Builder
	fmt.Fprintf(&t, "%d devices in %s are responding again.\n\n", len(members), group)
	for _, m := range members {
		fmt.Fprintf(&t, "  %-28s %-22s down for %s\n", m.Name, m.Addr, human(m.Downtime))
	}

	rows := make([][2]string, 0, len(members))
	for _, m := range members {
		rows = append(rows, [2]string{m.Name, fmt.Sprintf("%s — down for %s", m.Addr, human(m.Downtime))})
	}
	htmlBody = page(
		fmt.Sprintf("%s — %d devices recovered", html.EscapeString(group), len(members)),
		"#2f6b3f", rows,
		"Collapsed into one message because these recovered together.")
	return subject, t.String(), htmlBody
}

// GlobalDown renders the last-resort digest.
//
// This is the core-switch case: when devices fail across unrelated groups at
// once, the common cause is upstream of all of them, and per-group mails would
// be a dozen messages describing one event. Groups are named per device here,
// because which sites are affected is the diagnosis.
func GlobalDown(groups int, members []DigestMember) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("[DOWN] %d devices unreachable across %d groups", len(members), groups)

	var t strings.Builder
	fmt.Fprintf(&t, "%d devices in %d different groups stopped responding at about the same time.\n\n",
		len(members), groups)
	t.WriteString("Failures spread across unrelated groups usually mean something upstream " +
		"of all of them — a core switch, a router, or this machine's own link.\n\n")
	for _, m := range members {
		group := m.Group
		if group == "" {
			group = "Ungrouped"
		}
		fmt.Fprintf(&t, "  %-20s %-28s %s\n", group, m.Name, m.Addr)
	}

	rows := make([][2]string, 0, len(members))
	for _, m := range members {
		group := m.Group
		if group == "" {
			group = "Ungrouped"
		}
		rows = append(rows, [2]string{m.Name, fmt.Sprintf("%s — %s", group, m.Addr)})
	}
	htmlBody = page(
		fmt.Sprintf("%d devices unreachable across %d groups", len(members), groups),
		"#a83227", rows,
		"Collapsed into one message: this many failures at once has one cause.")
	return subject, t.String(), htmlBody
}

// Throttled renders the notice that mail is being withheld.
//
// Sent once when the hourly cap is reached. The alternative — dropping alerts
// silently — would leave an operator believing everything is fine because the
// mail stopped, which is the exact failure this whole system exists to
// prevent. The incidents are all in the database and on the dashboard either
// way; only the mail is capped.
func Throttled(sent int, window time.Duration) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("[DOWN] Alert mail paused — %d messages in the last %s", sent, human(window))

	var t strings.Builder
	fmt.Fprintf(&t, "%d alert messages have gone out in the last %s, which is the configured "+
		"limit, so further alert mail is paused.\n\n", sent, human(window))
	t.WriteString("Monitoring has not stopped. Every state change is still being recorded, " +
		"and the dashboard is up to date — only email is affected.\n\n")
	t.WriteString("This usually means a device is flapping, or a large outage is still " +
		"unfolding. Raise alert.max_per_hour in Settings if the limit is too low.\n")

	htmlBody = page(
		"Alert mail paused",
		"#8a6d1f",
		[][2]string{
			{"Messages sent", fmt.Sprintf("%d in the last %s", sent, human(window))},
			{"Monitoring", "still running — only email is paused"},
			{"To raise the limit", "Settings → alert.max_per_hour"},
		},
		"Nothing has been lost: the incidents are on the dashboard.")
	return subject, t.String(), htmlBody
}
