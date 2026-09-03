package notify

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubSMTP is a minimal SMTP server: enough of the protocol to accept one
// message and record it. Testing the three transports needs a server that
// actually speaks them, and net/smtp offers no fake.
type stubSMTP struct {
	t         *testing.T
	ln        net.Listener
	tlsCfg    *tls.Config
	implicit  bool // TLS from the first byte, as on port 465
	offerTLS  bool // advertise STARTTLS
	offerAuth bool

	pool *x509.CertPool

	mu       sync.Mutex
	received []string
	from     string
	rcpt     []string
	authSeen bool
}

func newStub(t *testing.T, implicit, offerTLS, offerAuth bool) *stubSMTP {
	t.Helper()

	cert, pool := selfSigned(t)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}

	var ln net.Listener
	var err error
	if implicit {
		ln, err = tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	} else {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}

	s := &stubSMTP{t: t, ln: ln, tlsCfg: tlsCfg, implicit: implicit, offerTLS: offerTLS, offerAuth: offerAuth}
	s.pool = pool
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *stubSMTP) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))

	br := bufio.NewReader(c)
	write := func(line string) { _, _ = c.Write([]byte(line + "\r\n")) }
	write("220 stub.test ESMTP ready")

	inData := false
	var body strings.Builder

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		if inData {
			if line == "." {
				inData = false
				s.mu.Lock()
				s.received = append(s.received, body.String())
				s.mu.Unlock()
				body.Reset()
				write("250 2.0.0 Ok: queued")
				continue
			}
			body.WriteString(line + "\n")
			continue
		}

		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			ext := []string{"250-stub.test"}
			if s.offerTLS {
				ext = append(ext, "250-STARTTLS")
			}
			if s.offerAuth {
				ext = append(ext, "250-AUTH PLAIN")
			}
			ext = append(ext, "250 SIZE 10240000")
			for _, e := range ext {
				write(e)
			}

		case strings.HasPrefix(verb, "STARTTLS"):
			write("220 2.0.0 Ready to start TLS")
			tc := tls.Server(c, s.tlsCfg)
			if err := tc.Handshake(); err != nil {
				return
			}
			c = tc
			br = bufio.NewReader(c)
			write = func(line string) { _, _ = c.Write([]byte(line + "\r\n")) }

		case strings.HasPrefix(verb, "AUTH"):
			s.mu.Lock()
			s.authSeen = true
			s.mu.Unlock()
			write("235 2.7.0 Authentication successful")

		case strings.HasPrefix(verb, "MAIL FROM"):
			s.mu.Lock()
			s.from = line
			s.mu.Unlock()
			write("250 2.1.0 Ok")

		case strings.HasPrefix(verb, "RCPT TO"):
			s.mu.Lock()
			s.rcpt = append(s.rcpt, line)
			s.mu.Unlock()
			write("250 2.1.5 Ok")

		case verb == "DATA":
			inData = true
			write("354 End data with <CR><LF>.<CR><LF>")

		case verb == "QUIT":
			write("221 2.0.0 Bye")
			return

		default:
			write("250 2.0.0 Ok")
		}
	}
}

func (s *stubSMTP) pooled() *x509.CertPool { return s.pool }

func (s *stubSMTP) port() int {
	_, p, _ := net.SplitHostPort(s.ln.Addr().String())
	n, _ := strconv.Atoi(p)
	return n
}

func (s *stubSMTP) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.received))
	copy(out, s.received)
	return out
}

func (s *stubSMTP) sawAuth() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authSeen
}

func (s *stubSMTP) recipients() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.rcpt))
	copy(out, s.rcpt)
	return out
}

func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// --- tests -------------------------------------------------------------------

