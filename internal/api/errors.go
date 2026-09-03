package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gkgraphite/device-status-monitor/internal/store"
)

// Error codes. They are part of the API contract: the UI switches on the code,
// never on the message text.
const (
	CodeUnauthorized     = "unauthorized"
	CodeForbidden        = "forbidden"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeConflict         = "conflict"
	CodeValidation       = "validation"
	CodeBadRequest       = "bad_request"
	CodeTooLarge         = "too_large"
	CodeUnavailable      = "unavailable"
	CodeInternal         = "internal"
)

// errorBody is the one error shape every endpoint returns:
//
//	{"error": {"code": "validation", "message": "...", "field": "port"}}
//
// Field is set whenever the problem can be pointed at one input, so the UI can
// mark the offending control instead of showing a toast.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// writeJSON encodes v with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already out; all that is left is to say so in the
		// log rather than corrupt the response further.
		slog.Default().Debug("write response", "err", err)
	}
}

// writeErr sends one error. field may be empty.
func writeErr(w http.ResponseWriter, status int, code, message, field string) {
	writeJSON(w, status, errorBody{errorDetail{Code: code, Message: message, Field: field}})
}

// notFound, badRequest and friends keep handlers to one line per failure.
func notFound(w http.ResponseWriter, what string) {
	writeErr(w, http.StatusNotFound, CodeNotFound, what+" not found", "")
}

func badRequest(w http.ResponseWriter, message string) {
	writeErr(w, http.StatusBadRequest, CodeBadRequest, message, "")
}

func invalid(w http.ResponseWriter, message, field string) {
	writeErr(w, http.StatusUnprocessableEntity, CodeValidation, message, field)
}

// problemWriter rewrites net/http's own plain-text 404 and 405 bodies into the
// API's error envelope, and leaves anything a handler wrote alone.
//
// This exists so the mux can be left without a catch-all route. Registering
// one would give a JSON 404 at the cost of the method-based patterns' 405 and
// its Allow header — "PUT /api/devices" would come back as "no such endpoint",
// which is not what happened.
type problemWriter struct {
	http.ResponseWriter
	req     *http.Request
	swallow bool
	done    bool
}

func (w *problemWriter) WriteHeader(status int) {
	if w.done {
		return
	}
	w.done = true

	// Only net/http's Error sets this content type; every handler here writes
	// application/json before its status.
	if (status != http.StatusNotFound && status != http.StatusMethodNotAllowed) ||
		w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		w.ResponseWriter.WriteHeader(status)
		return
	}

	code := CodeNotFound
	message := "no endpoint " + w.req.Method + " " + w.req.URL.Path
	if status == http.StatusMethodNotAllowed {
		code = CodeMethodNotAllowed
		message = w.req.Method + " is not allowed on " + w.req.URL.Path
		if allow := w.Header().Get("Allow"); allow != "" {
			message += "; try " + allow
		}
	}

	w.swallow = true
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
	_ = json.NewEncoder(w.ResponseWriter).Encode(
		errorBody{errorDetail{Code: code, Message: message}})
}

func (w *problemWriter) Write(b []byte) (int, error) {
	if w.swallow {
		// The plain-text body net/http was about to write; the envelope has
		// already gone out in its place.
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards, so /api/events still streams through this wrapper.
func (w *problemWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// jsonProblems wraps a handler so its built-in error responses come back in the
// same shape as every other error.
func jsonProblems(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&problemWriter{ResponseWriter: w, req: r}, r)
	})
}

// storeErr maps a store error onto a status. Anything unrecognised is a 500
// with the detail in the log, never in the response: SQL text and file paths
// are not the caller's business.
//
// The three constraint classes are what make this worth having — a duplicate
// device is the user's mistake (409) and reporting it as a 500 would send them
// looking for a broken service.
func (s *Server) storeErr(w http.ResponseWriter, r *http.Request, err error, what, dupField string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, what)
	case errors.Is(err, store.ErrDuplicate):
		writeErr(w, http.StatusConflict, CodeConflict,
			"another "+what+" already uses that value", dupField)
	case errors.Is(err, store.ErrConstraint):
		invalid(w, "the database rejected that value: "+err.Error(), dupField)
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeErr(w, http.StatusInternalServerError, CodeInternal,
			"the service could not complete the request; see the log for detail", "")
	}
}
