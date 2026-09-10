// Package platform — session resolution + customer profile bookkeeping.
package platform

import (
	"context"
	"fmt"
	"time"
)

// ensureSession finds or creates the session + platform mapping, persists the
// user message (with avatar/media metadata), and returns
// (sessionID, userMessageID, isNewMessage, sessionStatus).
func (p *Pipeline) ensureSession(ctx context.Context, ev *InboundEvent, cfg *configCred, content, avatar, mediaURL string, platformMedia map[string]any) (string, int64, bool, string, error) {
	// Upsert the customer profile (avatar + name).
	p.upsertCustomerProfile(ctx, cfg.UserID, cfg.Platform, ev.PlatformUserID, ev.UserDisplayName, avatar)

	// Find or create the session mapping.
	var sessionID string
	err := p.DB.QueryRow(ctx,
		"SELECT session_id FROM platform_user_sessions WHERE config_id = $1 AND platform_user_id = $2",
		ev.ConfigID, ev.PlatformUserID).Scan(&sessionID)
	if err != nil {
		sessionID = newUUID()
		title := truncateRunes(content, 60)
		tx, err := p.DB.Begin(ctx)
		if err != nil {
			return "", 0, false, "", err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx,
			"INSERT INTO sessions (session_id, user_id, platform, platform_user_id, language, status, title) VALUES ($1,$2,$3::platform_type,$4,'km','active',$5)",
			sessionID, cfg.UserID, cfg.Platform, ev.PlatformUserID, title); err != nil {
			return "", 0, false, "", fmt.Errorf("create session: %w", err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO platform_user_sessions (config_id, platform, platform_user_id, session_id, user_display_name, last_inbound_at, created_at) VALUES ($1,$2::platform_type,$3,$4,$5,$6,$6)",
			ev.ConfigID, cfg.Platform, ev.PlatformUserID, sessionID, ev.UserDisplayName, time.Now()); err != nil {
			return "", 0, false, "", fmt.Errorf("create mapping: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return "", 0, false, "", err
		}
	} else {
		_, _ = p.DB.Exec(ctx,
			"UPDATE platform_user_sessions SET last_inbound_at = $1, user_display_name = $2 WHERE config_id = $3 AND platform_user_id = $4",
			time.Now(), ev.UserDisplayName, ev.ConfigID, ev.PlatformUserID)
		// A returned customer un-archives the thread: otherwise their new
		// messages would land in a conversation hidden from the inbox.
		_, _ = p.DB.Exec(ctx, "UPDATE sessions SET archived_at = NULL WHERE session_id = $1 AND archived_at IS NOT NULL", sessionID)
	}

	// Persist the user message (with media metadata for the inbox UI).
	var metadata any
	if platformMedia != nil {
		if mediaURL != "" {
			platformMedia["storage_key"] = mediaURL
		}
		metadata = toJSON(map[string]any{"platform_media": platformMedia})
	}
	var userMessageID int64
	err = p.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, media_url, metadata, created_at) VALUES ($1,'user','text',$2,$3,$4,$5) RETURNING message_id",
		sessionID, content, nullIfEmpty(mediaURL), metadata, time.Now()).Scan(&userMessageID)
	if err != nil {
		return "", 0, false, "", fmt.Errorf("persist user message: %w", err)
	}
	_, _ = p.DB.Exec(ctx, "UPDATE sessions SET user_message_count = user_message_count + 1 WHERE session_id = $1", sessionID)
	p.publishMessage(ctx, cfg.UserID, sessionID, userMessageID, "user")

	var status string
	_ = p.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id = $1", sessionID).Scan(&status)
	if status == "" {
		status = "active"
	}
	return sessionID, userMessageID, true, status, nil
}

// upsertCustomerProfile inserts or refreshes the customer profile row.
func (p *Pipeline) upsertCustomerProfile(ctx context.Context, userID int32, platform, platformUserID, displayName, avatar string) {
	if displayName == "" && avatar == "" {
		return
	}
	_, _ = p.DB.Exec(ctx,
		"INSERT INTO customer_profiles (user_id, platform, platform_user_id, display_name, avatar_url, last_seen_at, updated_at) "+
			"VALUES ($1,$2,$3,$4,$5,$6,$6) "+
			"ON CONFLICT (user_id, platform, platform_user_id) DO UPDATE "+
			"SET display_name = CASE WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name ELSE customer_profiles.display_name END, "+
			"avatar_url = CASE WHEN EXCLUDED.avatar_url <> '' THEN EXCLUDED.avatar_url ELSE customer_profiles.avatar_url END, "+
			"last_seen_at = EXCLUDED.last_seen_at, updated_at = EXCLUDED.updated_at",
		userID, platform, platformUserID, displayName, avatar, time.Now())
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
