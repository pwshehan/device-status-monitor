package probe

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestProbeUp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	res := TCP{}.Probe(context.Background(), 7, ln.Addr().String(), 2*time.Second)
	if !res.OK {
		t.Fatalf("want OK, got %v (%v)", res.Class, res.Err)
	}
	if res.Class != ClassOK {
		t.Errorf("class = %v", res.Class)
	}
	if res.DeviceID != 7 {
		t.Errorf("device id = %d", res.DeviceID)
	}
	if res.ErrMsg() != "" {
		t.Errorf("err msg = %q", res.ErrMsg())
	}
	if res.At.IsZero() {
		t.Error("At not set")
	}
}

func TestProbeRefused(t *testing.T) {
	// Bind and close to get a port nothing is listening on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	res := TCP{}.Probe(context.Background(), 1, addr, 2*time.Second)
	if res.OK {
		t.Fatal("want failure against a closed port")
	}
	if res.Class != ClassRefused {
		t.Errorf("class = %v, want REFUSED (err: %v)", res.Class, res.Err)
	}
	if res.ErrMsg() == "" {
		t.Error("want a non-empty error message")
	}
}

func TestProbeHonoursTheDeadline(t *testing.T) {
	// 192.0.2.0/24 is TEST-NET-1, reserved for documentation. Whether packets
	// to it are dropped or refused depends on the network the test runs on, so
	// this asserts only what is invariant: the probe fails and does not outrun
	// its deadline. Classification is covered by TestClassify, which needs no
	// network at all.
	const timeout = 300 * time.Millisecond
	start := time.Now()
	res := TCP{}.Probe(context.Background(), 1, "192.0.2.1:80", timeout)
	elapsed := time.Since(start)

	if res.OK {
		t.Fatal("want failure against TEST-NET-1")
	}
	if res.Class == ClassOK || res.Class == ClassCanceled {
		t.Errorf("class = %v, want a failure class (err: %v)", res.Class, res.Err)
	}
	if elapsed > timeout+2*time.Second {
		t.Errorf("probe took %s, timeout was %s — the deadline is not being honoured", elapsed, timeout)
	}
}

// fakeTimeout is a net.Error that reports itself as a timeout.
type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "i/o timeout" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Class
	}{
		{"deadline exceeded", context.DeadlineExceeded, ClassTimeout},
		{"net timeout", &net.OpError{Op: "dial", Err: fakeTimeout{}}, ClassTimeout},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, ClassRefused},
		{"host unreachable", &net.OpError{Op: "dial", Err: syscall.EHOSTUNREACH}, ClassUnreachable},
		{"net unreachable", &net.OpError{Op: "dial", Err: syscall.ENETUNREACH}, ClassUnreachable},
		{"dns", &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host"}}, ClassDNS},
		{"anything else", errors.New("something odd"), ClassOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(context.Background(), tc.err); got != tc.want {
				t.Errorf("classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyPrefersCancellation(t *testing.T) {
	// A cancelled parent means the service is stopping. Whatever the dial error
	// says, that is not a device outage.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := classify(ctx, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}); got != ClassCanceled {
		t.Errorf("class = %v, want CANCELED", got)
	}
}

func TestProbeDNSFailure(t *testing.T) {
	res := TCP{}.Probe(context.Background(), 1, "no-such-host.invalid:80", 3*time.Second)
	if res.OK {
		t.Fatal("want failure for an unresolvable host")
	}
	if res.Class != ClassDNS {
		t.Errorf("class = %v, want DNS (err: %v)", res.Class, res.Err)
	}
}

func TestProbeCanceledParentIsNotDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := TCP{}.Probe(ctx, 1, "192.0.2.1:80", 5*time.Second)
	if res.OK {
		t.Fatal("want failure")
	}
	// Shutting the service down must not be reported as a device outage.
	if res.Class != ClassCanceled {
		t.Errorf("class = %v, want CANCELED (err: %v)", res.Class, res.Err)
	}
}
