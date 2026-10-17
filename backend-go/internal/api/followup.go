package api

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"time"

	"khmer-ai-cs-go/internal/platform"
)

// Post-resolution follow-up.
//
// A session that was resolved without the customer saying anything else gets one
// nudge ("anything else?"), but only on a channel whose capability table says it
// may be messaged with no inbound context (Telegram/LINE/Zalo — ProactiveSend).
// Meta's 24-hour window makes the same message a policy violation there, and
// WhatsApp needs a template.
//
// Off by default: FOLLOWUP_HOURS=0. Turning it on messages real customers, which
// is a product decision, not a maintenance default.

// followupHours reads FOLLOWUP_HOURS (hours between resolution and the nudge).
// 0 disables the sweep entirely.
func followupHours() int {
	n, err := strconv.Atoi(os.Getenv("FOLLOWUP_HOURS"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// followupText is the one message a customer receives. The session's language
// decides which one; an unknown language gets English, the dashboard default.
func followupText(language string) string {
	switch language {
	case "km":
		return "សួស្តី! តើនៅមានអ្វីទៀតដែលយើងអាចជួយអ្នកបានទេ? សូមឆ្លើយតបនៅទីនេះ។"
	case "zh":
		return "您好！还有什么需要我们帮忙的吗？直接回复即可。"
	default:
		return "Hi! Is there anything else we can help you with? Just reply here."
	}
}

type followupTarget struct {
	SessionID   string
	UserID      int32
	Platform    string
	RecipientID string
	ConfigID    int32
	Language    string
}

// followupCandidates returns resolved sessions that are due for their one nudge:
// resolved at least hours ago, never nudged, and silent since (a customer who
// wrote again reopened the conversation; nudging that would be noise on top of an
// answer they are waiting for). Test sessions are excluded.
func (a *App) followupCandidates(ctx context.Context, hours, limit int) ([]followupTarget, error) {
	rows, err := a.DB.Query(ctx,
		`SELECT s.session_id::text, s.user_id, s.platform::text, s.platform_user_id, pus.config_id, COALESCE(s.language,'')
		   FROM sessions s
		   JOIN platform_user_sessions pus ON pus.session_id = s.session_id
		  WHERE s.status = 'resolved'
		    AND s.followup_sent_at IS NULL
		    AND s.resolved_at IS NOT NULL
		    AND s.resolved_at <= now() - make_interval(hours => $1)
		    AND s.is_test = FALSE
		    AND NOT EXISTS (
		        SELECT 1 FROM chat_messages cm
		         WHERE cm.session_id = s.session_id AND cm.created_at > s.resolved_at)
		  ORDER BY s.resolved_at
		  LIMIT $2`, hours, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]followupTarget, 0)
	for rows.Next() {
		var t followupTarget
		if err := rows.Scan(&t.SessionID, &t.UserID, &t.Platform, &t.RecipientID, &t.ConfigID, &t.Language); err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// sendFollowups queues one nudge per due session. Each session is one
// transaction: the transcript row, the outbox row and followup_sent_at land
// together, so a crash cannot both record the nudge and lose the message (or send
// it twice).
func (a *App) sendFollowups(ctx context.Context) {
	hours := followupHours()
	if hours <= 0 || a.Pipe == nil {
		return
	}
	targets, err := a.followupCandidates(ctx, hours, 50)
	if err != nil {
		a.Logger.Warn("followup sweep: select failed", "error", err.Error())
		return
	}
	queued := 0
	for _, t := range targets {
		// A channel that cannot be messaged without inbound context must not be
		// nudged; the capability table is the rule, not a platform switch.
		if !platform.CapabilitiesFor(t.Platform).ProactiveSend {
			continue
		}
		text := followupText(t.Language)
		tx, err := a.DB.Begin(ctx)
		if err != nil {
			return
		}
		var msgID int64
		// The tenant column is bound in the statement (INSERT … SELECT), not just
		// implied by the session id: internal/tenantscope refuses a tenant table
		// write that never names the tenant.
		err = tx.QueryRow(ctx,
			"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) "+
				"SELECT s.session_id, 'agent', 'followup', $2, $3 FROM sessions s WHERE s.session_id = $1 AND s.user_id = $4 "+
				"RETURNING message_id",
			t.SessionID, text, time.Now(), t.UserID).Scan(&msgID)
		if err == nil {
			payload, _ := json.Marshal(map[string]any{"kind": "text", "followup": true})
			now := time.Now()
			_, err = tx.Exec(ctx,
				"INSERT INTO platform_outbox (config_id, session_id, chat_message_id, platform, recipient_id, content, payload, status, next_attempt_at, created_at, updated_at) "+
					"VALUES ($1,$2,$3,$4::platform_type,$5,$6,$7,'pending',$8,$8,$8) ON CONFLICT (chat_message_id) DO NOTHING",
				t.ConfigID, t.SessionID, msgID, t.Platform, t.RecipientID, text, payload, now)
		}
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE sessions SET followup_sent_at = NOW() WHERE session_id = $1 AND user_id = $2", t.SessionID, t.UserID)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			a.Logger.Warn("followup sweep: queue failed", "session_id", t.SessionID, "error", err.Error())
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			a.Logger.Warn("followup sweep: commit failed", "session_id", t.SessionID, "error", err.Error())
			continue
		}
		queued++
	}
	if queued > 0 {
		a.Logger.Info("followup sweep queued", "count", queued, "hours", hours)
		a.Pipe.SignalOutbound()
	}
}
