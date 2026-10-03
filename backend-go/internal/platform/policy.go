// Package platform — reply-window policy.
//
// The rules used to live in this comment plus a platform switch: Telegram, LINE
// and Zalo were always open, a WhatsApp template was exempt, everything else had
// to reply within 24h of the customer's last inbound message, and
// Messenger/Instagram human replies could continue for 7 days via the Meta
// HUMAN_AGENT message tag.
//
// Those rules are now data. capabilities.go declares Windowless, ReplyWindow,
// HumanExtension and TemplateExempt per channel and this file only evaluates
// them, so a channel with a different window is a table row rather than an edit
// here.
package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const CustomerCareWindowHours = 24

// HumanAgentWindowDays — Meta allows tagged human-agent replies within 7
// days of the customer's last inbound message.
const HumanAgentWindowDays = 7

// HumanAgentTag — the Meta message tag that extends human replies to 7 days.
const HumanAgentTag = "HUMAN_AGENT"

// PolicyError marks a terminal delivery failure (never retried).
type PolicyError struct{ Msg string }

func (e *PolicyError) Error() string { return e.Msg }

// EnsureReplyWindow returns the reply deadline when a reply is allowed —
// extended=true marks the window as closed but the provider's human-agent
// extension in effect (the caller must send with HumanAgentTag) — or a
// *PolicyError when no reply is possible anymore.
func EnsureReplyWindow(ctx context.Context, db *pgxpool.Pool, platform string, configID int32, sessionID string, isTemplate, isHuman bool, now time.Time) (time.Time, bool, error) {
	caps := CapabilitiesFor(platform)

	// Windowless channels — and template sends on a template-exempt channel —
	// never consult the customer context. Keeping this short-circuit ahead of the
	// query preserves the previous behaviour exactly: Telegram/LINE/Zalo
	// deliveries do not touch platform_user_sessions at all.
	if caps.Windowless || (caps.TemplateExempt && isTemplate) {
		return now.Add(CustomerCareWindowHours * time.Hour), false, nil
	}
	// Everything else must declare a window. This is where the old switch's
	// "unsupported platform reply policy" error came from, and it still covers
	// both an unknown platform and a known channel that declares no window (the
	// website widget, which is served over its own SSE path).
	if !caps.Known || caps.ReplyWindow <= 0 {
		return time.Time{}, false, fmt.Errorf("unsupported platform reply policy")
	}

	var lastInbound *time.Time
	var raw *time.Time
	err := db.QueryRow(ctx,
		"SELECT last_inbound_at FROM platform_user_sessions WHERE config_id = $1 AND session_id = $2",
		configID, sessionID).Scan(&raw)
	switch {
	case err == nil:
		// raw stays nil when the column itself is NULL — no inbound recorded.
		lastInbound = raw
	case errors.Is(err, pgx.ErrNoRows):
		// Genuinely no row: fall through to the terminal policy decision.
	default:
		// A transient lookup failure is not a policy decision. Classifying it
		// as terminal (PolicyError) made the delivery non-retryable, so a
		// brief database blip silently dropped the reply forever. Return a
		// plain error so the outbox retries it.
		return time.Time{}, false, fmt.Errorf("look up reply window: %w", err)
	}
	if lastInbound == nil {
		if caps.TemplateExempt {
			return time.Time{}, false, &PolicyError{caps.DisplayName + " customer context was not found; an approved template is required"}
		}
		return time.Time{}, false, &PolicyError{"Customer messaging context was not found"}
	}
	return decideInWindow(caps, lastInbound, isHuman, now)
}

// decideInWindow is the pure half of the policy: capabilities plus the customer's
// last inbound time decide whether a reply is allowed. It is separated from the
// database lookup so the window rules can be tested without a pool
// (see policy_test.go).
func decideInWindow(caps Capabilities, lastInbound *time.Time, isHuman bool, now time.Time) (time.Time, bool, error) {
	expires := lastInbound.Add(caps.ReplyWindow)
	if now.Before(expires) {
		return expires, false, nil
	}
	if caps.HumanExtension <= 0 {
		// Hard stop — WhatsApp without an approved template.
		return time.Time{}, false, &PolicyError{fmt.Sprintf(
			"The %s %d-hour reply window has expired. Send an approved template instead.",
			caps.DisplayName, int(caps.ReplyWindow/time.Hour))}
	}
	days := int(caps.HumanExtension / (24 * time.Hour))
	humanDeadline := lastInbound.Add(caps.HumanExtension)
	if now.Before(humanDeadline) {
		if isHuman {
			return humanDeadline, true, nil
		}
		return time.Time{}, false, &PolicyError{fmt.Sprintf(
			"The %s %d-hour reply window has expired; automatic replies are not allowed. A human agent may still reply within %d days via the %s tag.",
			caps.DisplayName, int(caps.ReplyWindow/time.Hour), days, HumanAgentTag)}
	}
	return time.Time{}, false, &PolicyError{fmt.Sprintf(
		"The %s reply window and its %d-day human-agent extension have expired. Wait for a customer message before replying.",
		caps.DisplayName, days)}
}
