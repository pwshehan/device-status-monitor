package model

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Effective is a device's settings after resolving the three-tier chain
//
//	device ?? group ?? global default
//
// Resolve is the single place that chain is evaluated. Nothing else may reach
// for a raw Device field that has a group-level counterpart, and no query may
// COALESCE its way to one of these values.
type Effective struct {
	DeviceID          int64
	GroupID           *int64
	GroupName         string
	Name              string
	Addr              string
	Interval          time.Duration
	Timeout           time.Duration
	FailureThreshold  int
	RecoveryThreshold int
	Notify            bool
	Recipients        []string
	PausedUntil       time.Time

	// Source records where each resolved value came from, so the API can answer
	// "why is this device checking every 60 seconds?" without guesswork.
	// Keys: interval, timeout, failure_threshold, recovery_threshold, recipients.
	Source map[string]string
}

// Paused reports whether probing is suppressed at now, by either the device's
// own pause or its group's maintenance window.
func (e Effective) Paused(now time.Time) bool {
	return !e.PausedUntil.IsZero() && e.PausedUntil.After(now)
}

// Resolve folds a device, its group (nil when ungrouped) and the global
// defaults into one Effective.
func Resolve(d Device, g *Group, def Defaults) Effective {
	e := Effective{
		DeviceID: d.ID,
		GroupID:  d.GroupID,
		Name:     d.Name,
		Addr:     net.JoinHostPort(d.IPAddress, strconv.Itoa(d.Port)),
		Source:   make(map[string]string, 5),
	}
	if g != nil {
		e.GroupName = g.Name
	}

	// Notify is an AND: silencing a group silences its members, and silencing a
	// device is not undone by its group.
	e.Notify = d.Notify && (g == nil || g.Notify)

	interval := resolveInt("interval", e.Source, d.CheckIntervalSec, groupInt(g, func(g *Group) *int { return g.CheckIntervalSec }), def.CheckIntervalSec, g)
	timeout := resolveInt("timeout", e.Source, d.TimeoutSec, groupInt(g, func(g *Group) *int { return g.TimeoutSec }), def.TimeoutSec, g)
	e.Interval = time.Duration(interval) * time.Second
	e.Timeout = time.Duration(timeout) * time.Second
	e.FailureThreshold = resolveInt("failure_threshold", e.Source, d.FailureThreshold, groupInt(g, func(g *Group) *int { return g.FailureThreshold }), def.FailureThreshold, g)
	e.RecoveryThreshold = resolveInt("recovery_threshold", e.Source, d.RecoveryThreshold, groupInt(g, func(g *Group) *int { return g.RecoveryThreshold }), def.RecoveryThreshold, g)

	switch {
	case g != nil && g.Recipients != nil && strings.TrimSpace(*g.Recipients) != "":
		e.Recipients = splitList(*g.Recipients)
		e.Source["recipients"] = "group:" + g.Name
	default:
		e.Recipients = splitList(def.Recipients)
		e.Source["recipients"] = "global"
	}

	// Effective pause is a maximum, never an assignment: a group maintenance
	// window must not overwrite a device's own pause, or resuming the group
	// would silently un-pause a device somebody paused deliberately.
	if d.PausedUntil != nil {
		e.PausedUntil = *d.PausedUntil
	}
	if g != nil && g.PausedUntil != nil && g.PausedUntil.After(e.PausedUntil) {
		e.PausedUntil = *g.PausedUntil
	}
	return e
}

func groupInt(g *Group, pick func(*Group) *int) *int {
	if g == nil {
		return nil
	}
	return pick(g)
}

func resolveInt(key string, src map[string]string, device, group *int, global int, g *Group) int {
	if device != nil {
		src[key] = "device"
		return *device
	}
	if group != nil {
		src[key] = "group:" + g.Name
		return *group
	}
	src[key] = "global"
	return global
}

func splitList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// String renders an Effective compactly for logs.
func (e Effective) String() string {
	return fmt.Sprintf("device=%d %s %s every %s timeout %s (%d/%d strikes)",
		e.DeviceID, e.Name, e.Addr, e.Interval, e.Timeout, e.FailureThreshold, e.RecoveryThreshold)
}
