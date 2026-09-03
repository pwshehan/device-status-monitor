package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// keepAlive is how often a comment line is sent on an idle stream. Something
// has to move periodically or an idle connection is indistinguishable from a
// dead one to every proxy and NAT between the two ends — and on a quiet
// network this stream can be silent for hours.
const keepAlive = 20 * time.Second

// handleEvents streams status changes as Server-Sent Events.
//
// SSE rather than polling: a status badge that lags two seconds behind reality
// is the difference between a monitor and a report, and one long-lived
// connection is cheaper than every open window polling six endpoints.
//
// The stream requires the same bearer token as everything else, which means it
// cannot be consumed with the browser's EventSource — that API cannot set
// headers. The UI reads it with fetch() and a stream reader instead. The
// alternative, a token in the query string, would put the credential in every
// log and history entry that touches the URL.
//
// Subscribe to a subset with ?types=device_status,incident. The default is
// everything, including the heartbeat feed, which at 200 devices is about
// seven events a second — a dashboard wants it, a settings window does not.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, CodeInternal,
			"this connection cannot stream", "")
		return
	}

	types, err := parseEventTypes(r.URL.Query().Get("types"))
	if s.reject(w, err) {
		return
	}

	events, unsubscribe := s.hub.Subscribe(types...)
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Nothing in this stack buffers, but a future reverse proxy would, and a
	// buffered SSE stream is a broken SSE stream.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// An immediate comment tells the client the stream is live before the
	// first event, which on a healthy network may be a minute away.
	fmt.Fprintf(w, ": connected %s\n\n", rfc3339(time.Now()))
	flusher.Flush()

	ticker := time.NewTicker(keepAlive)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			// The client went away, or the service is shutting down: the
			// server's base context is the engine's, so a stop closes streams
			// rather than making Shutdown wait out its whole timeout on them.
			return

		case ev, open := <-events:
			if !open {
				return
			}
			payload, err := json.Marshal(ev.Data)
			if err != nil {
				s.log.Error("encode event", "type", ev.Type, "err", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, payload); err != nil {
				return
			}
			flusher.Flush()

		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// parseEventTypes reads the ?types= filter. An unknown name is an error rather
// than an ignored one: a client asking for "devices_status" would otherwise
// silently receive nothing and look like a broken stream.
func parseEventTypes(raw string) ([]EventType, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []EventType
	for _, part := range strings.Split(raw, ",") {
		name := EventType(strings.ToLower(strings.TrimSpace(part)))
		if name == "" {
			continue
		}
		valid := false
		for _, known := range AllEventTypes {
			if name == known {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fieldErr("types", "unknown event type %q", name)
		}
		out = append(out, name)
	}
	return out, nil
}
