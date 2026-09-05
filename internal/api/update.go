package api

import (
	"errors"
	"net/http"

	"github.com/pwshehan/device-status-monitor/internal/update"
)

// updateResponse is what the Service page renders.
//
// Authenticated, unlike /api/health: what version a machine is on and whether
// it is behind is a fact about how well maintained it is, and the health
// endpoint is deliberately open so a GUI with a stale token can still tell
// "service down" from "token wrong". That reason does not extend to this.
type updateResponse struct {
	// Supported is false where an update could not be applied even if one
	// existed. The page hides the whole section rather than offering a button
	// that would fail.
	Supported bool `json:"supported"`

	Enabled      bool `json:"enabled"`
	AutoDownload bool `json:"auto_download"`

	Current string `json:"current"`

	// State is idle, available, downloading, ready, installing or error.
	State string `json:"state"`

	Latest      string  `json:"latest,omitempty"`
	NotesURL    string  `json:"notes_url,omitempty"`
	PublishedAt *string `json:"published_at"`

	// SizeBytes and DownloadedBytes are only meaningful while downloading.
	SizeBytes       int64 `json:"size_bytes"`
	DownloadedBytes int64 `json:"downloaded_bytes"`

	LastCheckedAt *string `json:"last_checked_at"`
	Error         string  `json:"error,omitempty"`
}

func newUpdateDTO(s update.Status) updateResponse {
	return updateResponse{
		Supported:       s.Supported,
		Enabled:         s.Enabled,
		AutoDownload:    s.AutoDownload,
		Current:         s.Current,
		State:           string(s.State),
		Latest:          s.Latest,
		NotesURL:        s.NotesURL,
		PublishedAt:     rfc3339Zero(s.PublishedAt),
		SizeBytes:       s.SizeBytes,
		DownloadedBytes: s.DownloadedBytes,
		LastCheckedAt:   rfc3339Zero(s.LastCheckedAt),
		Error:           s.Err,
	}
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, newUpdateDTO(s.eng.UpdateStatus()))
}

// handleUpdateCheck asks GitHub now rather than waiting for the next pass.
//
// Synchronous: someone who pressed a button labelled "check now" is looking at
// the page waiting for the answer, and the request carries their timeout.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, newUpdateDTO(s.eng.CheckUpdate(r.Context())))
}

// handleUpdateDownload stages the installer. Only needed when automatic
// downloads are off; otherwise the check has already done it.
func (s *Server) handleUpdateDownload(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DownloadUpdate(r.Context()); err != nil {
		// A download failure is not a server fault and not a bad request: the
		// release, the network or the checksum did not cooperate. The state
		// carries the detail, so the body is the status either way.
		writeJSON(w, http.StatusConflict, newUpdateDTO(s.eng.UpdateStatus()))
		return
	}
	writeJSON(w, http.StatusOK, newUpdateDTO(s.eng.UpdateStatus()))
}

// handleUpdateInstall runs the staged installer.
//
// 202 rather than 200, and the answer goes out before anything starts: the
// installer's first act is to stop this service, so a response written after
// that point would never arrive. What the caller is being told is "this has
// been accepted and the service is about to go away", which is exactly what
// they will observe.
func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	err := s.eng.InstallUpdate(r.Context())
	switch {
	case errors.Is(err, update.ErrNotReady):
		writeErr(w, http.StatusConflict, "not_ready",
			"there is no checked installer ready to run", "")
		return
	case err != nil:
		writeErr(w, http.StatusConflict, "install_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusAccepted, newUpdateDTO(s.eng.UpdateStatus()))
}
