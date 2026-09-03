package api

import (
	"net/http"
	"time"
)

// healthResponse is what the GUI polls to tell three states apart: the service
// is not running (the request fails outright), the service is running but the
// database is unusable (db_ok false), and everything is fine.
//
// This endpoint is unauthenticated on purpose. If it needed the token, a GUI
// with a stale token could not distinguish "service down" from "token wrong",
// and those two produce completely different advice for the user. It reveals
// counts and a version, nothing about what is monitored.
type healthResponse struct {
	OK             bool   `json:"ok"`
	Version        string `json:"version"`
	UptimeSec      int64  `json:"uptime_sec"`
	DBOK           bool   `json:"db_ok"`
	DBError        string `json:"db_error,omitempty"`
	DBSizeBytes    int64  `json:"db_size_bytes"`
	SchemaVersion  int    `json:"schema_version"`
	Devices        int    `json:"devices"`
	Up             int    `json:"up"`
	Down           int    `json:"down"`
	Unknown        int    `json:"unknown"`
	Paused         int    `json:"paused"`
	Monitored      int    `json:"monitored"`
	OpenIncidents  int64  `json:"open_incidents"`
	PendingAlerts  int64  `json:"pending_alerts"`
	SchedulerLagMS int64  `json:"scheduler_lag_ms"`
	EventClients   int    `json:"event_clients"`
	EventsDropped  int64  `json:"events_dropped"`
	DataDir        string `json:"data_dir"`
	LogDir         string `json:"log_dir"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res := healthResponse{
		OK:             true,
		Version:        s.cfg.Version,
		UptimeSec:      int64(s.eng.Uptime().Seconds()),
		Monitored:      s.eng.Running(),
		SchedulerLagMS: s.eng.SchedulerLagMS(),
		EventClients:   s.hub.Subscribers(),
		EventsDropped:  s.hub.Dropped(),
		DataDir:        s.cfg.DataDir,
		LogDir:         s.cfg.LogDir,
	}

	// A database that has gone away must not make health itself fail: a 503
	// here looks identical to a stopped service, which is the one distinction
	// this endpoint exists to draw.
	if err := s.st.Ping(ctx); err != nil {
		res.OK = false
		res.DBError = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	res.DBOK = true

	if v, err := s.st.SchemaVersion(ctx); err == nil {
		res.SchemaVersion = v
	}
	if size, err := s.st.DBSizeBytes(ctx); err == nil {
		res.DBSizeBytes = size
	}
	if c, err := s.st.Counts(ctx); err == nil {
		res.Devices, res.Up, res.Down = c.Devices, c.Up, c.Down
		res.Unknown, res.Paused = c.Unknown, c.Paused
	} else {
		res.OK = false
		res.DBError = err.Error()
	}
	if n, err := s.st.CountOpenIncidents(ctx); err == nil {
		res.OpenIncidents = n
	}
	if n, err := s.st.PendingAlerts(ctx); err == nil {
		res.PendingAlerts = n
	}

	writeJSON(w, http.StatusOK, res)
}

// handleSummary serves the dashboard in one request: tiles, per-group
// breakdown, worst offenders and the open incident list.
//
// One endpoint rather than four because the dashboard renders all of it at
// once, and four round trips would let the tiles disagree with the sections
// they sit above.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	counts, err := s.st.Counts(ctx)
	if err != nil {
		s.storeErr(w, r, err, "summary", "")
		return
	}
	stats, err := s.st.GroupStats(ctx)
	if err != nil {
		s.storeErr(w, r, err, "summary", "")
		return
	}
	offenders, err := s.st.WorstOffenders(ctx, 5)
	if err != nil {
		s.storeErr(w, r, err, "summary", "")
		return
	}
	open, err := s.st.OpenIncidents(ctx, 50)
	if err != nil {
		s.storeErr(w, r, err, "summary", "")
		return
	}

	groups := make([]groupDTO, 0, len(stats))
	for _, st := range stats {
		groups = append(groups, newGroupStatDTO(st))
	}
	worst := make([]offenderDTO, 0, len(offenders))
	for _, o := range offenders {
		worst = append(worst, offenderDTO(o))
	}
	incidents := make([]incidentDTO, 0, len(open))
	for _, oi := range open {
		incidents = append(incidents, newOpenIncidentDTO(oi))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at": rfc3339(time.Now()),
		"counts": map[string]any{
			"devices":  counts.Devices,
			"up":       counts.Up,
			"down":     counts.Down,
			"unknown":  counts.Unknown,
			"paused":   counts.Paused,
			"disabled": counts.Disabled,
			"groups":   counts.Groups,
		},
		"monitored":       s.eng.Running(),
		"groups":          groups,
		"worst_offenders": worst,
		"open_incidents":  incidents,
	})
}
