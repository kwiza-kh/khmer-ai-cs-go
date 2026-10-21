package mailer

import (
	"context"
	"errors"
	"testing"
	"time"
)

// SMTP is enabled by the presence of a host and a From address only. Getting
// this wrong turns "mail off" into a Send attempt (or vice versa), so the
// combination is pinned rather than left to the call site.
func TestConfigConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"host and from", Config{Host: "smtp.example.com", From: "ops@example.com"}, true},
		{"host only", Config{Host: "smtp.example.com"}, false},
		{"from only", Config{From: "ops@example.com"}, false},
		{"empty", Config{}, false},
		{"whitespace only", Config{Host: "  ", From: "\t "}, false},
		{"credentials are optional", Config{Host: "127.0.0.1", From: "ops@example.com"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Configured(); got != tc.want {
				t.Errorf("Configured() = %v, want %v", got, tc.want)
			}
		})
	}
}

// An unconfigured deployment must answer the sentinel WITHOUT touching the
// network: the host below is TEST-NET-1 (RFC 5737), so a dial would block until
// the 10s dial timeout and then fail with something that is not the sentinel.
func TestUnconfiguredSendReturnsErrNotConfiguredWithoutDialling(t *testing.T) {
	sender := New(Config{Host: "192.0.2.1", Port: 587})
	start := time.Now()
	err := sender.Send(context.Background(), "user@example.com", "subject", "body")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Send on an unconfigured sender = %v, want ErrNotConfigured", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Send took %s — it must return before any network I/O", elapsed)
	}
}

// The empty-recipient guard also runs before dialling: a configured sender
// asked to deliver nowhere is a caller bug, not a mail-server problem.
func TestSendRejectsEmptyRecipient(t *testing.T) {
	sender := New(Config{Host: "192.0.2.1", From: "ops@example.com"})
	done := make(chan error, 1)
	go func() {
		done <- sender.Send(context.Background(), "   ", "subject", "body")
	}()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("Send with an empty recipient = %v, want a recipient error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send with an empty recipient tried to reach the network")
	}
}

// MAIL FROM carries a bare address while the header may keep a display name;
// passing the display form through makes net/smtp reject the address.
func TestEnvelopeAddress(t *testing.T) {
	cases := map[string]string{
		"ops@example.com":                   "ops@example.com",
		"RelayChat <no-reply@example.com>":  "no-reply@example.com",
		"  spaced@example.com  ":            "spaced@example.com",
		"RelayChat <no-reply@example.com> ": "no-reply@example.com",
	}
	for in, want := range cases {
		if got := envelopeAddress(in); got != want {
			t.Errorf("envelopeAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
