package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gkgraphite/device-status-monitor/internal/model"
	"github.com/gkgraphite/device-status-monitor/internal/store"
)

// deviceBody is the create/update body. Every field is an Opt so a PATCH can
// tell "leave it alone" from "clear the override".
type deviceBody struct {
	GroupID           Opt[int64]    `json:"group_id"`
	Name              Opt[string]   `json:"name"`
	IPAddress         Opt[string]   `json:"ip_address"`
	Port              Opt[int]      `json:"port"`
	CheckIntervalSec  Opt[int]      `json:"check_interval_sec"`
	TimeoutSec        Opt[int]      `json:"timeout_sec"`
	FailureThreshold  Opt[int]      `json:"failure_threshold"`
	RecoveryThreshold Opt[int]      `json:"recovery_threshold"`
	Enabled           Opt[bool]     `json:"enabled"`
	Notify            Opt[bool]     `json:"notify"`
	Tags              Opt[[]string] `json:"tags"`
}

// apply folds the body onto a device.
func (b deviceBody) apply(d *model.Device) {
	applyPtr(&d.GroupID, b.GroupID)
	applyVal(&d.Name, b.Name)
	applyVal(&d.IPAddress, b.IPAddress)
	applyVal(&d.Port, b.Port)
	applyPtr(&d.CheckIntervalSec, b.CheckIntervalSec)
	applyPtr(&d.TimeoutSec, b.TimeoutSec)
	applyPtr(&d.FailureThreshold, b.FailureThreshold)
	applyPtr(&d.RecoveryThreshold, b.RecoveryThreshold)
	applyVal(&d.Enabled, b.Enabled)
	applyVal(&d.Notify, b.Notify)
	if b.Tags.Set {
		d.Tags = joinTags(b.Tags.Value)
	}
	d.Name = strings.TrimSpace(d.Name)
	d.IPAddress = strings.TrimSpace(d.IPAddress)
}

func validateDevice(d model.Device) error {
	if err := validateName("name", d.Name); err != nil {
		return err
	}
	if err := validateHost(d.IPAddress); err != nil {
		return err
	}
	if err := validatePort(d.Port); err != nil {
		return err
	}
	if err := validateOverrides(d.CheckIntervalSec, d.TimeoutSec,
		d.FailureThreshold, d.RecoveryThreshold); err != nil {
		return err
	}
	return validateTags(splitTags(d.Tags))
}

// handleListDevices serves GET /api/devices with optional group_id, tag,
// status and enabled filters.
func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.DeviceFilter{}

	if raw := strings.TrimSpace(q.Get("group_id")); raw != "" {
		// "none" is how the UI asks for the Ungrouped section, since an empty
		// group_id has to keep meaning "no filter".
		if raw == "none" || raw == "null" {
			f.Ungrouped = true
		} else {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				invalid(w, "group_id must be a number or \"none\"", "group_id")
				return
			}
			f.GroupID = &id
		}
	}
	if raw := strings.TrimSpace(q.Get("status")); raw != "" {
		st := model.Status(strings.ToUpper(raw))
		switch st {
		case model.StatusUp, model.StatusDown, model.StatusUnknown:
			f.Status = st
		default:
			invalid(w, "status must be UP, DOWN or UNKNOWN", "status")
			return
		}
	}
	f.Tag = strings.TrimSpace(q.Get("tag"))
	f.EnabledOnly = q.Get("enabled") == "true"

	devices, err := s.st.ListDevices(r.Context(), f)
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}
	res, err := s.st.NewResolver(r.Context())
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}

	// One query for the whole page rather than one per row: the strip is
	// refetched every time any device changes state.
	ids := make([]int64, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	recent, err := s.st.RecentChecks(r.Context(), ids, RecentCheckCount)
	if err != nil {
		// A missing strip is a cosmetic loss; the list itself is the answer.
		s.log.Warn("read recent checks", "err", err)
		recent = nil
	}

	out := make([]deviceDTO, 0, len(devices))
	for _, d := range devices {
		dto := newDeviceDTO(d, res.Resolve(d))
		dto.RecentChecks = encodeChecks(recent[d.ID])
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// RecentCheckCount is how many outcomes a row strip shows. Forty is about a
// screen's worth at the size a table row allows, and twenty minutes of history
// on the default interval.
const RecentCheckCount = 40

func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	d, err := s.st.GetDevice(r.Context(), id)
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}
	res, err := s.st.NewResolver(r.Context())
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}

	dto := newDeviceDTO(d, res.Resolve(d))
	if recent, err := s.st.RecentChecks(r.Context(), []int64{id}, RecentCheckCount); err == nil {
		dto.RecentChecks = encodeChecks(recent[id])
	}

	body := map[string]any{"device": dto}

	// The open incident comes with the device: the detail page always needs
	// it, and a second round trip to find out "no outage" is waste.
	inc, err := s.st.OpenIncidentFor(r.Context(), id)
	switch {
	case err == nil:
		dto := newIncidentDTO(inc)
		secs := int64(time.Since(inc.StartedAt).Seconds())
		dto.DurationSec = &secs
		body["open_incident"] = dto
	case errors.Is(err, store.ErrNotFound):
		body["open_incident"] = nil
	default:
		s.storeErr(w, r, err, "device", "")
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var body deviceBody
	if err := decode(w, r, &body); err != nil {
		return
	}

	// A new device is enabled and notifying unless it says otherwise: adding a
	// device you have to then switch on is a papercut.
	d := model.Device{Enabled: true, Notify: true}
	body.apply(&d)

	if s.reject(w, validateDevice(d)) {
		return
	}

	created, err := s.st.CreateDevice(r.Context(), d)
	if err != nil {
		s.storeErr(w, r, err, "device", "ip_address")
		return
	}
	s.eng.Reload()

	res, err := s.st.NewResolver(r.Context())
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"device": newDeviceDTO(created, res.Resolve(created)),
	})
}

