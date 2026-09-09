package api

import (
	"strings"
	"time"

	"github.com/pwshehan/local-device-monitor/internal/model"
	"github.com/pwshehan/local-device-monitor/internal/store"
)

// The wire types live here rather than on the model, so a rename in the UI
// contract never forces a change to the engine's domain types — and so the
// SMTP password has no field to accidentally be serialised into.
//
// Two conventions, applied everywhere:
//
//   - Record timestamps are RFC3339 UTC strings; series timestamps are unix
//     seconds, because that is what a chart library wants and converting
//     thousands of strings back to numbers in the browser is waste.
//   - A nullable probe setting is null when the row inherits. The resolved
//     value and where it came from live under "effective", so a form can show
//     "30 (from Warehouse)" as placeholder text without guessing.

type effectiveDTO struct {
	CheckIntervalSec  int               `json:"check_interval_sec"`
	TimeoutSec        int               `json:"timeout_sec"`
	FailureThreshold  int               `json:"failure_threshold"`
	RecoveryThreshold int               `json:"recovery_threshold"`
	Notify            bool              `json:"notify"`
	Recipients        []string          `json:"recipients"`
	PausedUntil       *string           `json:"paused_until"`
	Paused            bool              `json:"paused"`
	Source            map[string]string `json:"source"`
}

func newEffectiveDTO(e model.Effective) effectiveDTO {
	return effectiveDTO{
		CheckIntervalSec:  int(e.Interval / time.Second),
		TimeoutSec:        int(e.Timeout / time.Second),
		FailureThreshold:  e.FailureThreshold,
		RecoveryThreshold: e.RecoveryThreshold,
		Notify:            e.Notify,
		Recipients:        e.Recipients,
		PausedUntil:       rfc3339Zero(e.PausedUntil),
		Paused:            e.Paused(time.Now()),
		Source:            e.Source,
	}
}

