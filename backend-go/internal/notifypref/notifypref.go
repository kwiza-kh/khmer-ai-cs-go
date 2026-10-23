// Package notifypref decides whether one notification may be delivered, given
// the recipient's saved preference.
//
// users.notification_pref has existed since migration 034 and the settings page
// has always written it, but nothing read it: choosing "none" still produced
// handoff/SLA/negative-feedback notifications (in-app rows and Telegram pings).
// The preference is an explicit product promise, so the decision lives in one
// place with tests rather than in each notify path.
package notifypref

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	// All is the default: every notification kind.
	All = "all"
	// Escalations keeps the notifications that mean a human is needed, and drops
	// the routine "a customer messaged you" pings.
	Escalations = "escalations"
	// None suppresses every notification this package gates.
	None = "none"
)

// Normalize maps a stored value to one of the three known preferences. Anything
// unrecognised (empty column, legacy value, case/space drift) is All: a broken
// preference must not silently mute a merchant's handoff alerts.
func Normalize(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case Escalations:
		return Escalations
	case None:
		return None
	default:
		return All
	}
}

// IsEscalation reports whether a notification kind means "a human is needed".
// The kinds are the ones the notify paths actually pass today:
//   - handoff: a customer asked for / was handed to a human
//   - sla:     an SLA breach
//   - warning: reply-guard quality flag (an agent should review the answer)
//   - negative_feedback: a 👎 on an AI reply
func IsEscalation(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "handoff", "sla", "warning", "negative_feedback":
		return true
	}
	return false
}

// Allows is the whole decision. An unknown kind under "escalations" is treated
// as routine (suppressed): the escalation list is an allow-list, so a new kind
// must be added deliberately rather than paging every merchant by accident.
func Allows(pref, kind string) bool {
	switch Normalize(pref) {
	case None:
		return false
	case Escalations:
		return IsEscalation(kind)
	default:
		return true
	}
}

// Queryer is the one-method database slice this package needs (pgxpool.Pool
// satisfies it), so callers do not have to hold a pool type here.
type Queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Load reads the recipient's preference. The query coalesces NULL to "" so the
// result is always a string; the default applies in Normalize. An error is
// returned so the caller can log it — callers that cannot fail closed (these
// are auxiliary notifications) send anyway, matching "fail open on auxiliary
// paths".
func Load(ctx context.Context, db Queryer, userID int32) (string, error) {
	var raw string
	if err := db.QueryRow(ctx,
		"SELECT COALESCE(notification_pref, '') FROM users WHERE user_id = $1", userID).Scan(&raw); err != nil {
		return All, err
	}
	return Normalize(raw), nil
}
