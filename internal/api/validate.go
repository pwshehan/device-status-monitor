package api

import (
	"fmt"
	"net"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// Validation bounds. These mirror the CHECK constraints in the schema on
// purpose: the database is the backstop, but a 422 naming the field is a much
// better answer than a constraint violation.
const (
	MinIntervalSec  = 5
	MinTimeoutSec   = 1
	MaxTimeoutSec   = 60
	MaxPauseMinutes = 60 * 24 * 30 // a month; longer is a disabled device, not a pause
	MaxNameLen      = 100
	MaxTagsLen      = 200
	MaxBulkIDs      = 500
)

// fieldError is a validation failure aimed at one input.
type fieldError struct {
	Field   string
	Message string
}

func (e fieldError) Error() string { return e.Field + ": " + e.Message }

func fieldErr(field, format string, args ...any) error {
	return fieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// colorPattern accepts the hex colours the group manager's swatches produce.
var colorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// hostnamePattern is deliberately permissive: it accepts anything that could
// be a DNS label sequence. Rejecting an unusual but valid internal hostname
// would be worse than accepting one that never resolves — the first failed
// probe says so plainly, in the UI, with the DNS error attached.
var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9\-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9\-]{0,61}[A-Za-z0-9])?)*$`)

func validateHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fieldErr("ip_address", "an address or hostname is required")
	}
	if len(host) > 255 {
		return fieldErr("ip_address", "address is too long")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if !hostnamePattern.MatchString(host) {
		return fieldErr("ip_address", "%q is neither an IP address nor a hostname", host)
	}
	return nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fieldErr("port", "port must be between 1 and 65535")
	}
	return nil
}

func validateName(field, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fieldErr(field, "a name is required")
	}
	if len(name) > MaxNameLen {
		return fieldErr(field, "name must be at most %d characters", MaxNameLen)
	}
	return nil
}

// validateOverrides checks the four inheritable settings. nil means inherit and
// is always valid — that is the whole point of them being nullable.
func validateOverrides(interval, timeout, failure, recovery *int) error {
	if interval != nil && *interval < MinIntervalSec {
		return fieldErr("check_interval_sec",
			"interval must be at least %d seconds, or null to inherit", MinIntervalSec)
	}
	if timeout != nil && (*timeout < MinTimeoutSec || *timeout > MaxTimeoutSec) {
		return fieldErr("timeout_sec",
			"timeout must be between %d and %d seconds, or null to inherit",
			MinTimeoutSec, MaxTimeoutSec)
	}
	if failure != nil && *failure < 1 {
		return fieldErr("failure_threshold",
			"failure threshold must be at least 1, or null to inherit")
	}
	if recovery != nil && *recovery < 1 {
		return fieldErr("recovery_threshold",
			"recovery threshold must be at least 1, or null to inherit")
	}
	// A timeout longer than the interval means probes overlap forever. The
	// resolved pair is what matters, so this is only checked when both are
	// overridden on the same row; the effective-value check lives in the
	// engine's own logging.
	if interval != nil && timeout != nil && *timeout > *interval {
		return fieldErr("timeout_sec", "timeout must not exceed the check interval")
	}
	return nil
}

func validateTags(tags []string) error {
	joined := joinTags(tags)
	if len(joined) > MaxTagsLen {
		return fieldErr("tags", "tags must be at most %d characters in total", MaxTagsLen)
	}
	for _, t := range tags {
		if strings.Contains(t, ",") {
			return fieldErr("tags", "a tag may not contain a comma")
		}
	}
	return nil
}

// validateRecipients accepts a comma-separated list and rejects the whole
// field if any address is unparseable: a typo in the third address would
// otherwise silently drop the alert for that person only.
func validateRecipients(field, list string) error {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	for _, addr := range strings.Split(list, ",") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if _, err := mail.ParseAddress(addr); err != nil {
			return fieldErr(field, "%q is not a valid email address", addr)
		}
	}
	return nil
}

// pauseRequest is the shared body of the two pause endpoints. All three shapes
// mean something different:
//
//	{"minutes": 30}   pause for 30 minutes from now
//	{"until": "..."}  pause until an explicit instant
//	{} or null fields resume
type pauseRequest struct {
	Minutes Opt[int]    `json:"minutes"`
	Until   Opt[string] `json:"until"`
}

// resolve turns a pause request into an absolute instant, or nil to resume.
func (p pauseRequest) resolve(now time.Time) (*time.Time, error) {
	switch {
	case p.Minutes.Set && p.Minutes.Valid:
		m := p.Minutes.Value
		if m <= 0 {
			return nil, fieldErr("minutes", "minutes must be positive; send null to resume")
		}
		if m > MaxPauseMinutes {
			return nil, fieldErr("minutes",
				"a pause longer than %d minutes should be a disabled device instead",
				MaxPauseMinutes)
		}
		until := now.Add(time.Duration(m) * time.Minute)
		return &until, nil

	case p.Until.Set && p.Until.Valid:
		until, err := time.Parse(time.RFC3339, p.Until.Value)
		if err != nil {
			return nil, fieldErr("until", "until must be an RFC3339 timestamp")
		}
		if !until.After(now) {
			return nil, fieldErr("until", "until is in the past; send null to resume")
		}
		return &until, nil

	default:
		// Either an explicit null or an empty body: resume.
		return nil, nil
	}
}
