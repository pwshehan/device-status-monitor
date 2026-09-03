package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxBodyBytes caps a request body. Nothing this API accepts is large, and an
// unbounded decode on a loopback port is a free way for any local process to
// exhaust the service's memory.
const MaxBodyBytes = 64 << 10

// Opt is a JSON field that can tell "absent" from "null" from a value.
//
// This distinction is the whole inheritance story on the wire: a PATCH that
// omits check_interval_sec leaves the override alone, and one that sends null
// clears it back to inherited. A plain *int cannot express both.
type Opt[T any] struct {
	Set   bool // the key was present in the body
	Valid bool // present and not null
	Value T
}

// UnmarshalJSON records presence, then nullness, then the value.
func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Valid = false
		return nil
	}
	if err := json.Unmarshal(b, &o.Value); err != nil {
		return err
	}
	o.Valid = true
	return nil
}

// MarshalJSON is here so a struct carrying Opt fields can round-trip in tests.
func (o Opt[T]) MarshalJSON() ([]byte, error) {
	if !o.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(o.Value)
}

// applyPtr folds an Opt onto a nullable model field: absent leaves it alone,
// null clears the override, a value sets it.
func applyPtr[T any](dst **T, o Opt[T]) {
	if !o.Set {
		return
	}
	if !o.Valid {
		*dst = nil
		return
	}
	v := o.Value
	*dst = &v
}

// applyVal folds an Opt onto a non-nullable field. A null for one of these is
// meaningless, so it is ignored rather than zeroing the field.
func applyVal[T any](dst *T, o Opt[T]) {
	if o.Set && o.Valid {
		*dst = o.Value
	}
}

// decode reads a JSON body with the size cap applied and rejects unknown
// fields, so a typo in a field name is a 400 rather than a silent no-op.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			writeErr(w, http.StatusRequestEntityTooLarge, CodeTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", MaxBodyBytes), "")
		case errors.Is(err, io.EOF):
			badRequest(w, "a JSON body is required")
		default:
			badRequest(w, "malformed JSON: "+err.Error())
		}
		return err
	}
	return nil
}

// pathID parses a numeric path value.
func pathID(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%q is not a valid id", raw)
	}
	return id, nil
}

// queryInt reads an integer query parameter, falling back to def when absent.
func queryInt(r *http.Request, name string, def int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", name)
	}
	return v, nil
}

// queryTime reads a timestamp query parameter, accepting RFC3339 or unix
// seconds so a chart can pass whichever it holds.
func queryTime(r *http.Request, name string, def time.Time) (time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(secs, 0), nil
	}
	return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp or unix seconds", name)
}

// rfc3339 renders a timestamp for JSON. UTC on the wire, formatted locally by
// the UI: the database stores epoch UTC and every hop in between should be
// unambiguous.
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfc3339Ptr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

func rfc3339Zero(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := rfc3339(t)
	return &s
}
