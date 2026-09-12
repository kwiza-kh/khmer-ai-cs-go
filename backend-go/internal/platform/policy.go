// Package platform — reply-window policy (port of platform_policy.go).
// Telegram/LINE/Zalo always open; WhatsApp templates exempt; the rest must
// reply within 24h of the customer's last inbound message. Messenger /
// Instagram human replies may continue for 7 days via the Meta HUMAN_AGENT
// message tag; automatic (model) replies stay locked behind the 24h window.
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
// extended=true marks the 24h window as closed but the 7-day human-agent
// extension in effect (the caller must send with HumanAgentTag) — or a
// *PolicyError when no reply is possible anymore.
func EnsureReplyWindow(ctx context.Context, db *pgxpool.Pool, platform string, configID int32, sessionID string, isTemplate, isHuman bool, now time.Time) (time.Time, bool, error) {
	if platform == "telegram" || platform == "line" || platform == "zalo" || (platform == "whatsapp" && isTemplate) {
		return now.Add(CustomerCareWindowHours * time.Hour), false, nil
	}
	if platform != "whatsapp" && platform != "meta" && platform != "instagram" {
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
		if platform == "whatsapp" {
			return time.Time{}, false, &PolicyError{"WhatsApp customer context was not found; an approved template is required"}
		}
		return time.Time{}, false, &PolicyError{"Customer messaging context was not found"}
	}
	expires := lastInbound.Add(CustomerCareWindowHours * time.Hour)
	if now.Before(expires) {
		return expires, false, nil
	}
	humanDeadline := lastInbound.Add(HumanAgentWindowDays * 24 * time.Hour)
	if platform == "meta" || platform == "instagram" {
		if now.Before(humanDeadline) {
			if isHuman {
				return humanDeadline, true, nil
			}
			return time.Time{}, false, &PolicyError{fmt.Sprintf("The %s 24-hour reply window has expired; automatic replies are not allowed. A human agent may still reply within %d days via the %s tag.", platform, HumanAgentWindowDays, HumanAgentTag)}
		}
		return time.Time{}, false, &PolicyError{fmt.Sprintf("The %s reply window and its %d-day human-agent extension have expired. Wait for a customer message before replying.", platform, HumanAgentWindowDays)}
	}
	// WhatsApp (non-template): hard 24h stop.
	return time.Time{}, false, &PolicyError{"The WhatsApp 24-hour reply window has expired. Send an approved template instead."}
}
