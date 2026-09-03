package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"
)

// The three hardening layers, in the order a request meets them.
//
// "It binds to 127.0.0.1" is not a security model: every process of every user
// on the machine can reach a loopback port, and a web page the user visits can
// point a DNS name at 127.0.0.1 and have the browser send authenticated
// requests to it. So:
//
//  1. RemoteAddr must be loopback — a backstop in case the bind ever widens.
//  2. Host must be a loopback name and Origin, when present, must be one of
//     ours. This is what stops DNS rebinding: the attacker's page can reach
//     the port but arrives carrying their hostname.
//  3. A bearer token from a file only local administrators and the desktop
//     user can read.

// allowedOrigins are the shells that legitimately drive this API: a browser
// pointed at the dev server or the built UI, and the Tauri webview.
var allowedOriginHosts = map[string]bool{
	"127.0.0.1": true,
	"localhost": true,
	"[::1]":     true,
	"::1":       true,
}

// chain applies the middleware stack to a handler.
func (s *Server) chain(h http.Handler) http.Handler {
	return s.recoverPanic(s.logRequests(s.checkRemoteAddr(s.checkOriginAndHost(s.authenticate(h)))))
}

// recoverPanic keeps one bad request from taking the service down with it. The
// monitor staying up matters more than any single response.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic serving request",
					"method", r.Method, "path", r.URL.Path,
					"panic", v, "stack", string(debug.Stack()))
				writeErr(w, http.StatusInternalServerError, CodeInternal,
					"the service hit an internal error; see the log for detail", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// logRequests records one line per request at debug level. The API is chatty
// by design — SSE plus polling — so this is not on at info.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Debug("api request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"dur", time.Since(start).Round(time.Millisecond))
	})
}

// statusRecorder remembers the status code for the log line.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.written {
		r.status, r.written = status, true
	}
	r.ResponseWriter.WriteHeader(status)
}

// Flush forwards to the wrapped writer so SSE keeps working through the
// middleware stack.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) checkRemoteAddr(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			s.log.Warn("rejected non-loopback request", "remote", r.RemoteAddr, "path", r.URL.Path)
			writeErr(w, http.StatusForbidden, CodeForbidden,
				"this API only serves the local machine", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) checkOriginAndHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Host is pinned by name, not by name and port: the port is whatever
		// the service was configured with (and, in tests, whatever the OS
		// handed out). The rebinding attack turns on the hostname, which is
		// the part checked here.
		if !hostAllowed(r.Host) {
			s.log.Warn("rejected request with unexpected Host", "host", r.Host, "path", r.URL.Path)
			writeErr(w, http.StatusForbidden, CodeForbidden,
				"unexpected Host header; this API is only reachable as 127.0.0.1", "")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin) {
			s.log.Warn("rejected request with unexpected Origin", "origin", origin, "path", r.URL.Path)
			writeErr(w, http.StatusForbidden, CodeForbidden,
				"unexpected Origin header", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostAllowed(host string) bool {
	if host == "" {
		// HTTP/1.1 requires a Host header; a request without one is not from
		// anything we serve.
		return false
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	return allowedOriginHosts[strings.ToLower(strings.Trim(name, "[]"))]
}

func originAllowed(origin string) bool {
	// The Tauri webview sends this rather than an http origin.
	if origin == "tauri://localhost" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "http", "https", "tauri":
	default:
		return false
	}
	return allowedOriginHosts[strings.ToLower(strings.Trim(u.Hostname(), "[]"))]
}

// authenticate requires a bearer token, except on /api/health.
//
// Health is deliberately open so the GUI can tell "the service is not running"
// from "your token is stale" — two very different things to put in front of a
// user, and it reveals only counts.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		if s.token == "" {
			// Refusing everything is the only safe reading of a missing token:
			// serving unauthenticated writes would be worse than a hard fail.
			writeErr(w, http.StatusServiceUnavailable, CodeUnavailable,
				"the service has no API token; restart it or run monitor-service rotate-token", "")
			return
		}

		presented, ok := bearer(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, CodeUnauthorized,
				"an Authorization: Bearer <token> header is required", "")
			return
		}
		if subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) != 1 {
			s.log.Warn("rejected request with a bad token", "path", r.URL.Path)
			writeErr(w, http.StatusUnauthorized, CodeUnauthorized,
				"that token is not valid for this service", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearer extracts the token from the Authorization header.
//
// Header only, never a query parameter: a token in a URL ends up in logs and
// in history. That does mean the browser EventSource API cannot be used for
// /api/events — it cannot set headers — so the UI consumes the stream with
// fetch(), which can.
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}