func TestSendPlain(t *testing.T) {
	stub := newStub(t, false, false, false)
	cfg := Config{Host: "127.0.0.1", Port: stub.port(), Security: SecurityNone, From: "monitor@example.com"}

	err := SMTP{Timeout: 5 * time.Second}.Send(context.Background(), cfg, Message{
		To:      []string{"ops@example.com", "oncall@example.com"},
		Subject: "[DOWN] Core switch",
		Text:    "plain body",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	msgs := stub.messages()
	if len(msgs) != 1 {
		t.Fatalf("received %d messages", len(msgs))
	}
	if len(stub.recipients()) != 2 {
		t.Errorf("recipients = %v, want 2", stub.recipients())
	}

	body := msgs[0]
	for _, want := range []string{"From: monitor@example.com", "Subject: [DOWN] Core switch",
		"Date: ", "Message-ID: <", "MIME-Version: 1.0", "plain body"} {
		if !strings.Contains(body, want) {
			t.Errorf("message is missing %q:\n%s", want, body)
		}
	}
}

func TestSendSTARTTLS(t *testing.T) {
	stub := newStub(t, false, true, true)
	cfg := Config{
		Host: "127.0.0.1", Port: stub.port(), Security: SecurityStartTLS,
		Username: "monitor", Password: "app-password", From: "monitor@example.com",
	}

	err := SMTP{Timeout: 5 * time.Second, RootCAs: stub.pooled()}.
		Send(context.Background(), cfg, Message{
			To: []string{"ops@example.com"}, Subject: "test", Text: "t", HTML: "<b>t</b>",
		})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !stub.sawAuth() {
		t.Error("credentials were never offered")
	}

	msgs := stub.messages()
	if len(msgs) != 1 {
		t.Fatalf("received %d messages", len(msgs))
	}
	// Both alternatives must be present, in the right order.
	body := msgs[0]
	if !strings.Contains(body, "multipart/alternative") {
		t.Error("want multipart/alternative when HTML is set")
	}
	plainAt := strings.Index(body, "text/plain")
	htmlAt := strings.Index(body, "text/html")
	if plainAt < 0 || htmlAt < 0 || plainAt > htmlAt {
		t.Errorf("plain part must precede the HTML part (plain=%d html=%d)", plainAt, htmlAt)
	}
}

func TestSendImplicitTLS(t *testing.T) {
	// This is the case smtp.SendMail cannot do at all: TLS from the first byte,
	// as on port 465.
	stub := newStub(t, true, false, true)
	cfg := Config{
		Host: "127.0.0.1", Port: stub.port(), Security: SecurityTLS,
		Username: "monitor", Password: "pw", From: "monitor@example.com",
	}

	err := SMTP{Timeout: 5 * time.Second, RootCAs: stub.pooled()}.
		Send(context.Background(), cfg, Message{
			To: []string{"ops@example.com"}, Subject: "test", Text: "t",
		})
	if err != nil {
		t.Fatalf("send over implicit TLS: %v", err)
	}
	if len(stub.messages()) != 1 {
		t.Fatalf("received %d messages", len(stub.messages()))
	}
}

func TestSTARTTLSNotOfferedIsAClearError(t *testing.T) {
	stub := newStub(t, false, false, false)
	cfg := Config{Host: "127.0.0.1", Port: stub.port(), Security: SecurityStartTLS, From: "m@example.com"}

	err := SMTP{Timeout: 5 * time.Second}.Send(context.Background(), cfg,
		Message{To: []string{"ops@example.com"}, Subject: "s", Text: "t"})
	if err == nil {
		t.Fatal("want an error when the server does not offer STARTTLS")
	}
	// The message has to name the fix, because this is exactly the
	// misconfiguration people hit with port 465.
	if !strings.Contains(err.Error(), "STARTTLS") || !strings.Contains(err.Error(), "tls") {
		t.Errorf("error should name the alternative security modes, got: %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"complete", Config{Host: "smtp.example.com", Port: 587, Security: SecurityStartTLS, From: "a@b.c"}, true},
		{"no host", Config{Port: 587, Security: SecurityStartTLS, From: "a@b.c"}, false},
		{"no port", Config{Host: "h", Security: SecurityStartTLS, From: "a@b.c"}, false},
		{"bad port", Config{Host: "h", Port: 70000, Security: SecurityStartTLS, From: "a@b.c"}, false},
		{"no sender", Config{Host: "h", Port: 587, Security: SecurityStartTLS}, false},
		{"unknown security", Config{Host: "h", Port: 587, Security: "ssl", From: "a@b.c"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Errorf("want valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestSendRejectsEmptyRecipients(t *testing.T) {
	cfg := Config{Host: "127.0.0.1", Port: 25, Security: SecurityNone, From: "a@b.c"}
	err := SMTP{}.Send(context.Background(), cfg, Message{Subject: "s", Text: "t"})
	if err == nil {
		t.Fatal("want an error with no recipients")
	}
}

func TestBuildNormalizesLineEndings(t *testing.T) {
	out := string(build(
		Config{From: "a@b.c"},
		Message{To: []string{"x@y.z"}, Subject: "s", Text: "one\ntwo\nthree"},
		time.Now()))

	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Error("body still contains bare newlines; SMTP requires CRLF")
	}
}

func TestBuildEncodesNonASCIISubject(t *testing.T) {
	out := string(build(
		Config{From: "a@b.c"},
		Message{To: []string{"x@y.z"}, Subject: "Café switch down", Text: "t"},
		time.Now()))

	if strings.Contains(out, "Café") {
		t.Error("non-ASCII subject must be RFC 2047 encoded")
	}
	if !strings.Contains(out, "=?utf-8?") {
		t.Errorf("want an encoded-word subject, got:\n%s", out)
	}
}

func TestSenderFuncSatisfiesInterface(t *testing.T) {
	var called bool
	var s Sender = Func(func(context.Context, Config, Message) error {
		called = true
		return errors.New("boom")
	})
	if err := s.Send(context.Background(), Config{}, Message{}); err == nil {
		t.Error("want the stub's error")
	}
	if !called {
		t.Error("stub was not called")
	}
}
