// Package model holds the domain types shared by the store, the scheduler,
// the state machine and the notifier.
package model

import "time"

// Status is a device's current reachability as decided by the state machine.
type Status string

const (
	StatusUnknown Status = "UNKNOWN"
	StatusUp      Status = "UP"
	StatusDown    Status = "DOWN"
)

// Group is the structural home of a device: one group per device, no nesting.
// The four probe-setting pointers are nil when the group inherits the global
// default, so "inherited" stays representable.
type Group struct {
	ID                int64
	Name              string
	Description       string
	Color             string
	SortOrder         int
	CheckIntervalSec  *int
	TimeoutSec        *int
	FailureThreshold  *int
	RecoveryThreshold *int
	Notify            bool
	Recipients        *string
	PausedUntil       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Device is one monitored TCP endpoint. GroupID is nil for ungrouped devices,
// and the four probe-setting pointers are nil when the device inherits.
type Device struct {
	ID                int64
	GroupID           *int64
	Name              string
	IPAddress         string
	Port              int
	CheckIntervalSec  *int
	TimeoutSec        *int
	FailureThreshold  *int
	RecoveryThreshold *int
	Enabled           bool
	Notify            bool
	PausedUntil       *time.Time
	Tags              string

	Status               Status
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	FirstFailureAt       *time.Time
	LastCheckAt          *time.Time
	LastLatencyMS        *int64
	LastError            string
	LastStatusChangeAt   *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Incident is one outage episode. StartedAt is the first failed probe, not the
// probe that tripped the threshold, so downtime is accurate across restarts.
type Incident struct {
	ID           int64
	DeviceID     int64
	StartedAt    time.Time
	DetectedAt   time.Time
	ResolvedAt   *time.Time
	DurationSec  *int64
	Cause        string
	AlertSent    bool
	RecoverySent bool
}

// Heartbeat is one probe result as stored in the raw time series.
type Heartbeat struct {
	DeviceID  int64
	Status    Status
	LatencyMS int64
	ErrorMsg  string
	CheckedAt time.Time
}

// AlertKind identifies an outbox row's template.
type AlertKind string

const (
	AlertDown     AlertKind = "DOWN"
	AlertRecovery AlertKind = "RECOVERY"
	AlertTest     AlertKind = "TEST"
	AlertDigest   AlertKind = "DIGEST"
)

// Alert is a queued outbound email. It is durable on purpose: the mail about a
// network outage cannot be sent over that outage.
type Alert struct {
	ID            int64
	IncidentID    *int64
	Kind          AlertKind
	Subject       string
	BodyText      string
	BodyHTML      string
	Recipients    string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	SentAt        *time.Time
}

// Defaults is the bottom tier of the effective-value chain, read from settings.
type Defaults struct {
	CheckIntervalSec  int
	TimeoutSec        int
	FailureThreshold  int
	RecoveryThreshold int
	Recipients        string
}
