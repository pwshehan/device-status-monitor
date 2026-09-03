package model

import (
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

var defs = Defaults{
	CheckIntervalSec:  30,
	TimeoutSec:        3,
	FailureThreshold:  3,
	RecoveryThreshold: 1,
	Recipients:        "ops@example.com, oncall@example.com",
}

func TestResolveTiers(t *testing.T) {
	tests := []struct {
		name         string
		device       Device
		group        *Group
		wantInterval time.Duration
		wantTimeout  time.Duration
		wantFail     int
		wantSource   string // source of "interval"
	}{
		{
			name:         "all inherited from global",
			device:       Device{ID: 1, Notify: true},
			group:        nil,
			wantInterval: 30 * time.Second,
			wantTimeout:  3 * time.Second,
			wantFail:     3,
			wantSource:   "global",
		},
		{
			name:         "group overrides global",
			device:       Device{ID: 1, Notify: true},
			group:        &Group{Name: "Warehouse", Notify: true, CheckIntervalSec: ptr(60), FailureThreshold: ptr(5)},
			wantInterval: 60 * time.Second,
			wantTimeout:  3 * time.Second, // still global
			wantFail:     5,
			wantSource:   "group:Warehouse",
		},
		{
			name:         "device overrides group",
			device:       Device{ID: 1, Notify: true, CheckIntervalSec: ptr(10)},
			group:        &Group{Name: "Warehouse", Notify: true, CheckIntervalSec: ptr(60)},
			wantInterval: 10 * time.Second,
			wantTimeout:  3 * time.Second,
			wantFail:     3,
			wantSource:   "device",
		},
		{
			name:         "device overrides global with no group",
			device:       Device{ID: 1, Notify: true, TimeoutSec: ptr(9), CheckIntervalSec: ptr(15)},
			group:        nil,
			wantInterval: 15 * time.Second,
			wantTimeout:  9 * time.Second,
			wantFail:     3,
			wantSource:   "device",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.device, tc.group, defs)
			if got.Interval != tc.wantInterval {
				t.Errorf("interval = %s, want %s", got.Interval, tc.wantInterval)
			}
			if got.Timeout != tc.wantTimeout {
				t.Errorf("timeout = %s, want %s", got.Timeout, tc.wantTimeout)
			}
			if got.FailureThreshold != tc.wantFail {
				t.Errorf("failure threshold = %d, want %d", got.FailureThreshold, tc.wantFail)
			}
			if got.Source["interval"] != tc.wantSource {
				t.Errorf("source[interval] = %q, want %q", got.Source["interval"], tc.wantSource)
			}
		})
	}
}

func TestResolveNotifyIsAnd(t *testing.T) {
	tests := []struct {
		device, group, want bool
	}{
		{true, true, true},
		{true, false, false}, // silencing a group silences its members
		{false, true, false}, // a group cannot un-silence a device
		{false, false, false},
	}
	for _, tc := range tests {
		got := Resolve(Device{Notify: tc.device}, &Group{Name: "G", Notify: tc.group}, defs)
		if got.Notify != tc.want {
			t.Errorf("device=%v group=%v: notify = %v, want %v", tc.device, tc.group, got.Notify, tc.want)
		}
	}
	// Ungrouped devices keep their own setting.
	if got := Resolve(Device{Notify: true}, nil, defs); !got.Notify {
		t.Error("ungrouped notifying device should notify")
	}
}

func TestResolveRecipients(t *testing.T) {
	global := Resolve(Device{Notify: true}, nil, defs)
	if len(global.Recipients) != 2 || global.Recipients[0] != "ops@example.com" {
		t.Fatalf("global recipients = %v", global.Recipients)
	}
	if global.Source["recipients"] != "global" {
		t.Errorf("source = %q", global.Source["recipients"])
	}

	grouped := Resolve(Device{Notify: true}, &Group{Name: "Warehouse", Notify: true, Recipients: ptr("warehouse@example.com")}, defs)
	if len(grouped.Recipients) != 1 || grouped.Recipients[0] != "warehouse@example.com" {
		t.Fatalf("group recipients = %v", grouped.Recipients)
	}
	if grouped.Source["recipients"] != "group:Warehouse" {
		t.Errorf("source = %q", grouped.Source["recipients"])
	}

	// An empty group recipients string falls through rather than muting alerts.
	blank := Resolve(Device{Notify: true}, &Group{Name: "W", Notify: true, Recipients: ptr("   ")}, defs)
	if len(blank.Recipients) != 2 {
		t.Errorf("blank group recipients should fall through to global, got %v", blank.Recipients)
	}
}

func TestResolvePauseIsMaximum(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	soon, later := now.Add(time.Hour), now.Add(4*time.Hour)

	cases := []struct {
		name         string
		devicePause  *time.Time
		groupPause   *time.Time
		wantPausedAt time.Time
		wantPaused   bool
	}{
		{"neither", nil, nil, time.Time{}, false},
		{"device only", &soon, nil, soon, true},
		{"group only", nil, &soon, soon, true},
		{"group window is longer", &soon, &later, later, true},
		{"device pause is longer", &later, &soon, later, true},
		{"expired pause", ptr(now.Add(-time.Minute)), nil, now.Add(-time.Minute), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(
				Device{PausedUntil: tc.devicePause},
				&Group{Name: "G", PausedUntil: tc.groupPause},
				defs,
			)
			if !got.PausedUntil.Equal(tc.wantPausedAt) {
				t.Errorf("PausedUntil = %v, want %v", got.PausedUntil, tc.wantPausedAt)
			}
			if got.Paused(now) != tc.wantPaused {
				t.Errorf("Paused(now) = %v, want %v", got.Paused(now), tc.wantPaused)
			}
		})
	}
}

func TestResolveAddr(t *testing.T) {
	if got := Resolve(Device{IPAddress: "10.0.0.1", Port: 22}, nil, defs); got.Addr != "10.0.0.1:22" {
		t.Errorf("addr = %q", got.Addr)
	}
	// IPv6 needs bracketing, which is why this goes through net.JoinHostPort.
	if got := Resolve(Device{IPAddress: "::1", Port: 443}, nil, defs); got.Addr != "[::1]:443" {
		t.Errorf("addr = %q", got.Addr)
	}
}
