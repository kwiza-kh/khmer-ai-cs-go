// Package mailer sends the platform's transactional e-mail.
//
// Only one message exists today — the password-reset link. It lives in its own
// package rather than inside internal/api so the SMTP mechanics (dial timeout,
// STARTTLS, header encoding, the "not configured" path) are testable without a
// database or an HTTP server, and so main.go can inject a Sender
// unconditionally: an unconfigured deployment answers ErrNotConfigured instead
// of panicking on a nil interface.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// ErrNotConfigured is returned by Send when SMTP_HOST/SMTP_FROM are unset. It
// means "mail is off", not "this address failed": callers degrade to the
// operator-generated link instead of reporting a delivery error to the user.
var ErrNotConfigured = errors.New("mailer: SMTP is not configured")

// Sender is the narrow surface handlers depend on. Tests substitute a capture
// sender through api.App.Mailer.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Config is the subset of the SMTP settings this sender needs.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// Configured reports whether enough settings exist to attempt a send: a host
// and a From address. Credentials are optional — a relay on a trusted network
// may accept unauthenticated mail — and EMAIL_ENABLED is deliberately not
// consulted here: that flag gates the notification channel, not this sender.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.From) != ""
}

// smtpSender is the production Sender.
type smtpSender struct{ cfg Config }

// New returns a Sender for cfg. An unconfigured Config is valid and expected:
// Send then answers ErrNotConfigured, which is what lets main.go inject the
// sender without an if-branch.
func New(cfg Config) Sender {
	if cfg.Port <= 0 {
		// 587 is the submission port (STARTTLS + auth); 25 is increasingly
		// blocked outbound, so an accidental zero must not become 25 or a dial
		// to port 0.
		cfg.Port = 587
	}
	return &smtpSender{cfg: cfg}
}

const (
	dialTimeout = 10 * time.Second
	// sendTimeout bounds the whole conversation. Handlers pass a 15s context,
	// but that only cancels the dial; net/smtp itself is not context-aware, so
	// a stuck relay also needs a socket deadline.
	sendTimeout = 30 * time.Second
)

// Send connects, upgrades with STARTTLS when the relay advertises it,
// authenticates when a username is configured, and writes one plain-text
// message.
//
// Nothing retries. A reset link that arrives late is harmless, while a retry
// from the request path would make a broken relay cost the user a slow request
// — the operator can always mint a fresh link.
func (s *smtpSender) Send(ctx context.Context, to, subject, body string) error {
	if !s.cfg.Configured() {
		return ErrNotConfigured
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return errors.New("mailer: empty recipient")
	}
	// A cancel before we touch the network must not open a connection at all.
	if err := ctx.Err(); err != nil {
		return err
	}

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mailer: dial %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(sendTimeout))
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer: greeting %s: %w", s.cfg.Host, err)
	}
	defer func() { _ = c.Close() }()

	if ok, _ := c.Extension("STARTTLS"); ok {
		// ServerName must be the configured host: verifying against the dialled
		// address would fail behind a relay or an IP, and verifying against
		// nothing would accept any certificate.
		if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host}); err != nil {
			return fmt.Errorf("mailer: starttls: %w", err)
		}
	}
	if strings.TrimSpace(s.cfg.Username) != "" {
		// PlainAuth refuses to send the password over an unencrypted connection
		// unless it is to localhost, which is exactly the guard wanted here.
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("mailer: auth: %w", err)
		}
	}
	if err := c.Mail(envelopeAddress(s.cfg.From)); err != nil {
		return fmt.Errorf("mailer: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mailer: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mailer: DATA: %w", err)
	}
	if _, err := w.Write(s.message(to, subject, body)); err != nil {
		_ = w.Close()
		return fmt.Errorf("mailer: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: close body: %w", err)
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("mailer: quit: %w", err)
	}
	return nil
}

// message renders the RFC 5322 payload. Subject is RFC 2047 encoded because the
// three supported user languages are Khmer, Chinese and English; a raw UTF-8
// subject is rejected or mangled by stricter relays. MIME-Version and an
// explicit UTF-8 charset keep the body readable.
func (s *smtpSender) message(to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + s.cfg.From + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

// envelopeAddress strips an optional display name from the configured From
// ("MekongChat <no-reply@example.com>"), because MAIL FROM takes a bare address
// while the header may keep the readable form.
func envelopeAddress(from string) string {
	from = strings.TrimSpace(from)
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.Index(from[i:], ">"); j > 0 {
			if addr := strings.TrimSpace(from[i+1 : i+j]); addr != "" {
				return addr
			}
		}
	}
	return from
}
