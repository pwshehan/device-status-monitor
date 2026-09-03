package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/model"
	"github.com/gkgraphite/device-status-monitor/internal/probe"
	"github.com/gkgraphite/device-status-monitor/internal/store"
)

// fakeEngine stands in for the running monitor: the handlers only need to know
// that a reload was asked for, not that anything was rescheduled.
type fakeEngine struct {
	st       *store.Store
	mu       sync.Mutex
	reloads  int
	checkRes probe.Result
	checkOK  bool
	testTo   []string
	testErr  error
	password string
	pwSet    bool
}

func (f *fakeEngine) Reload() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
}

func (f *fakeEngine) reloadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reloads
}

func (f *fakeEngine) CheckNow(context.Context, int64) (probe.Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkRes, f.checkOK
}

func (f *fakeEngine) Uptime() time.Duration { return 42 * time.Second }
func (f *fakeEngine) Running() int          { return 3 }
func (f *fakeEngine) SchedulerLagMS() int64 { return 17 }

func (f *fakeEngine) SendTestEmail(_ context.Context, to []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.testTo = to
	return f.testErr
}

// SaveSMTPPassword records the plaintext it was handed and writes the sealed
// slot the way notify.Worker does, so the settings handler sees the same
// has_password it would in production. What it must never do is store the
// plaintext itself.
func (f *fakeEngine) SaveSMTPPassword(ctx context.Context, plaintext string) error {
	f.mu.Lock()
	f.password, f.pwSet = plaintext, true
	st := f.st
	f.mu.Unlock()

	if st == nil {
		return nil
	}
	sealed := ""
	if plaintext != "" {
		sealed = "aesgcm:" + strings.Repeat("x", 32)
	}
	return st.PutSettings(ctx, map[string]string{store.KeySMTPPasswordEnc: sealed})
}

// env is one API under test: a real database, a stub engine, and a live
// listener so the middleware runs exactly as it does in production.
type env struct {
	t     *testing.T
	st    *store.Store
	eng   *fakeEngine
	srv   *Server
	http  *httptest.Server
	token string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SeedSettings(ctx); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	eng := &fakeEngine{st: st, checkOK: true, checkRes: probe.Result{
		OK: true, LatencyMS: 7, Class: probe.ClassOK, At: time.Now(),
	}}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, eng, NewHub(), Config{Token: "test-token", Version: "test"}, quiet)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &env{t: t, st: st, eng: eng, srv: srv, http: ts, token: "test-token"}
}

// request builds an authenticated request against the test listener.
func (e *env) request(method, path string, body any) *http.Request {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("encode body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.http.URL+path, rdr)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// call sends a request and decodes the JSON response into out, which may be
// nil. It returns the status code.
func (e *env) call(method, path string, body, out any) int {
	e.t.Helper()
	res, err := e.http.Client().Do(e.request(method, path, body))
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return res.StatusCode
}

// mustCall fails the test unless the status matches.
func (e *env) mustCall(method, path string, body, out any, want int) {
	e.t.Helper()
	if got := e.call(method, path, body, out); got != want {
		e.t.Fatalf("%s %s = %d, want %d", method, path, got, want)
	}
}

// localRequest builds a request that looks like it came from the loopback
// listener. httptest.NewRequest defaults to RemoteAddr 192.0.2.1 and Host
// example.com, and this API rejects both — so a test driving the handler
// directly has to say otherwise or it only ever exercises the middleware.
func localRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:49215"
	return req
}

// errorOf decodes the standard error envelope.
func errorOf(t *testing.T, raw json.RawMessage) errorDetail {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body %q: %v", raw, err)
	}
	return body.Error
}

// --- auth and hardening ------------------------------------------------------

func TestHealthNeedsNoToken(t *testing.T) {
	e := newEnv(t)

	res, err := http.Get(e.http.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/health without a token = %d, want 200", res.StatusCode)
	}

	var health healthResponse
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	// The point of the endpoint: a GUI with a stale token can still tell that
	// the service is alive.
	if !health.DBOK || !health.OK {
		t.Errorf("health = %+v, want ok with a working database", health)
	}
	if health.SchedulerLagMS != 17 || health.Monitored != 3 {
		t.Errorf("health did not report the engine's figures: %+v", health)
	}
}