func (s *Server) handlePatchDevice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	current, err := s.st.GetDevice(r.Context(), id)
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}

	var body deviceBody
	if err := decode(w, r, &body); err != nil {
		return
	}
	body.apply(&current)

	if s.reject(w, validateDevice(current)) {
		return
	}

	updated, err := s.st.UpdateDevice(r.Context(), current)
	if err != nil {
		s.storeErr(w, r, err, "device", "ip_address")
		return
	}
	// Any of address, interval or timeout changing needs the ticker rebuilt,
	// and the scheduler works out which by diffing — so one Reload covers
	// every field without the handler having to know which matter.
	s.eng.Reload()

	res, err := s.st.NewResolver(r.Context())
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device": newDeviceDTO(updated, res.Resolve(updated)),
	})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := s.st.DeleteDevice(r.Context(), id); err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}
	s.eng.Reload()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func (s *Server) handlePauseDevice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	var body pauseRequest
	if r.ContentLength != 0 {
		if err := decode(w, r, &body); err != nil {
			return
		}
	}
	until, err := body.resolve(time.Now())
	if s.reject(w, err) {
		return
	}

	if err := s.st.PauseDevice(r.Context(), id, until); err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}
	s.eng.Reload()
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":    id,
		"paused_until": rfc3339Ptr(until),
	})
}

func (s *Server) handleCheckDevice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if _, err := s.st.GetDevice(r.Context(), id); err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}

	// The engine only holds enabled, unpaused devices. Saying so plainly beats
	// a check that silently reports nothing.
	res, ok := s.eng.CheckNow(r.Context(), id)
	if !ok {
		writeErr(w, http.StatusConflict, CodeConflict,
			"this device is not being monitored right now — it is disabled or paused", "")
		return
	}

	status := model.StatusDown
	if res.OK {
		status = model.StatusUp
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":  id,
		"ok":         res.OK,
		"status":     status,
		"latency_ms": res.LatencyMS,
		"class":      string(res.Class),
		"error":      res.ErrMsg(),
		"checked_at": rfc3339(res.At),
	})
}

// handleDeviceHeartbeats serves the latency series, decimated server-side.
//
// Decimation happens in SQL, not by fetching everything and thinning it in Go:
// a 90-day window at a 10 s interval is 777 000 rows, and the client asked for
// a thousand points.
func (s *Server) handleDeviceHeartbeats(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	now := time.Now()
	from, err := queryTime(r, "from", now.Add(-24*time.Hour))
	if err != nil {
		invalid(w, err.Error(), "from")
		return
	}
	to, err := queryTime(r, "to", now)
	if err != nil {
		invalid(w, err.Error(), "to")
		return
	}
	if !to.After(from) {
		invalid(w, "to must be after from", "to")
		return
	}
	maxPoints, err := queryInt(r, "max_points", store.DefaultMaxPoints)
	if err != nil {
		invalid(w, err.Error(), "max_points")
		return
	}
	if maxPoints < 1 {
		invalid(w, "max_points must be at least 1", "max_points")
		return
	}

	samples, err := s.st.HeartbeatSeries(r.Context(), id, from, to, maxPoints)
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}
	out := make([]sampleDTO, 0, len(samples))
	for _, sm := range samples {
		out = append(out, sampleDTO{
			T:            sm.At.Unix(),
			AvgLatencyMS: sm.AvgLatencyMS,
			MaxLatencyMS: sm.MaxLatencyMS,
			Checks:       sm.Checks,
			Downs:        sm.Downs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":  id,
		"from":       from.Unix(),
		"to":         to.Unix(),
		"max_points": maxPoints,
		"samples":    out,
	})
}

