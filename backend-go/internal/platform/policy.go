// Package platform — reply-window policy (port of platform_policy.go).
// Telegram/LINE always open; WhatsApp templates exempt; the rest must reply
// within 24h of the customer's last inbound message.
package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const CustomerCareWindowHours = 24

// PolicyError marks a terminal delivery failure (never retried).
type PolicyError struct{ Msg string }

func (e *PolicyError) Error() string { return e.Msg }

// EnsureReplyWindow returns the expiry when a reply is allowed, or a
// *PolicyError when the window has closed.
func EnsureReplyWindow(ctx context.Context, db *pgxpool.Pool, platform string, configID int32, sessionID string, isTemplate bool, now time.Time) (time.Time, error) {
	if platform == "telegram" || platform == "line" || (platform == "whatsapp" && isTemplate) {
		return now.Add(CustomerCareWindowHours * time.Hour), nil
	}
	if platform != "whatsapp" && platform != "meta" && platform != "instagram" {
		return time.Time{}, fmt.Errorf("unsupported platform reply policy")
	}
	var lastInbound *time.Time
	var raw *time.Time
	err := db.QueryRow(ctx,
		"SELECT last_inbound_at FROM platform_user_sessions WHERE config_id = $1 AND session_id = $2",
		configID, sessionID).Scan(&raw)
	if err == nil {
		lastInbound = raw
	}
	if lastInbound == nil {
		if platform == "whatsapp" {
			return time.Time{}, &PolicyError{"WhatsApp customer context was not found; an approved template is required"}
		}
		return time.Time{}, &PolicyError{"Customer messaging context was not found"}
	}
	expires := lastInbound.Add(CustomerCareWindowHours * time.Hour)
	if !now.Before(expires) {
		if platform == "whatsapp" {
			return time.Time{}, &PolicyError{"The WhatsApp 24-hour reply window has expired. Send an approved template instead."}
		}
		return time.Time{}, &PolicyError{fmt.Sprintf("The %s 24-hour reply window has expired. Wait for a customer message before replying.", platform)}
	}
	return expires, nil
}
