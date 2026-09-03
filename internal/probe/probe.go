// Package probe dials a TCP endpoint and classifies the outcome.
package probe

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"
)

// Class is a coarse classification of a probe outcome. Both TimedOut and
// Refused mean DOWN for alerting, but they read differently to a human: a
// silent host is a different problem from a host that answered "no".
type Class string

const (
	ClassOK          Class = "OK"
	ClassTimeout     Class = "TIMEOUT"
	ClassRefused     Class = "REFUSED"
	ClassDNS         Class = "DNS"
	ClassUnreachable Class = "UNREACHABLE"
	ClassCanceled    Class = "CANCELED"
	ClassOther       Class = "OTHER"
)

// Result is one probe outcome.
type Result struct {
	DeviceID  int64
	OK        bool
	LatencyMS int64
	Class     Class
	Err       error
	At        time.Time
}

// ErrMsg renders the error for storage and email, or "" when the probe succeeded.
func (r Result) ErrMsg() string {
	if r.Err == nil {
		return ""
	}
	return string(r.Class) + ": " + r.Err.Error()
}

// Prober dials one address. The interface exists so the scheduler and the
// state machine can be tested without touching the network.
type Prober interface {
	Probe(ctx context.Context, deviceID int64, addr string, timeout time.Duration) Result
}

// TCP is the real prober: a single connect, measured and immediately closed.
type TCP struct{}

// Probe dials addr and reports how long the connect took.
//
// DialContext rather than DialTimeout, so a service shutdown cancels an
// in-flight dial instead of waiting out the full timeout.
func (TCP) Probe(ctx context.Context, deviceID int64, addr string, timeout time.Duration) Result {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(dialCtx, "tcp", addr)
	elapsed := time.Since(start)

	res := Result{
		DeviceID:  deviceID,
		LatencyMS: elapsed.Milliseconds(),
		At:        start,
	}
	if err != nil {
		res.Err = err
		res.Class = classify(ctx, err)
		return res
	}

	// Drop the connection immediately. SetLinger(0) sends RST instead of going
	// through FIN/TIME_WAIT, which matters when 200 devices are probed on a loop.
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()

	res.OK = true
	res.Class = ClassOK
	return res
}

func classify(parent context.Context, err error) Class {
	// A cancelled parent means we are shutting down, not that the device is down.
	if parent.Err() != nil {
		return ClassCanceled
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ClassDNS
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ClassTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassTimeout
	}
	// Winsock's error numbers before the POSIX ones: on Windows the two sets
	// do not overlap, and Windows is what this ships on.
	if class, ok := platformClass(err); ok {
		return class
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ClassRefused
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return ClassUnreachable
	}
	return ClassOther
}

// Func adapts a function to the Prober interface, for tests.
type Func func(ctx context.Context, deviceID int64, addr string, timeout time.Duration) Result

// Probe calls f.
func (f Func) Probe(ctx context.Context, deviceID int64, addr string, timeout time.Duration) Result {
	return f(ctx, deviceID, addr, timeout)
}
