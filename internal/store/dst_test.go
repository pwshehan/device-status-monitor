package store

import (
	"context"
	"testing"
	"time"

	// Embedded so LoadLocation works whatever the host has installed. Test-only:
	// production reads the machine's own timezone, which is the point.
	_ "time/tzdata"

	"github.com/gkgraphite/device-status-monitor/internal/model"
)

// TestDowntimeAcrossDaylightSaving covers the risk §14 names: a clock change
// skewing daily figures twice a year.
//
// The two transitions are not symmetric bugs. On the spring-forward day the
// local day is 23 hours long, so an all-day outage that reports 24 is claiming
// an hour that did not exist; on the autumn-back day it is 25, and reporting 24
// silently loses one. Both come out of the same arithmetic, which is why the
// day's bounds are computed with AddDate on a local date rather than by adding
// 24 hours to a timestamp.
func TestDowntimeAcrossDaylightSaving(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no timezone database: %v", err)
	}

	// This is what production reads. Swapping it is the only way to exercise a
	// transition from a machine in a zone that has none — India, where this was
	// written, never changes its clocks.
	original := time.Local
	time.Local = newYork
	t.Cleanup(func() { time.Local = original })

	cases := []struct {
		name string
		day  string
		want int64
	}{
		// 2026-03-08: 02:00 becomes 03:00. The day is 23 hours long.
		{"spring forward is a 23-hour day", "2026-03-08", 23 * 3600},
		// 2026-11-01: 02:00 becomes 01:00. The day is 25 hours long.
		{"autumn back is a 25-hour day", "2026-11-01", 25 * 3600},
		// An ordinary day, as a control: if this one is wrong the arithmetic is
		// broken generally rather than only at a transition.
		{"an ordinary day is 24 hours", "2026-06-15", 24 * 3600},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := open(t)

			d, err := s.CreateDevice(ctx, model.Device{
				Name: "switch", IPAddress: "10.0.0.1", Port: 22, Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}

			// Down for the whole of that local day, and beyond it in both
			// directions, so the figure is decided by the day's bounds rather
			// than by the incident's.
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, newYork)
			end := time.Date(2027, 1, 1, 0, 0, 0, 0, newYork)
			id, err := s.OpenIncident(ctx, d.ID, start, start, "TIMEOUT: i/o timeout", true)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CloseIncident(ctx, id, end, true); err != nil {
				t.Fatal(err)
			}

			got, err := s.DowntimeForDay(ctx, d.ID, tc.day)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("downtime for %s = %ds (%.2fh), want %ds (%.2fh)",
					tc.day, got, float64(got)/3600, tc.want, float64(tc.want)/3600)
			}
		})
	}
}

// TestGoAndSQLiteAgreeOnTheLocalDay checks the seam between the two clocks.
//
// Days are discovered by SQLite (`date(…,'localtime')`, when grouping raw rows)
// and bounded by Go (ParseInLocation and AddDate, when clipping incidents). A
// day that one of them places differently from the other would put a rollup's
// checks and its downtime on different dates — so this asserts they agree, on
// whatever timezone the machine running the tests happens to use.
func TestGoAndSQLiteAgreeOnTheLocalDay(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	d, err := s.CreateDevice(ctx, model.Device{
		Name: "switch", IPAddress: "10.0.0.1", Port: 22, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Times chosen to land either side of every boundary that matters: local
	// midnight, local noon, and the UTC day boundary, which is a different
	// instant in every zone but UTC.
	base := time.Date(2026, 3, 8, 0, 0, 0, 0, time.Local)
	var offsets []time.Duration
	for _, h := range []int{0, 1, 2, 3, 12, 23} {
		offsets = append(offsets, time.Duration(h)*time.Hour)
	}
	offsets = append(offsets, 23*time.Hour+59*time.Minute)

	var batch []model.Heartbeat
	for _, off := range offsets {
		batch = append(batch, model.Heartbeat{
			DeviceID: d.ID, Status: model.StatusUp, LatencyMS: 4,
			CheckedAt: base.Add(off),
		})
	}
	if err := s.InsertHeartbeats(ctx, batch); err != nil {
		t.Fatal(err)
	}

	// SQLite's view: which local day did each row land on?
	rows, err := s.PendingRollups(ctx, time.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no candidate days")
	}

	for _, r := range rows {
		// Go's view: the day's bounds, from the same date string.
		start, err := time.ParseInLocation(dayFormat, r.Day, time.Local)
		if err != nil {
			t.Fatalf("parse %q: %v", r.Day, err)
		}
		end := start.AddDate(0, 0, 1)

		// Every row SQLite assigned to this day must fall inside the window Go
		// computes for it. A mismatch here is a rollup whose checks and whose
		// downtime describe different days.
		var inside int
		for _, hb := range batch {
			if hb.CheckedAt.Before(end) && !hb.CheckedAt.Before(start) {
				inside++
			}
		}
		if int64(inside) != r.ChecksTotal {
			t.Errorf("day %s: SQLite counted %d rows, Go's bounds [%s, %s) contain %d",
				r.Day, r.ChecksTotal, start, end, inside)
		}
	}
}
