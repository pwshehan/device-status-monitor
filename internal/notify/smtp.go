// Package notify builds and delivers alert email.
package notify

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// Security names the SMTP transport. net/smtp does not choose one for you, and
// smtp.SendMail only ever does STARTTLS — against a port-465 server it fails
// in a way that looks like bad credentials. All three are explicit here.
const (
	SecurityStartTLS = "starttls" // port 587: plain connect, then upgrade
	SecurityTLS      = "tls"      // port 465: TLS from the first byte
	SecurityNone     = "none"     // port 25: internal relay, usually no auth
)

// Config is the resolved SMTP configuration.
type Config struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
}

// Validate reports whether the configuration is complete enough to try.
func (c Config) Validate() error {
	var missing []string
	if strings.TrimSpace(c.Host) == "" {
		missing = append(missing, "host")
	}
	if c.Port <= 0 || c.Port > 65535 {
		missing = append(missing, "port")
	}
	if strings.TrimSpace(c.From) == "" {
		missing = append(missing, "sender address")
	}
	if len(missing) > 0 {
		return fmt.Errorf("SMTP is not configured: missing %s", strings.Join(missing, ", "))
	}
	switch c.Security {
	case SecurityStartTLS, SecurityTLS, SecurityNone:
	default:
		return fmt.Errorf("unknown SMTP security mode %q", c.Security)
	}
	return nil
}

// Message is one outbound mail.
type Message struct {
	To      []string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a message. The interface exists so the outbox worker can be
// tested without an SMTP server.
type Sender interface {
	Send(ctx context.Context, cfg Config, msg Message) error
}

// SMTP is the real sender.
type SMTP struct {
	// Timeout bounds the whole conversation. Default 30s.
	Timeout time.Duration

	// RootCAs overrides the system trust store, for relays presenting a
	// certificate from a private CA. Nil uses the system roots.
	RootCAs *x509.CertPool
}

// Send delivers msg over the transport named by cfg.Security.
func (s SMTP) Send(ctx context.Context, cfg Config, msg Message) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if len(msg.To) == 0 {
		return errors.New("no recipients configured")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := net.Dialer{Timeout: timeout}
	tlsCfg := &tls.Config{
		ServerName: cfg.Host,
		MinVersion: tls.VersionTLS12,
		RootCAs:    s.RootCAs,
	}

	var conn net.Conn
	var err error
	if cfg.Security == SecurityTLS {
		conn, err = tls.DialWithDialer(&dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer func() { _ = c.Close() }()

	if cfg.Security == SecurityStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s does not offer STARTTLS; try security mode %q or %q",
				addr, SecurityTLS, SecurityNone)
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return fmt.Errorf("%s does not offer AUTH but a username is configured", addr)
		}
		// PlainAuth refuses to send credentials over an unencrypted link, which
		// is why a username with security mode "none" fails here rather than
		// leaking the password.
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("authenticate as %s: %w", cfg.Username, err)
		}
	}

	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM %s: %w", cfg.From, err)
	}
	for _, rcpt := range msg.To {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", rcpt, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(build(cfg, msg, time.Now())); err != nil {
		_ = w.Close()
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return c.Quit()
}

// build renders the RFC 5322 message.
//
// Date and Message-ID are not optional in practice: a message without them
// scores as spam on most receivers, which is a miserable bug to chase when the
// alert simply never arrives.
func build(cfg Config, msg Message, now time.Time) []byte {
	var b strings.Builder

	b.WriteString("From: " + cfg.From + "\r\n")
	b.WriteString("To: " + strings.Join(msg.To, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", msg.Subject) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + messageID(cfg.From) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")

	if msg.HTML == "" {
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		b.WriteString(normalizeEOL(msg.Text))
		return []byte(b.String())
	}

	boundary := randomHex(16)
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(normalizeEOL(msg.Text) + "\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(normalizeEOL(msg.HTML) + "\r\n")
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}

// normalizeEOL converts bare newlines to CRLF, as the wire format requires.
func normalizeEOL(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func messageID(from string) string {
	domain := "localmonitor.local"
	if at := strings.LastIndex(from, "@"); at >= 0 && at < len(from)-1 {
		domain = strings.Trim(from[at+1:], "<> ")
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "monitor"
	}
	return "<" + randomHex(12) + "." + host + "@" + domain + ">"
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// A predictable boundary or Message-ID is cosmetically bad, never a
		// correctness problem, so fall back rather than failing the send.
		return hex.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 16)))
	}
	return hex.EncodeToString(b)
}

// Func adapts a function to Sender, for tests.
type Func func(ctx context.Context, cfg Config, msg Message) error

// Send calls f.
func (f Func) Send(ctx context.Context, cfg Config, msg Message) error { return f(ctx, cfg, msg) }