func TestAuthenticatedRoutesRequireTheToken(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic test-token", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"empty token", "Bearer ", http.StatusUnauthorized},
		{"correct token", "Bearer test-token", http.StatusOK},
		{"case-insensitive scheme", "bearer test-token", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, e.http.URL+"/api/devices", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			res, err := e.http.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

func TestNonLoopbackRequestIsRejected(t *testing.T) {
	e := newEnv(t)

	// Driven through the handler directly: the whole point is a RemoteAddr the
	// listener would never produce.
	req := localRequest(http.MethodGet, "/api/devices", nil)
	req.RemoteAddr = "192.168.1.50:5555"
	req.Header.Set("Authorization", "Bearer "+e.token)

	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if code := errorOf(t, rec.Body.Bytes()).Code; code != CodeForbidden {
		t.Errorf("code = %q, want %q", code, CodeForbidden)
	}
}

func TestRebindingHostAndOriginAreRejected(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"attacker host", "monitor.attacker.example", "", http.StatusForbidden},
		{"attacker origin", "127.0.0.1:49215", "https://attacker.example", http.StatusForbidden},
		{"loopback host", "127.0.0.1:49215", "", http.StatusOK},
		{"localhost host", "localhost:49215", "", http.StatusOK},
		{"dev server origin", "127.0.0.1:49215", "http://localhost:5173", http.StatusOK},
		{"tauri origin", "127.0.0.1:49215", "tauri://localhost", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := localRequest(http.MethodGet, "/api/devices", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			req.Header.Set("Authorization", "Bearer "+e.token)

			rec := httptest.NewRecorder()
			e.srv.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	e := newEnv(t)

	// A valid device with an absurd tag list: rejected on size, not on shape.
	huge := strings.Repeat("x", MaxBodyBytes+1024)
	body := map[string]any{"name": "big", "ip_address": "10.0.0.1", "port": 22, "tags": []string{huge}}

	var raw json.RawMessage
	status := e.call(http.MethodPost, "/api/devices", body, &raw)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", status)
	}
}

func TestUnknownFieldIsRejected(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{
		"name": "typo", "ip_address": "10.0.0.1", "port": 22,
		"check_interval_secs": 30, // note the s
	}
	e.mustCall(http.MethodPost, "/api/devices", body, nil, http.StatusBadRequest)
}

// --- devices -----------------------------------------------------------------

// deviceEnvelope is the shape every device response uses.
type deviceEnvelope struct {
	Device       deviceDTO    `json:"device"`
	OpenIncident *incidentDTO `json:"open_incident"`
}

func TestDeviceCRUDCarriesInheritance(t *testing.T) {
	e := newEnv(t)

	var groupRes struct{ Group groupDTO }
	e.mustCall(http.MethodPost, "/api/groups", map[string]any{
		"name": "Warehouse", "check_interval_sec": 15,
		"recipients": "warehouse@example.com",
	}, &groupRes, http.StatusCreated)
	groupID := *groupRes.Group.ID

	// Created with no overrides at all: everything should resolve through the
	// group, and the response should say so.
	var created deviceEnvelope
	e.mustCall(http.MethodPost, "/api/devices", map[string]any{
		"name": "Floor 2 switch", "ip_address": "10.0.7.3", "port": 22,
		"group_id": groupID, "tags": []string{"critical", "network"},
	}, &created, http.StatusCreated)

	d := created.Device
	if d.CheckIntervalSec != nil {
		t.Errorf("check_interval_sec = %v, want null: the device inherits", *d.CheckIntervalSec)
	}
	if d.Effective.CheckIntervalSec != 15 {
		t.Errorf("effective interval = %d, want 15 from the group", d.Effective.CheckIntervalSec)
	}
	if got := d.Effective.Source["interval"]; got != "group:Warehouse" {
		t.Errorf("source[interval] = %q, want group:Warehouse", got)
	}
	if d.Effective.TimeoutSec != 3 || d.Effective.Source["timeout"] != "global" {
		t.Errorf("timeout should fall through to the global default, got %d from %q",
			d.Effective.TimeoutSec, d.Effective.Source["timeout"])
	}
	if len(d.Effective.Recipients) != 1 || d.Effective.Recipients[0] != "warehouse@example.com" {
		t.Errorf("recipients = %v, want the group's", d.Effective.Recipients)
	}
	if len(d.Tags) != 2 {
		t.Errorf("tags = %v, want two", d.Tags)
	}
	if e.eng.reloadCount() == 0 {
		t.Error("creating a device must reload the engine")
	}

	// Override the interval, then clear it back to inherited with null. This
	// is the distinction the whole Opt type exists for.
	var patched deviceEnvelope
	e.mustCall(http.MethodPatch, fmt.Sprintf("/api/devices/%d", d.ID),
		map[string]any{"check_interval_sec": 60}, &patched, http.StatusOK)
	if patched.Device.CheckIntervalSec == nil || *patched.Device.CheckIntervalSec != 60 {
		t.Fatalf("override not stored: %+v", patched.Device.CheckIntervalSec)
	}
	if patched.Device.Effective.Source["interval"] != "device" {
		t.Errorf("source[interval] = %q, want device", patched.Device.Effective.Source["interval"])
	}

	// A PATCH that does not mention the field must leave the override alone.
	e.mustCall(http.MethodPatch, fmt.Sprintf("/api/devices/%d", d.ID),
		map[string]any{"name": "Floor 2 core switch"}, &patched, http.StatusOK)
	if patched.Device.CheckIntervalSec == nil || *patched.Device.CheckIntervalSec != 60 {
		t.Error("an unrelated PATCH cleared the interval override")
	}

	// An explicit null resets it.
	e.mustCall(http.MethodPatch, fmt.Sprintf("/api/devices/%d", d.ID),
		map[string]any{"check_interval_sec": nil}, &patched, http.StatusOK)
	if patched.Device.CheckIntervalSec != nil {
		t.Errorf("null did not clear the override: %v", *patched.Device.CheckIntervalSec)
	}
	if patched.Device.Effective.CheckIntervalSec != 15 {
		t.Errorf("effective interval = %d, want the group's 15 again",
			patched.Device.Effective.CheckIntervalSec)
	}

	var fetched deviceEnvelope
	e.mustCall(http.MethodGet, fmt.Sprintf("/api/devices/%d", d.ID), nil, &fetched, http.StatusOK)
	if fetched.OpenIncident != nil {
		t.Errorf("open_incident = %+v, want null", fetched.OpenIncident)
	}

	e.mustCall(http.MethodDelete, fmt.Sprintf("/api/devices/%d", d.ID), nil, nil, http.StatusOK)
	e.mustCall(http.MethodGet, fmt.Sprintf("/api/devices/%d", d.ID), nil, nil, http.StatusNotFound)
}

func TestDuplicateDeviceIsAConflict(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"name": "switch", "ip_address": "10.0.0.9", "port": 22}
	e.mustCall(http.MethodPost, "/api/devices", body, nil, http.StatusCreated)

	body["name"] = "the same switch again"
	var raw json.RawMessage
	if got := e.call(http.MethodPost, "/api/devices", body, &raw); got != http.StatusConflict {
		t.Fatalf("status = %d, want 409", got)
	}
	detail := errorOf(t, raw)
	if detail.Code != CodeConflict || detail.Field != "ip_address" {
		t.Errorf("error = %+v, want a conflict naming ip_address", detail)
	}
}

func TestValidationNamesTheField(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"no name", map[string]any{"ip_address": "10.0.0.1", "port": 22}, "name"},
		{"bad host", map[string]any{"name": "x", "ip_address": "not a host!", "port": 22}, "ip_address"},
		{"port zero", map[string]any{"name": "x", "ip_address": "10.0.0.1", "port": 0}, "port"},
		{"port too high", map[string]any{"name": "x", "ip_address": "10.0.0.1", "port": 70000}, "port"},
		{"interval too small", map[string]any{
			"name": "x", "ip_address": "10.0.0.1", "port": 22, "check_interval_sec": 2,
		}, "check_interval_sec"},
		{"timeout out of range", map[string]any{
			"name": "x", "ip_address": "10.0.0.1", "port": 22, "timeout_sec": 90,
		}, "timeout_sec"},
		{"timeout beyond interval", map[string]any{
			"name": "x", "ip_address": "10.0.0.1", "port": 22,
			"check_interval_sec": 10, "timeout_sec": 30,
		}, "timeout_sec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if got := e.call(http.MethodPost, "/api/devices", tc.body, &raw); got != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", got)
			}
			detail := errorOf(t, raw)
			if detail.Code != CodeValidation {
				t.Errorf("code = %q, want %q", detail.Code, CodeValidation)
			}
			if detail.Field != tc.field {
				t.Errorf("field = %q, want %q", detail.Field, tc.field)
			}
		})
	}
}