type deviceDTO struct {
	ID        int64  `json:"id"`
	GroupID   *int64 `json:"group_id"`
	GroupName string `json:"group_name"`
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	Port      int    `json:"port"`

	CheckIntervalSec  *int     `json:"check_interval_sec"`
	TimeoutSec        *int     `json:"timeout_sec"`
	FailureThreshold  *int     `json:"failure_threshold"`
	RecoveryThreshold *int     `json:"recovery_threshold"`
	Enabled           bool     `json:"enabled"`
	Notify            bool     `json:"notify"`
	PausedUntil       *string  `json:"paused_until"`
	Tags              []string `json:"tags"`

	Status               model.Status `json:"status"`
	ConsecutiveFailures  int          `json:"consecutive_failures"`
	ConsecutiveSuccesses int          `json:"consecutive_successes"`
	FirstFailureAt       *string      `json:"first_failure_at"`
	LastCheckAt          *string      `json:"last_check_at"`
	LastLatencyMS        *int64       `json:"last_latency_ms"`
	LastError            string       `json:"last_error"`
	LastStatusChangeAt   *string      `json:"last_status_change_at"`

	Effective effectiveDTO `json:"effective"`

	// RecentChecks is the last few outcomes, oldest first, as one character
	// each: "U" up, "D" down.
	//
	// A compact string rather than an array because this ships for every
	// device on every refresh, and the dashboard refreshes whenever anything
	// changes state: 200 devices × 40 checks is 8 kB this way and 40 kB as
	// JSON booleans. It carries no latency — the strip it draws answers "has
	// this been steady?", and the device's own page has the rest.
	RecentChecks string `json:"recent_checks"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// encodeChecks renders check outcomes for the wire.
func encodeChecks(checks []model.Status) string {
	if len(checks) == 0 {
		return ""
	}
	out := make([]byte, 0, len(checks))
	for _, c := range checks {
		if c == model.StatusUp {
			out = append(out, 'U')
		} else {
			out = append(out, 'D')
		}
	}
	return string(out)
}

func newDeviceDTO(d model.Device, eff model.Effective) deviceDTO {
	return deviceDTO{
		ID:        d.ID,
		GroupID:   d.GroupID,
		GroupName: eff.GroupName,
		Name:      d.Name,
		IPAddress: d.IPAddress,
		Port:      d.Port,

		CheckIntervalSec:  d.CheckIntervalSec,
		TimeoutSec:        d.TimeoutSec,
		FailureThreshold:  d.FailureThreshold,
		RecoveryThreshold: d.RecoveryThreshold,
		Enabled:           d.Enabled,
		Notify:            d.Notify,
		PausedUntil:       rfc3339Ptr(d.PausedUntil),
		Tags:              splitTags(d.Tags),

		Status:               d.Status,
		ConsecutiveFailures:  d.ConsecutiveFailures,
		ConsecutiveSuccesses: d.ConsecutiveSuccesses,
		FirstFailureAt:       rfc3339Ptr(d.FirstFailureAt),
		LastCheckAt:          rfc3339Ptr(d.LastCheckAt),
		LastLatencyMS:        d.LastLatencyMS,
		LastError:            d.LastError,
		LastStatusChangeAt:   rfc3339Ptr(d.LastStatusChangeAt),

		Effective: newEffectiveDTO(eff),

		CreatedAt: rfc3339(d.CreatedAt),
		UpdatedAt: rfc3339(d.UpdatedAt),
	}
}

type groupStatsDTO struct {
	Members     int      `json:"members"`
	Up          int      `json:"up"`
	Down        int      `json:"down"`
	Unknown     int      `json:"unknown"`
	Paused      int      `json:"paused"`
	Disabled    int      `json:"disabled"`
	ChecksToday int64    `json:"checks_today"`
	UptimeToday *float64 `json:"uptime_today_pct"`
}

type groupDTO struct {
	// ID is null for the Ungrouped bucket: a real section of the dashboard,
	// but not a row in the groups table and not something you can edit.
	ID        *int64 `json:"id"`
	Ungrouped bool   `json:"ungrouped"`

	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
	SortOrder   int    `json:"sort_order"`

	CheckIntervalSec  *int    `json:"check_interval_sec"`
	TimeoutSec        *int    `json:"timeout_sec"`
	FailureThreshold  *int    `json:"failure_threshold"`
	RecoveryThreshold *int    `json:"recovery_threshold"`
	Notify            bool    `json:"notify"`
	Recipients        *string `json:"recipients"`
	PausedUntil       *string `json:"paused_until"`
	Paused            bool    `json:"paused"`

	Stats *groupStatsDTO `json:"stats,omitempty"`

	CreatedAt *string `json:"created_at"`
	UpdatedAt *string `json:"updated_at"`
}

func newGroupDTO(g model.Group) groupDTO {
	id := g.ID
	return groupDTO{
		ID:                &id,
		Name:              g.Name,
		Description:       g.Description,
		Color:             g.Color,
		SortOrder:         g.SortOrder,
		CheckIntervalSec:  g.CheckIntervalSec,
		TimeoutSec:        g.TimeoutSec,
		FailureThreshold:  g.FailureThreshold,
		RecoveryThreshold: g.RecoveryThreshold,
		Notify:            g.Notify,
		Recipients:        g.Recipients,
		PausedUntil:       rfc3339Ptr(g.PausedUntil),
		Paused:            g.PausedUntil != nil && g.PausedUntil.After(time.Now()),
		CreatedAt:         strptr(rfc3339(g.CreatedAt)),
		UpdatedAt:         strptr(rfc3339(g.UpdatedAt)),
	}
}

func newGroupStatDTO(st store.GroupStat) groupDTO {
	var dto groupDTO
	if st.Group != nil {
		dto = newGroupDTO(*st.Group)
	} else {
		dto = groupDTO{Name: "Ungrouped", Ungrouped: true, Notify: true, SortOrder: 1 << 30}
	}
	dto.Stats = &groupStatsDTO{
		Members:     st.Members,
		Up:          st.Up,
		Down:        st.Down,
		Unknown:     st.Unknown,
		Paused:      st.Paused,
		Disabled:    st.Disabled,
		ChecksToday: st.ChecksToday,
		UptimeToday: st.UptimeToday,
	}
	return dto
}

type incidentDTO struct {
	ID           int64   `json:"id"`
	DeviceID     int64   `json:"device_id"`
	DeviceName   string  `json:"device_name,omitempty"`
	GroupID      *int64  `json:"group_id,omitempty"`
	GroupName    string  `json:"group_name,omitempty"`
	StartedAt    string  `json:"started_at"`
	DetectedAt   string  `json:"detected_at"`
	ResolvedAt   *string `json:"resolved_at"`
	DurationSec  *int64  `json:"duration_sec"`
	Ongoing      bool    `json:"ongoing"`
	Cause        string  `json:"cause"`
	AlertSent    bool    `json:"alert_sent"`
	RecoverySent bool    `json:"recovery_sent"`
}

func newIncidentDTO(inc model.Incident) incidentDTO {
	return incidentDTO{
		ID:           inc.ID,
		DeviceID:     inc.DeviceID,
		StartedAt:    rfc3339(inc.StartedAt),
		DetectedAt:   rfc3339(inc.DetectedAt),
		ResolvedAt:   rfc3339Ptr(inc.ResolvedAt),
		DurationSec:  inc.DurationSec,
		Ongoing:      inc.ResolvedAt == nil,
		Cause:        inc.Cause,
		AlertSent:    inc.AlertSent,
		RecoverySent: inc.RecoverySent,
	}
}

func newOpenIncidentDTO(oi store.OpenIncident) incidentDTO {
	dto := newIncidentDTO(oi.Incident)
	dto.DeviceName = oi.DeviceName
	dto.GroupID = oi.GroupID
	dto.GroupName = oi.GroupName
	// An open incident has no stored duration; the UI wants "down for how
	// long", so report it from the clock rather than making the client guess
	// which of started_at and now to subtract.
	secs := int64(time.Since(oi.StartedAt).Seconds())
	dto.DurationSec = &secs
	return dto
}

// sampleDTO is one point of the decimated latency series. Unix seconds, as the
// chart wants them.
type sampleDTO struct {
	T            int64    `json:"t"`
	AvgLatencyMS *float64 `json:"avg_latency_ms"`
	MaxLatencyMS int64    `json:"max_latency_ms"`
	Checks       int64    `json:"checks"`
	Downs        int64    `json:"downs"`
}

type dayUptimeDTO struct {
	Day          string   `json:"day"`
	ChecksTotal  int64    `json:"checks_total"`
	ChecksUp     int64    `json:"checks_up"`
	UptimePct    float64  `json:"uptime_pct"`
	AvgLatencyMS *float64 `json:"avg_latency_ms"`
	P95LatencyMS *int64   `json:"p95_latency_ms"`
	DowntimeSec  int64    `json:"downtime_sec"`
	Source       string   `json:"source"`
}

type groupDayUptimeDTO struct {
	Day             string   `json:"day"`
	Devices         int      `json:"devices"`
	ChecksTotal     int64    `json:"checks_total"`
	ChecksUp        int64    `json:"checks_up"`
	UptimePct       float64  `json:"uptime_pct"`
	WorstDevicePct  *float64 `json:"worst_device_pct"`
	WorstDeviceName string   `json:"worst_device_name"`
	DowntimeSec     int64    `json:"downtime_sec"`
	Source          string   `json:"source"`
}

type offenderDTO struct {
	DeviceID  int64        `json:"device_id"`
	Name      string       `json:"name"`
	GroupName string       `json:"group_name"`
	Status    model.Status `json:"status"`
	Checks    int64        `json:"checks"`
	Failures  int64        `json:"failures"`
	UptimePct float64      `json:"uptime_pct"`
	LastError string       `json:"last_error"`
}

// --- event payloads ----------------------------------------------------------
//
// Exported because the engine builds them: the evaluator is the only place that
// knows a transition happened, and the hub is how it says so.

// DeviceStatusEvent is published on a state transition, not on every probe.
type DeviceStatusEvent struct {
	DeviceID       int64        `json:"device_id"`
	Name           string       `json:"name"`
	GroupID        *int64       `json:"group_id"`
	GroupName      string       `json:"group_name"`
	Status         model.Status `json:"status"`
	PreviousStatus model.Status `json:"previous_status"`
	At             string       `json:"at"`
	LatencyMS      int64        `json:"latency_ms"`
	Class          string       `json:"class"`
	Error          string       `json:"error,omitempty"`
	IncidentID     *int64       `json:"incident_id,omitempty"`
}

// HeartbeatEvent is published for every probe result.
type HeartbeatEvent struct {
	DeviceID  int64        `json:"device_id"`
	Status    model.Status `json:"status"`
	LatencyMS int64        `json:"latency_ms"`
	T         int64        `json:"t"`
	Class     string       `json:"class"`
	Error     string       `json:"error,omitempty"`
}

// IncidentEvent is published when an outage opens or closes.
type IncidentEvent struct {
	ID          int64   `json:"id"`
	DeviceID    int64   `json:"device_id"`
	DeviceName  string  `json:"device_name"`
	GroupID     *int64  `json:"group_id"`
	State       string  `json:"state"` // "opened" | "resolved"
	StartedAt   string  `json:"started_at"`
	ResolvedAt  *string `json:"resolved_at"`
	DurationSec *int64  `json:"duration_sec"`
	Cause       string  `json:"cause"`
}

// GroupStatusEvent carries a group's tallies after a member changed state, so
// section headers update without every open window refetching the list.
type GroupStatusEvent struct {
	GroupID     *int64   `json:"group_id"`
	Name        string   `json:"name"`
	Members     int      `json:"members"`
	Up          int      `json:"up"`
	Down        int      `json:"down"`
	Unknown     int      `json:"unknown"`
	Paused      int      `json:"paused"`
	UptimeToday *float64 `json:"uptime_today_pct"`
}

// NewGroupStatusEvent builds the payload from store tallies.
func NewGroupStatusEvent(st store.GroupStat) GroupStatusEvent {
	ev := GroupStatusEvent{
		Name:        "Ungrouped",
		Members:     st.Members,
		Up:          st.Up,
		Down:        st.Down,
		Unknown:     st.Unknown,
		Paused:      st.Paused,
		UptimeToday: st.UptimeToday,
	}
	if st.Group != nil {
		id := st.Group.ID
		ev.GroupID = &id
		ev.Name = st.Group.Name
	}
	return ev
}

// SettingsEvent names the keys a settings write touched. The values are not
// included: one of them is the SMTP password.
type SettingsEvent struct {
	ChangedKeys []string `json:"changed_keys"`
}

// splitTags turns the stored comma-separated list into an array, which is what
// the UI filters on.
func splitTags(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// joinTags is the inverse, normalising spacing on the way in.
func joinTags(tags []string) string {
	clean := make([]string, 0, len(tags))
	for _, t := range tags {
		if p := strings.TrimSpace(t); p != "" {
			clean = append(clean, p)
		}
	}
	return strings.Join(clean, ",")
}

func strptr(s string) *string { return &s }
