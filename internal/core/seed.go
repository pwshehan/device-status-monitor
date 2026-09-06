package core

import (
	"context"
	"fmt"

	"github.com/pwshehan/device-status-monitor/internal/model"
	"github.com/pwshehan/device-status-monitor/internal/store"
)

func strptr(s string) *string { return &s }
func intptr(i int) *int       { return &i }

// Seed creates two groups and three devices, for exercising the engine before
// the API exists. It is a no-op once any device is present, so restarting a
// seeded dev database does not duplicate anything.
//
// The endpoints are chosen to cover all three outcomes: one that answers, one
// that is reachable but has nothing listening, and one that is silent.
func Seed(ctx context.Context, st *store.Store) (int, error) {
	existing, err := st.ListDevices(ctx, store.DeviceFilter{})
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, nil
	}

	office, err := st.CreateGroup(ctx, model.Group{
		Name: "Head Office", Color: "#0d6a73", SortOrder: 0, Notify: true,
	})
	if err != nil {
		return 0, fmt.Errorf("create group: %w", err)
	}
	warehouse, err := st.CreateGroup(ctx, model.Group{
		Name: "Warehouse", Color: "#a83227", SortOrder: 1, Notify: true,
		// A group-level override, so the inheritance chain is exercised.
		CheckIntervalSec: intptr(15),
		Recipients:       strptr("warehouse@example.com"),
	})
	if err != nil {
		return 0, fmt.Errorf("create group: %w", err)
	}

	devices := []model.Device{
		{
			// Answers: a public resolver on port 53.
			Name: "Cloudflare DNS", IPAddress: "1.1.1.1", Port: 53,
			GroupID: &office.ID, Enabled: true, Notify: true, Tags: "external",
			CheckIntervalSec: intptr(10),
		},
		{
			// Reachable, nothing listening: expect REFUSED.
			Name: "Loopback, closed port", IPAddress: "127.0.0.1", Port: 9,
			GroupID: &office.ID, Enabled: true, Notify: true, Tags: "local",
			CheckIntervalSec: intptr(10),
		},
		{
			// TEST-NET-1: packets are dropped, so expect TIMEOUT.
			Name: "Unreachable host", IPAddress: "192.0.2.1", Port: 80,
			GroupID: &warehouse.ID, Enabled: true, Notify: true, Tags: "local",
			TimeoutSec: intptr(2),
		},
	}

	for _, d := range devices {
		if _, err := st.CreateDevice(ctx, d); err != nil {
			return 0, fmt.Errorf("create device %s: %w", d.Name, err)
		}
	}
	return len(devices), nil
}