func TestPauseAndResumeDevice(t *testing.T) {
	e := newEnv(t)
	var created deviceEnvelope
	e.mustCall(http.MethodPost, "/api/devices", map[string]any{
		"name": "NAS", "ip_address": "10.0.9.5", "port": 445,
	}, &created, http.StatusCreated)
	path := fmt.Sprintf("/api/devices/%d/pause", created.Device.ID)

	var paused struct {
		PausedUntil *string `json:"paused_until"`
	}
	e.mustCall(http.MethodPost, path, map[string]any{"minutes": 30}, &paused, http.StatusOK)
	if paused.PausedUntil == nil {
		t.Fatal("paused_until = null after a 30 minute pause")
	}

	// A null minutes field is how the UI resumes.
	e.mustCall(http.MethodPost, path, map[string]any{"minutes": nil}, &paused, http.StatusOK)
	if paused.PausedUntil != nil {
		t.Errorf("paused_until = %q, want null after resuming", *paused.PausedUntil)
	}

	d, err := e.st.GetDevice(context.Background(), created.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.PausedUntil != nil {
		t.Error("resume did not clear paused_until in the database")
	}
}

func TestCheckNowReportsTheProbe(t *testing.T) {
	e := newEnv(t)
	var created deviceEnvelope
	e.mustCall(http.MethodPost, "/api/devices", map[string]any{
		"name": "resolver", "ip_address": "1.1.1.1", "port": 53,
	}, &created, http.StatusCreated)

	var res struct {
		OK        bool   `json:"ok"`
		Status    string `json:"status"`
		LatencyMS int64  `json:"latency_ms"`
		Class     string `json:"class"`
	}
	e.mustCall(http.MethodPost, fmt.Sprintf("/api/devices/%d/check", created.Device.ID),
		nil, &res, http.StatusOK)
	if !res.OK || res.Status != "UP" || res.LatencyMS != 7 {
		t.Errorf("check = %+v, want the stub's UP/7ms result", res)
	}

	// A device the engine is not running cannot be checked, and saying so is
	// better than reporting a probe that never happened.
	e.eng.mu.Lock()
	e.eng.checkOK = false
	e.eng.mu.Unlock()
	e.mustCall(http.MethodPost, fmt.Sprintf("/api/devices/%d/check", created.Device.ID),
		nil, nil, http.StatusConflict)
}

func TestBulkMoveIsOneReload(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	target, err := e.st.CreateGroup(ctx, model.Group{Name: "Head Office", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 5; i++ {
		d, err := e.st.CreateDevice(ctx, model.Device{
			Name: fmt.Sprintf("device %d", i), IPAddress: fmt.Sprintf("10.1.0.%d", i+1),
			Port: 22, Enabled: true, Notify: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}

	before := e.eng.reloadCount()
	var res struct {
		Op       string `json:"op"`
		Affected int64  `json:"affected"`
	}
	e.mustCall(http.MethodPost, "/api/devices/bulk", map[string]any{
		"ids": ids, "op": "move", "group_id": target.ID,
	}, &res, http.StatusOK)

	if res.Affected != int64(len(ids)) {
		t.Errorf("affected = %d, want %d", res.Affected, len(ids))
	}
	if got := e.eng.reloadCount() - before; got != 1 {
		t.Errorf("reloads = %d, want exactly 1 for a bulk move", got)
	}
	for _, id := range ids {
		d, err := e.st.GetDevice(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.GroupID == nil || *d.GroupID != target.ID {
			t.Fatalf("device %d did not move", id)
		}
	}

	// null is a legitimate destination: move them all back out.
	e.mustCall(http.MethodPost, "/api/devices/bulk", map[string]any{
		"ids": ids, "op": "move", "group_id": nil,
	}, &res, http.StatusOK)
	d, err := e.st.GetDevice(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if d.GroupID != nil {
		t.Error("move to null did not ungroup the devices")
	}

	// An omitted group_id is a mistake, not a move to Ungrouped.
	e.mustCall(http.MethodPost, "/api/devices/bulk", map[string]any{
		"ids": ids, "op": "move",
	}, nil, http.StatusUnprocessableEntity)

	// A move to a group that does not exist is a 404, not a broken foreign key.
	e.mustCall(http.MethodPost, "/api/devices/bulk", map[string]any{
		"ids": ids, "op": "move", "group_id": 9999,
	}, nil, http.StatusNotFound)
}

func TestBulkRejectsUnknownOp(t *testing.T) {
	e := newEnv(t)
	var raw json.RawMessage
	if got := e.call(http.MethodPost, "/api/devices/bulk",
		map[string]any{"ids": []int64{1}, "op": "destroy"}, &raw); got != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", got)
	}
	if field := errorOf(t, raw).Field; field != "op" {
		t.Errorf("field = %q, want op", field)
	}
}

// --- groups ------------------------------------------------------------------

func TestDeleteGroupOrphansDevicesAndSaysHowMany(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	g, err := e.st.CreateGroup(ctx, model.Group{Name: "Warehouse", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.st.CreateDevice(ctx, model.Device{
			Name: fmt.Sprintf("wh %d", i), IPAddress: fmt.Sprintf("10.2.0.%d", i+1),
			Port: 22, GroupID: &g.ID, Enabled: true, Notify: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var res struct {
		Deleted          int64 `json:"deleted"`
		DevicesUngrouped int64 `json:"devices_ungrouped"`
	}
	e.mustCall(http.MethodDelete, fmt.Sprintf("/api/groups/%d", g.ID), nil, &res, http.StatusOK)
	if res.DevicesUngrouped != 3 {
		t.Errorf("devices_ungrouped = %d, want 3", res.DevicesUngrouped)
	}

	// The devices themselves must still be there. Deleting a label never
	// deletes the things it labelled.
	devices, err := e.st.ListDevices(ctx, store.DeviceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 3 {
		t.Fatalf("device count = %d, want 3 survivors", len(devices))
	}
	for _, d := range devices {
		if d.GroupID != nil {
			t.Errorf("device %d still points at the deleted group", d.ID)
		}
	}
}

func TestGroupListCarriesTalliesAndUngroupedBucket(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	g, err := e.st.CreateGroup(ctx, model.Group{Name: "Head Office", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateDevice(ctx, model.Device{
		Name: "in a group", IPAddress: "10.3.0.1", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateDevice(ctx, model.Device{
		Name: "loose", IPAddress: "10.3.0.2", Port: 22, Enabled: true, Notify: true,
	}); err != nil {
		t.Fatal(err)
	}

	var res struct{ Groups []groupDTO }
	e.mustCall(http.MethodGet, "/api/groups", nil, &res, http.StatusOK)
	if len(res.Groups) != 2 {
		t.Fatalf("groups = %d, want the real group plus Ungrouped", len(res.Groups))
	}
	named, ungrouped := res.Groups[0], res.Groups[1]
	if named.ID == nil || *named.ID != g.ID || named.Stats.Members != 1 {
		t.Errorf("first entry = %+v, want the real group with one member", named)
	}
	if !ungrouped.Ungrouped || ungrouped.ID != nil || ungrouped.Stats.Members != 1 {
		t.Errorf("last entry = %+v, want the Ungrouped bucket with one member", ungrouped)
	}
	// Nothing has been probed, so today's uptime is unknown — which is not 0%.
	if named.Stats.UptimeToday != nil {
		t.Errorf("uptime_today_pct = %v, want null before any check", *named.Stats.UptimeToday)
	}
}

func TestGroupPatchReloadsAndClearsOverrides(t *testing.T) {
	e := newEnv(t)

	var created struct{ Group groupDTO }
	e.mustCall(http.MethodPost, "/api/groups", map[string]any{
		"name": "Warehouse", "check_interval_sec": 15, "color": "#a83227",
	}, &created, http.StatusCreated)
	id := *created.Group.ID

	before := e.eng.reloadCount()
	var patched struct{ Group groupDTO }
	e.mustCall(http.MethodPatch, fmt.Sprintf("/api/groups/%d", id),
		map[string]any{"check_interval_sec": nil}, &patched, http.StatusOK)

	if patched.Group.CheckIntervalSec != nil {
		t.Errorf("interval = %v, want null after clearing", *patched.Group.CheckIntervalSec)
	}
	if e.eng.reloadCount() == before {
		t.Error("a group edit must reload every member")
	}

	// Bad colour and bad recipients are both the user's typo, aimed at a field.
	var raw json.RawMessage
	if got := e.call(http.MethodPatch, fmt.Sprintf("/api/groups/%d", id),
		map[string]any{"color": "reddish"}, &raw); got != http.StatusUnprocessableEntity {
		t.Fatalf("colour status = %d, want 422", got)
	}
	if field := errorOf(t, raw).Field; field != "color" {
		t.Errorf("field = %q, want color", field)
	}
	if got := e.call(http.MethodPatch, fmt.Sprintf("/api/groups/%d", id),
		map[string]any{"recipients": "ops@example.com, not-an-address"}, &raw); got != http.StatusUnprocessableEntity {
		t.Fatalf("recipients status = %d, want 422", got)
	}
}

func TestGroupPauseDoesNotTouchMembers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	g, err := e.st.CreateGroup(ctx, model.Group{Name: "Warehouse", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	own := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	d, err := e.st.CreateDevice(ctx, model.Device{
		Name: "NAS", IPAddress: "10.4.0.1", Port: 445, GroupID: &g.ID,
		Enabled: true, Notify: true, PausedUntil: &own,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Open a shorter maintenance window over a device that is paused for
	// longer, then close it. The device's own pause must survive both.
	e.mustCall(http.MethodPost, fmt.Sprintf("/api/groups/%d/pause", g.ID),
		map[string]any{"minutes": 30}, nil, http.StatusOK)
	e.mustCall(http.MethodPost, fmt.Sprintf("/api/groups/%d/pause", g.ID),
		map[string]any{"minutes": nil}, nil, http.StatusOK)

	got, err := e.st.GetDevice(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PausedUntil == nil || !got.PausedUntil.Equal(own.UTC()) {
		t.Errorf("device paused_until = %v, want its own %v untouched", got.PausedUntil, own.UTC())
	}
}

func TestReorderGroupsWritesSortOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	var ids []int64
	for _, name := range []string{"A", "B", "C"} {
		g, err := e.st.CreateGroup(ctx, model.Group{Name: name, Notify: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, g.ID)
	}
	reversed := []int64{ids[2], ids[1], ids[0]}
	e.mustCall(http.MethodPost, "/api/groups/reorder",
		map[string]any{"ids": reversed}, nil, http.StatusOK)

	var res struct{ Groups []groupDTO }
	e.mustCall(http.MethodGet, "/api/groups", nil, &res, http.StatusOK)
	if len(res.Groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(res.Groups))
	}
	for i, want := range reversed {
		if res.Groups[i].ID == nil || *res.Groups[i].ID != want {
			t.Fatalf("position %d = %v, want group %d", i, res.Groups[i].ID, want)
		}
	}
}

// --- settings ----------------------------------------------------------------

func TestSettingsNeverReturnThePassword(t *testing.T) {
	e := newEnv(t)

	var res settingsResponse
	e.mustCall(http.MethodPut, "/api/settings", map[string]any{
		"smtp": map[string]any{
			"host": "smtp.example.com", "port": 587, "security": "starttls",
			"username": "monitor", "from": "monitor@example.com",
			"password": "app-specific-secret",
		},
		"alerts":   map[string]any{"recipients": "ops@example.com"},
		"defaults": map[string]any{"check_interval_sec": 45},
	}, &res, http.StatusOK)

	if res.SMTP.Host != "smtp.example.com" || res.Defaults.CheckIntervalSec != 45 {
		t.Errorf("settings did not persist: %+v", res)
	}
	if !res.SMTP.HasPassword || res.SMTP.Password != redacted {
		t.Errorf("password = %q / has_password = %v, want %q and true",
			res.SMTP.Password, res.SMTP.HasPassword, redacted)
	}

	e.eng.mu.Lock()
	saved, wasSet := e.eng.password, e.eng.pwSet
	e.eng.mu.Unlock()
	if !wasSet || saved != "app-specific-secret" {
		t.Errorf("password was not handed to the sealer: %q (set=%v)", saved, wasSet)
	}

	// The stored value must be the sealed blob's slot, never the plaintext.
	stored, err := e.st.Setting(context.Background(), store.KeySMTPPasswordEnc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "app-specific-secret") {
		t.Error("plaintext password reached the settings table")
	}

	// A write that omits the password must leave it alone: this is what makes
	// "leave blank to keep" work in the form.
	e.eng.mu.Lock()
	e.eng.pwSet = false
	e.eng.mu.Unlock()
	e.mustCall(http.MethodPut, "/api/settings", map[string]any{
		"smtp": map[string]any{"host": "smtp2.example.com"},
	}, &res, http.StatusOK)

	e.eng.mu.Lock()
	touched := e.eng.pwSet
	e.eng.mu.Unlock()
	if touched {
		t.Error("a settings write without a password field rewrote the password")
	}

	// And the redaction marker coming back unchanged is not a new password.
	e.mustCall(http.MethodPut, "/api/settings", map[string]any{
		"smtp": map[string]any{"password": redacted},
	}, &res, http.StatusOK)
	e.eng.mu.Lock()
	touched = e.eng.pwSet
	e.eng.mu.Unlock()
	if touched {
		t.Error("echoing back \"***\" was treated as a new password")
	}
}

func TestSettingsValidationAndReload(t *testing.T) {
	e := newEnv(t)

	cases := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"interval too small", map[string]any{
			"defaults": map[string]any{"check_interval_sec": 1},
		}, "defaults.check_interval_sec"},
		{"timeout beyond interval", map[string]any{
			"defaults": map[string]any{"check_interval_sec": 10, "timeout_sec": 20},
		}, "defaults.timeout_sec"},
		{"bad recipients", map[string]any{
			"alerts": map[string]any{"recipients": "ops@example.com,nope"},
		}, "alerts.recipients"},
		{"bad security mode", map[string]any{
			"smtp": map[string]any{"security": "ssl-ish"},
		}, "smtp.security"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if got := e.call(http.MethodPut, "/api/settings", tc.body, &raw); got != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", got)
			}
			if field := errorOf(t, raw).Field; field != tc.field {
				t.Errorf("field = %q, want %q", field, tc.field)
			}
		})
	}

	// The global tier is the bottom of the inheritance chain, so a successful
	// write has to reload the engine.
	before := e.eng.reloadCount()
	e.mustCall(http.MethodPut, "/api/settings", map[string]any{
		"defaults": map[string]any{"check_interval_sec": 20},
	}, nil, http.StatusOK)
	if e.eng.reloadCount() == before {
		t.Error("a settings write must reload the engine")
	}
}

func TestTestEmailReturnsTheSMTPErrorVerbatim(t *testing.T) {
	e := newEnv(t)

	e.mustCall(http.MethodPut, "/api/settings", map[string]any{
		"alerts": map[string]any{"recipients": "ops@example.com"},
	}, nil, http.StatusOK)

	// Success uses the configured list.
	var ok struct {
		Sent bool     `json:"sent"`
		To   []string `json:"to"`
	}
	e.mustCall(http.MethodPost, "/api/settings/test-email", nil, &ok, http.StatusOK)
	if !ok.Sent || len(ok.To) != 1 || ok.To[0] != "ops@example.com" {
		t.Errorf("test email = %+v, want sent to the configured recipient", ok)
	}

	// Failure is the mail server's answer, not a broken service: 502 with the
	// server's words intact, because "535 Username and Password not accepted"
	// is what tells the user to make an app password.
	const smtpErr = "535 5.7.8 Username and Password not accepted"
	e.eng.mu.Lock()
	e.eng.testErr = errors.New(smtpErr)
	e.eng.mu.Unlock()

	var failed struct {
		Error errorDetail `json:"error"`
		Sent  bool        `json:"sent"`
	}
	e.mustCall(http.MethodPost, "/api/settings/test-email",
		map[string]any{"to": []string{"ops@example.com"}}, &failed, http.StatusBadGateway)
	if failed.Sent || failed.Error.Message != smtpErr {
		t.Errorf("error = %+v, want the raw SMTP text", failed.Error)
	}
}

// --- history -----------------------------------------------------------------

func TestHeartbeatSeriesIsDecimated(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	d, err := e.st.CreateDevice(ctx, model.Device{
		Name: "switch", IPAddress: "10.5.0.1", Port: 22, Enabled: true, Notify: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// An hour of ten-second probes: 360 rows, one of them a slow outlier and
	// one a failure.
	start := time.Now().Add(-time.Hour)
	var batch []model.Heartbeat
	for i := 0; i < 360; i++ {
		hb := model.Heartbeat{
			DeviceID: d.ID, Status: model.StatusUp, LatencyMS: 4,
			CheckedAt: start.Add(time.Duration(i) * 10 * time.Second),
		}
		switch i {
		case 100:
			hb.LatencyMS = 900
		case 200:
			hb.Status = model.StatusDown
			hb.LatencyMS = 3000
			hb.ErrorMsg = "TIMEOUT: i/o timeout"
		}
		batch = append(batch, hb)
	}
	if err := e.st.InsertHeartbeats(ctx, batch); err != nil {
		t.Fatal(err)
	}

	var res struct {
		Samples []sampleDTO `json:"samples"`
	}
	path := fmt.Sprintf("/api/devices/%d/heartbeats?from=%d&to=%d&max_points=60",
		d.ID, start.Add(-time.Minute).Unix(), time.Now().Unix())
	e.mustCall(http.MethodGet, path, nil, &res, http.StatusOK)

	if len(res.Samples) == 0 || len(res.Samples) > 60 {
		t.Fatalf("samples = %d, want between 1 and the requested 60", len(res.Samples))
	}

	var checks, downs int64
	var peak int64
	for _, s := range res.Samples {
		checks += s.Checks
		downs += s.Downs
		if s.MaxLatencyMS > peak {
			peak = s.MaxLatencyMS
		}
	}
	if checks != 360 {
		t.Errorf("checks across buckets = %d, want all 360 accounted for", checks)
	}
	if downs != 1 {
		t.Errorf("downs = %d, want the single failure preserved", downs)
	}
	// The spike must survive decimation: averaging it away is exactly the bug
	// the max column exists to prevent.
	if peak != 3000 {
		t.Errorf("peak latency = %d, want 3000 kept in a bucket maximum", peak)
	}

	// Timestamps must be ordered, since the chart draws them in order.
	for i := 1; i < len(res.Samples); i++ {
		if res.Samples[i].T < res.Samples[i-1].T {
			t.Fatalf("sample %d is out of order", i)
		}
	}
}

func TestDeviceUptimeUsesRawDaysBeforeAnyRollup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	d, err := e.st.CreateDevice(ctx, model.Device{
		Name: "switch", IPAddress: "10.6.0.1", Port: 22, Enabled: true, Notify: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Nine successes and one failure today: 90%. Anchored an hour into the
	// local day rather than at "now minus ten minutes", so a run just after
	// midnight does not spill half the samples into yesterday.
	now := time.Now()
	base := store.StartOfToday().Add(time.Hour)
	var batch []model.Heartbeat
	for i := 0; i < 10; i++ {
		st := model.StatusUp
		if i == 3 {
			st = model.StatusDown
		}
		batch = append(batch, model.Heartbeat{
			DeviceID: d.ID, Status: st, LatencyMS: 5,
			CheckedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	if err := e.st.InsertHeartbeats(ctx, batch); err != nil {
		t.Fatal(err)
	}

	var res struct {
		Uptime []dayUptimeDTO `json:"uptime"`
	}
	e.mustCall(http.MethodGet, fmt.Sprintf("/api/devices/%d/uptime?days=7", d.ID),
		nil, &res, http.StatusOK)

	if len(res.Uptime) != 1 {
		t.Fatalf("days = %d, want just today", len(res.Uptime))
	}
	today := res.Uptime[0]
	if today.Day != now.Format("2006-01-02") {
		t.Errorf("day = %q, want today's local date", today.Day)
	}
	if today.ChecksTotal != 10 || today.ChecksUp != 9 {
		t.Errorf("checks = %d/%d, want 9/10", today.ChecksUp, today.ChecksTotal)
	}
	if today.UptimePct != 90 {
		t.Errorf("uptime = %v, want 90", today.UptimePct)
	}
	// The source is what stops the UI reading a raw day as a rolled-up one.
	if today.Source != "raw" {
		t.Errorf("source = %q, want raw before the janitor has run", today.Source)
	}
}

func TestGroupUptimeAggregatesAndNamesTheWorstMember(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	g, err := e.st.CreateGroup(ctx, model.Group{Name: "Warehouse", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := e.st.CreateDevice(ctx, model.Device{
		Name: "healthy", IPAddress: "10.7.0.1", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sick, err := e.st.CreateDevice(ctx, model.Device{
		Name: "sick", IPAddress: "10.7.0.2", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	base := store.StartOfToday().Add(time.Hour)
	var batch []model.Heartbeat
	for i := 0; i < 10; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		batch = append(batch, model.Heartbeat{
			DeviceID: healthy.ID, Status: model.StatusUp, LatencyMS: 4, CheckedAt: at,
		})
		batch = append(batch, model.Heartbeat{
			DeviceID: sick.ID, Status: model.StatusDown, LatencyMS: 3000, CheckedAt: at,
		})
	}
	if err := e.st.InsertHeartbeats(ctx, batch); err != nil {
		t.Fatal(err)
	}

	var res struct {
		Uptime []groupDayUptimeDTO `json:"uptime"`
	}
	e.mustCall(http.MethodGet, fmt.Sprintf("/api/groups/%d/uptime?days=2", g.ID),
		nil, &res, http.StatusOK)

	if len(res.Uptime) != 1 {
		t.Fatalf("days = %d, want just today", len(res.Uptime))
	}
	day := res.Uptime[0]
	// Aggregate availability across the site: ten of twenty checks succeeded.
	if day.UptimePct != 50 {
		t.Errorf("aggregate uptime = %v, want 50", day.UptimePct)
	}
	if day.Devices != 2 {
		t.Errorf("devices = %d, want 2", day.Devices)
	}
	// And the tooltip's answer to "which one was it".
	if day.WorstDeviceName != "sick" {
		t.Errorf("worst member = %q, want sick", day.WorstDeviceName)
	}
	if day.WorstDevicePct == nil || *day.WorstDevicePct != 0 {
		t.Errorf("worst member pct = %v, want 0", day.WorstDevicePct)
	}
}

func TestSummaryReportsOpenIncidentsAndOffenders(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	g, err := e.st.CreateGroup(ctx, model.Group{Name: "Head Office", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.st.CreateDevice(ctx, model.Device{
		Name: "core switch", IPAddress: "10.8.0.1", Port: 22,
		GroupID: &g.ID, Enabled: true, Notify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-10 * time.Minute)
	if _, err := e.st.OpenIncident(ctx, d.ID, started, started.Add(30*time.Second),
		"TIMEOUT: i/o timeout", true); err != nil {
		t.Fatal(err)
	}
	if err := e.st.InsertHeartbeats(ctx, []model.Heartbeat{
		{DeviceID: d.ID, Status: model.StatusUp, LatencyMS: 4, CheckedAt: time.Now().Add(-time.Hour)},
		{DeviceID: d.ID, Status: model.StatusDown, LatencyMS: 3000, CheckedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}

	var res struct {
		Counts struct {
			Devices int `json:"devices"`
			Groups  int `json:"groups"`
		} `json:"counts"`
		Groups         []groupDTO    `json:"groups"`
		WorstOffenders []offenderDTO `json:"worst_offenders"`
		OpenIncidents  []incidentDTO `json:"open_incidents"`
	}
	e.mustCall(http.MethodGet, "/api/summary", nil, &res, http.StatusOK)

	if res.Counts.Devices != 1 || res.Counts.Groups != 1 {
		t.Errorf("counts = %+v, want one device in one group", res.Counts)
	}
	if len(res.OpenIncidents) != 1 {
		t.Fatalf("open incidents = %d, want 1", len(res.OpenIncidents))
	}
	inc := res.OpenIncidents[0]
	if inc.DeviceName != "core switch" || !inc.Ongoing {
		t.Errorf("incident = %+v, want the named, ongoing outage", inc)
	}
	// An open incident has no stored duration, so the API reports it from the
	// clock rather than making the client work it out.
	if inc.DurationSec == nil || *inc.DurationSec < 500 {
		t.Errorf("duration = %v, want roughly ten minutes", inc.DurationSec)
	}
	if len(res.WorstOffenders) != 1 || res.WorstOffenders[0].Failures != 1 {
		t.Errorf("offenders = %+v, want the one flapping device", res.WorstOffenders)
	}
}

// --- events ------------------------------------------------------------------

func TestSSEDeliversAnEventPromptly(t *testing.T) {
	e := newEnv(t)

	req := e.request(http.MethodGet, "/api/events?types=device_status", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	res, err := e.http.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer res.Body.Close()

	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// The hub must have the subscriber registered before anything is
	// published, or a fast publish would be delivered to nobody.
	deadline := time.Now().Add(2 * time.Second)
	for e.srv.Hub().Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if e.srv.Hub().Subscribers() != 1 {
		t.Fatalf("subscribers = %d, want 1", e.srv.Hub().Subscribers())
	}

	sent := time.Now()
	e.srv.Hub().Publish(Event{Type: EventDeviceStatus, Data: DeviceStatusEvent{
		DeviceID: 7, Name: "core switch", Status: model.StatusDown,
		PreviousStatus: model.StatusUp, At: rfc3339(time.Now()),
	}})
	// A heartbeat the subscription filtered out must not arrive.
	e.srv.Hub().Publish(Event{Type: EventHeartbeat, Data: HeartbeatEvent{DeviceID: 7}})

	typ, data := readSSEEvent(t, res.Body)
	if elapsed := time.Since(sent); elapsed > time.Second {
		t.Errorf("event took %s to arrive, want under a second", elapsed)
	}
	if typ != string(EventDeviceStatus) {
		t.Fatalf("event type = %q, want device_status (the filter let a heartbeat through)", typ)
	}

	var ev DeviceStatusEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if ev.DeviceID != 7 || ev.Status != model.StatusDown || ev.PreviousStatus != model.StatusUp {
		t.Errorf("event = %+v, want the published transition", ev)
	}
}

func TestSSERejectsUnknownEventType(t *testing.T) {
	e := newEnv(t)
	var raw json.RawMessage
	if got := e.call(http.MethodGet, "/api/events?types=devices_status", nil, &raw); got != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", got)
	}
	if field := errorOf(t, raw).Field; field != "types" {
		t.Errorf("field = %q, want types", field)
	}
}

func TestSSEUnsubscribesWhenTheClientGoesAway(t *testing.T) {
	e := newEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	req := e.request(http.MethodGet, "/api/events", nil).WithContext(ctx)
	res, err := e.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for e.srv.Hub().Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if e.srv.Hub().Subscribers() != 1 {
		t.Fatalf("subscribers = %d, want 1", e.srv.Hub().Subscribers())
	}

	cancel()
	_ = res.Body.Close()

	deadline = time.Now().Add(2 * time.Second)
	for e.srv.Hub().Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := e.srv.Hub().Subscribers(); n != 0 {
		t.Errorf("subscribers = %d after the client left, want 0: streams are leaking", n)
	}
}

func TestHubDropsForASlowSubscriberRatherThanBlocking(t *testing.T) {
	hub := NewHub()
	_, unsubscribe := hub.Subscribe(EventHeartbeat)
	defer unsubscribe()

	// Publish well past the buffer without ever reading. The evaluator
	// publishes from the goroutine that owns every state transition, so this
	// must not block — a stalled browser tab cannot be allowed to stall
	// monitoring.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*3; i++ {
			hub.Publish(Event{Type: EventHeartbeat, Data: HeartbeatEvent{DeviceID: int64(i)}})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that was not reading")
	}
	if hub.Dropped() == 0 {
		t.Error("dropped count = 0, want the overflow recorded for /api/health")
	}
}

// --- routing -----------------------------------------------------------------

func TestUnknownRouteAndMethodUseTheErrorShape(t *testing.T) {
	e := newEnv(t)

	var raw json.RawMessage
	if got := e.call(http.MethodGet, "/api/nope", nil, &raw); got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
	if code := errorOf(t, raw).Code; code != CodeNotFound {
		t.Errorf("code = %q, want %q", code, CodeNotFound)
	}

	// A known path with the wrong method is a 405, not a 404: the two are
	// different problems and the UI should not have to guess which it hit.
	res, err := e.http.Client().Do(e.request(http.MethodPut, "/api/devices", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /api/devices = %d, want 405", res.StatusCode)
	}
	if allow := res.Header.Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want it to list the methods that do work", allow)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if code := errorOf(t, body).Code; code != CodeMethodNotAllowed {
		t.Errorf("code = %q, want %q — net/http's plain-text body leaked through",
			code, CodeMethodNotAllowed)
	}
}

func TestMissingTokenMakesWritesUnavailable(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SeedSettings(ctx); err != nil {
		t.Fatal(err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, &fakeEngine{}, NewHub(), Config{}, quiet) // no token

	req := localRequest(http.MethodGet, "/api/devices", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	// Not 401: the token is the service's problem, not the caller's, and
	// serving unauthenticated writes would be the wrong way to fail.
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}

	// Health still answers, so the GUI can say what is wrong.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, localRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("health status = %d, want 200 even with no token", rec.Code)
	}
}

// --- token file --------------------------------------------------------------

func TestTokenIsCreatedOnceAndRotatable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "api.token")

	first, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(first) != TokenBytes*2 {
		t.Errorf("token length = %d, want %d hex characters", len(first), TokenBytes*2)
	}

	again, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Error("a second load generated a new token; every GUI would be logged out on restart")
	}

	rotated, err := RotateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if rotated == first {
		t.Error("rotate returned the same token")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(onDisk)) != rotated {
		t.Error("the rotated token was not the one written to disk")
	}

	// An empty file is worse than a missing one: it would authenticate nothing
	// and 401 every request with no explanation.
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(recovered) == "" {
		t.Error("an empty token file was accepted")
	}
}

// readSSEEvent reads one "event:"/"data:" pair from a stream, skipping the
// comment lines used for connect and keep-alive.
func readSSEEvent(t *testing.T, body io.Reader) (eventType, data string) {
	t.Helper()

	buf := make([]byte, 1)
	var line strings.Builder
	for {
		n, err := body.Read(buf)
		if err != nil {
			t.Fatalf("read stream: %v (line so far %q)", err, line.String())
		}
		if n == 0 {
			continue
		}
		if buf[0] != '\n' {
			line.WriteByte(buf[0])
			continue
		}

		text := line.String()
		line.Reset()
		switch {
		case strings.HasPrefix(text, "event: "):
			eventType = strings.TrimPrefix(text, "event: ")
		case strings.HasPrefix(text, "data: "):
			data = strings.TrimPrefix(text, "data: ")
			if eventType != "" {
				return eventType, data
			}
		}
	}
}