func (s *Server) handleDeviceUptime(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	days, err := queryInt(r, "days", 90)
	if err != nil {
		invalid(w, err.Error(), "days")
		return
	}
	if days < 1 || days > store.MaxUptimeDays {
		invalid(w, "days must be between 1 and "+strconv.Itoa(store.MaxUptimeDays), "days")
		return
	}
	if _, err := s.st.GetDevice(r.Context(), id); err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}

	rows, err := s.st.DeviceUptime(r.Context(), id, days)
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}
	out := make([]dayUptimeDTO, 0, len(rows))
	for _, d := range rows {
		out = append(out, dayUptimeDTO(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id": id, "days": days, "uptime": out,
	})
}

func (s *Server) handleDeviceIncidents(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		invalid(w, err.Error(), "limit")
		return
	}
	if limit < 1 || limit > 500 {
		invalid(w, "limit must be between 1 and 500", "limit")
		return
	}

	incidents, err := s.st.Incidents(r.Context(), id, limit)
	if err != nil {
		s.storeErr(w, r, err, "device", "")
		return
	}
	out := make([]incidentDTO, 0, len(incidents))
	for _, inc := range incidents {
		out = append(out, newIncidentDTO(inc))
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "incidents": out})
}

// bulkBody is the one-transaction bulk operation. This is what makes grouping
// usable after adding forty devices.
type bulkBody struct {
	IDs     []int64     `json:"ids"`
	Op      string      `json:"op"`
	GroupID Opt[int64]  `json:"group_id"`
	Minutes Opt[int]    `json:"minutes"`
	Until   Opt[string] `json:"until"`
}

func (s *Server) handleBulkDevices(w http.ResponseWriter, r *http.Request) {
	var body bulkBody
	if err := decode(w, r, &body); err != nil {
		return
	}
	if len(body.IDs) == 0 {
		invalid(w, "ids must not be empty", "ids")
		return
	}
	if len(body.IDs) > MaxBulkIDs {
		invalid(w, "at most "+strconv.Itoa(MaxBulkIDs)+" ids per call", "ids")
		return
	}

	op := store.BulkOp(strings.ToLower(strings.TrimSpace(body.Op)))
	var groupID *int64
	var until *time.Time

	switch op {
	case store.BulkMove:
		// group_id is required, and null is a legitimate value: it means move
		// these devices out of every group.
		if !body.GroupID.Set {
			invalid(w, "move needs group_id (null to move to Ungrouped)", "group_id")
			return
		}
		if body.GroupID.Valid {
			id := body.GroupID.Value
			if _, err := s.st.GetGroup(r.Context(), id); err != nil {
				s.storeErr(w, r, err, "group", "group_id")
				return
			}
			groupID = &id
		}

	case store.BulkPause:
		t, err := pauseRequest{Minutes: body.Minutes, Until: body.Until}.resolve(time.Now())
		if s.reject(w, err) {
			return
		}
		if t == nil {
			invalid(w, "pause needs minutes or until; use op \"resume\" to clear a pause", "minutes")
			return
		}
		until = t

	case store.BulkResume, store.BulkDelete:
		// Nothing else to read.

	default:
		invalid(w, "op must be move, pause, resume or delete", "op")
		return
	}

	affected, err := s.st.Bulk(r.Context(), op, body.IDs, groupID, until)
	if err != nil {
		s.storeErr(w, r, err, "device", "group_id")
		return
	}
	// One reload for the whole batch: coalescing is why bulk exists.
	s.eng.Reload()

	writeJSON(w, http.StatusOK, map[string]any{
		"op":       string(op),
		"affected": affected,
		"ids":      body.IDs,
	})
}
