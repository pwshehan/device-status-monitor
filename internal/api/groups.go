package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pwshehan/device-status-monitor/internal/model"
	"github.com/pwshehan/device-status-monitor/internal/store"
)

// groupBody is the create/update body. As with devices, every field is an Opt
// so a PATCH can clear a group-level override back to the global default.
type groupBody struct {
	Name              Opt[string] `json:"name"`
	Description       Opt[string] `json:"description"`
	Color             Opt[string] `json:"color"`
	SortOrder         Opt[int]    `json:"sort_order"`
	CheckIntervalSec  Opt[int]    `json:"check_interval_sec"`
	TimeoutSec        Opt[int]    `json:"timeout_sec"`
	FailureThreshold  Opt[int]    `json:"failure_threshold"`
	RecoveryThreshold Opt[int]    `json:"recovery_threshold"`
	Notify            Opt[bool]   `json:"notify"`
	Recipients        Opt[string] `json:"recipients"`
}

func (b groupBody) apply(g *model.Group) {
	applyVal(&g.Name, b.Name)
	applyVal(&g.Description, b.Description)
	applyVal(&g.Color, b.Color)
	applyVal(&g.SortOrder, b.SortOrder)
	applyPtr(&g.CheckIntervalSec, b.CheckIntervalSec)
	applyPtr(&g.TimeoutSec, b.TimeoutSec)
	applyPtr(&g.FailureThreshold, b.FailureThreshold)
	applyPtr(&g.RecoveryThreshold, b.RecoveryThreshold)
	applyVal(&g.Notify, b.Notify)
	applyPtr(&g.Recipients, b.Recipients)

	g.Name = strings.TrimSpace(g.Name)
	if g.Recipients != nil {
		trimmed := strings.TrimSpace(*g.Recipients)
		if trimmed == "" {
			// An empty recipients field is not an override of "nobody" — it
			// means fall back to the global list. Storing "" would silence the
			// group's alerts, which is what notify=false is for.
			g.Recipients = nil
		} else {
			g.Recipients = &trimmed
		}
	}
}

func validateGroup(g model.Group) error {
	if err := validateName("name", g.Name); err != nil {
		return err
	}
	if err := validateOverrides(g.CheckIntervalSec, g.TimeoutSec,
		g.FailureThreshold, g.RecoveryThreshold); err != nil {
		return err
	}
	if g.Recipients != nil {
		if err := validateRecipients("recipients", *g.Recipients); err != nil {
			return err
		}
	}
	if g.Color != "" && !colorPattern.MatchString(g.Color) {
		return fieldErr("color", "colour must be a hex value like #0d6a73")
	}
	return nil
}

// handleListGroups serves GET /api/groups with the tallies the grouped
// dashboard needs, plus the Ungrouped bucket when it is not empty.
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	stats, err := s.st.GroupStats(r.Context())
	if err != nil {
		s.storeErr(w, r, err, "group", "")
		return
	}
	out := make([]groupDTO, 0, len(stats))
	for _, st := range stats {
		out = append(out, newGroupStatDTO(st))
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	st, err := s.st.GroupStatFor(r.Context(), id)
	if err != nil {
		s.storeErr(w, r, err, "group", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": newGroupStatDTO(st)})
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var body groupBody
	if err := decode(w, r, &body); err != nil {
		return
	}
	g := model.Group{Notify: true}
	body.apply(&g)

	if s.reject(w, validateGroup(g)) {
		return
	}

	// A new group goes last unless it says otherwise, so creating one never
	// silently reorders the dashboard.
	if !body.SortOrder.Set {
		existing, err := s.st.ListGroups(r.Context())
		if err != nil {
			s.storeErr(w, r, err, "group", "")
			return
		}
		g.SortOrder = len(existing)
	}

	created, err := s.st.CreateGroup(r.Context(), g)
	if err != nil {
		s.storeErr(w, r, err, "group", "name")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"group": newGroupDTO(created)})
}

// handlePatchGroup updates a group. Every member's effective values may change,
// so this reloads the engine exactly like a device edit does — that is the
// point of resolving inheritance in one place.
func (s *Server) handlePatchGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	current, err := s.st.GetGroup(r.Context(), id)
	if err != nil {
		s.storeErr(w, r, err, "group", "")
		return
	}

	var body groupBody
	if err := decode(w, r, &body); err != nil {
		return
	}
	body.apply(&current)

	if s.reject(w, validateGroup(current)) {
		return
	}

	updated, err := s.st.UpdateGroup(r.Context(), current)
	if err != nil {
		s.storeErr(w, r, err, "group", "name")
		return
	}
	s.eng.Reload()
	writeJSON(w, http.StatusOK, map[string]any{"group": newGroupDTO(updated)})
}

// handleDeleteGroup deletes a group and says how many devices it let go of.
//
// The devices survive: the foreign key is ON DELETE SET NULL, never CASCADE.
// Deleting a label must not delete the things it labelled, and the count in
// the response is what lets the UI confirm that out loud.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	orphaned, err := s.st.DeleteGroup(r.Context(), id)
	if err != nil {
		s.storeErr(w, r, err, "group", "")
		return
	}
	// Members inherit from the global defaults now, so their intervals may
	// have changed.
	s.eng.Reload()
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":           id,
		"devices_ungrouped": orphaned,
	})
}

type reorderBody struct {
	IDs []int64 `json:"ids"`
}

func (s *Server) handleReorderGroups(w http.ResponseWriter, r *http.Request) {
	var body reorderBody
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
	if err := s.st.ReorderGroups(r.Context(), body.IDs); err != nil {
		s.storeErr(w, r, err, "group", "ids")
		return
	}
	// Order is presentation only: no reload, nothing about probing changed.
	writeJSON(w, http.StatusOK, map[string]any{"reordered": len(body.IDs)})
}

// handlePauseGroup opens or closes a maintenance window for a whole site.
//
// It writes only the group row. A device someone paused individually before
// the window opened must still be paused when it closes, which is why the
// effective pause is a max and not an assignment (model.Resolve).
func (s *Server) handlePauseGroup(w http.ResponseWriter, r *http.Request) {
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

	if err := s.st.PauseGroup(r.Context(), id, until); err != nil {
		s.storeErr(w, r, err, "group", "")
		return
	}
	s.eng.Reload()
	writeJSON(w, http.StatusOK, map[string]any{
		"group_id":     id,
		"paused_until": rfc3339Ptr(until),
	})
}

func (s *Server) handleGroupUptime(w http.ResponseWriter, r *http.Request) {
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

	rows, err := s.st.GroupUptime(r.Context(), id, days)
	if err != nil {
		s.storeErr(w, r, err, "group", "")
		return
	}
	out := make([]groupDayUptimeDTO, 0, len(rows))
	for _, d := range rows {
		out = append(out, groupDayUptimeDTO(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group_id": id, "days": days, "uptime": out,
	})
}

// reject writes a 422 naming the offending field and reports whether it wrote
// anything, so a handler can bail in one line.
//
// Naming the field is the whole point: "port must be between 1 and 65535" with
// field "port" lets the form mark the control, where a bare message can only
// become a toast.
func (s *Server) reject(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var fe fieldError
	if errors.As(err, &fe) {
		invalid(w, fe.Message, fe.Field)
	} else {
		invalid(w, err.Error(), "")
	}
	return true
}
