package platform

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// This file implements Meta's Data Deletion Request Callback, mandatory under
// Meta Platform Terms §3(d)(i). Meta POSTs a `signed_request` field; we verify
// its HMAC-SHA256 signature, then delete whatever we hold for that subject and
// reply with the confirmation code + status URL Meta requires.
//
// The hard part is not the signature, it is the subject lookup. Meta sends an
// app-scoped user id (ASID), while this product stores page-scoped ids (PSID /
// IGSID / wa_id) for end customers and its own user ids for business owners.
// An ASID will therefore usually match nothing. The callback must still answer
// correctly: Meta requires a well-formed reply, and silently claiming a
// deletion that never happened would be worse than useless. So: verify, record,
// delete exactly what matches, and mark the rest unresolved.

// signedRequestPayload is the verified body of a Meta signed_request.
type signedRequestPayload struct {
	Algorithm string `json:"algorithm"`
	UserID    string `json:"user_id"`
	IssuedAt  int64  `json:"issued_at"`
	Expires   int64  `json:"expires"`
}

// errSignedRequest is the single rejection reason. Callers answer 401 without
// disclosing which check failed.
var errSignedRequest = fmt.Errorf("invalid signed_request")

// parseSignedRequest verifies a Meta signed_request and returns its payload.
//
// Wire format: base64url(signature).base64url(payload), the signature being
// HMAC-SHA256 over the payload segment **as transmitted** (the encoded string,
// not the decoded bytes). Verification happens before the payload is decoded,
// so malformed input never reaches the JSON parser.
func parseSignedRequest(secret, signed string) (*signedRequestPayload, error) {
	if secret == "" {
		// An empty key would let anyone forge a matching signature.
		return nil, errSignedRequest
	}
	parts := strings.SplitN(signed, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, errSignedRequest
	}

	providedSig, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[0], "="))
	if err != nil {
		return nil, errSignedRequest
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[1]))
	if !hmac.Equal(mac.Sum(nil), providedSig) {
		return nil, errSignedRequest
	}

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, errSignedRequest
	}
	var p signedRequestPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errSignedRequest
	}
	// Reject algorithm confusion: only the scheme actually verified is allowed.
	if !strings.EqualFold(p.Algorithm, "HMAC-SHA256") {
		return nil, errSignedRequest
	}
	if p.UserID == "" {
		return nil, errSignedRequest
	}
	return &p, nil
}

// subjectIDIsSafe guards the delete path. Everything downstream matches on
// exact string equality, but a short or empty id could still collide with a
// real value, so refuse anything implausible rather than risk deleting the
// wrong person's data.
func subjectIDIsSafe(id string) bool {
	return len(id) >= 5 && len(id) <= 64
}

// metaFamily are the platform_type values whose platform_user_id shares Meta's
// identifier space. Scoping to these stops a numeric id that happens to collide
// with a Telegram or LINE id from matching another person's sessions.
var metaFamily = []string{"meta", "instagram", "whatsapp"}

// newConfirmationCode returns an uppercase alphanumeric code. Meta only
// requires "alphanumeric"; base32 avoids the 0/O and 1/I confusion when a
// customer reads it back to support.
func newConfirmationCode() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]), nil
}

// deletionOutcome is the auditable record of what a request actually removed.
type deletionOutcome struct {
	Sessions      int64
	Messages      int64
	InboundEvents int64
	QueryLogs     int64
	Notifications int64
	TokenUsage    int64
	Outbox        int64
	UserSessions  int64
	Profiles      int64
}

// Total counts every row removed across all tables. Zero means "verified, but
// nothing in this system is keyed by the id Meta supplied".
func (o deletionOutcome) Total() int64 {
	return o.Sessions + o.Messages + o.InboundEvents + o.QueryLogs +
		o.Notifications + o.TokenUsage + o.Outbox + o.UserSessions + o.Profiles
}

// String renders the outcome for the audit trail.
func (o deletionOutcome) String() string {
	return fmt.Sprintf("sessions=%d messages=%d inbound_events=%d query_logs=%d "+
		"notifications=%d token_usage=%d outbox=%d user_sessions=%d profiles=%d",
		o.Sessions, o.Messages, o.InboundEvents, o.QueryLogs,
		o.Notifications, o.TokenUsage, o.Outbox, o.UserSessions, o.Profiles)
}

// deleteMetaSubject removes every record tied to one Meta-scoped user id.
//
// Deleting the sessions row alone is NOT sufficient, and that is the trap this
// function exists to avoid. Audited against the live schema:
//
//   - platform_inbound_events.session_id is ON DELETE SET NULL, so the row
//     survives with its `content` column — the raw customer message — intact.
//   - rag_query_logs, notifications and token_usage carry a session_id with NO
//     foreign key at all, so a session delete orphans them instead of removing
//     them.
//
// Identity columns are matched first, while the linkage still exists, then the
// sessions are deleted so the ON DELETE CASCADE tables follow. Everything runs
// in one transaction: a partial deletion that leaves the trail behind is the
// exact failure this is meant to prevent.
func (wh *Webhooks) deleteMetaSubject(ctx context.Context, metaUserID string) (deletionOutcome, error) {
	var out deletionOutcome
	if !subjectIDIsSafe(metaUserID) {
		return out, fmt.Errorf("refusing to delete for implausible subject id (len=%d)", len(metaUserID))
	}

	tx, err := wh.DB.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Snapshot the session ids first: they are needed for the tables that have
	// no foreign key, and they disappear in the final step.
	rows, err := tx.Query(ctx,
		"SELECT session_id FROM sessions WHERE platform::text = ANY($1::text[]) AND platform_user_id = $2",
		metaFamily, metaUserID)
	if err != nil {
		return out, fmt.Errorf("collect sessions: %w", err)
	}
	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, fmt.Errorf("scan session id: %w", err)
		}
		sessionIDs = append(sessionIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("iterate sessions: %w", err)
	}

	// 1. Rows carrying the raw message text, which SET NULL would preserve.
	tag, err := tx.Exec(ctx,
		"DELETE FROM platform_inbound_events WHERE platform::text = ANY($1::text[]) AND platform_user_id = $2",
		metaFamily, metaUserID)
	if err != nil {
		return out, fmt.Errorf("delete inbound events: %w", err)
	}
	out.InboundEvents = tag.RowsAffected()

	// 2. session_id-linked tables with no foreign key to sessions.
	if len(sessionIDs) > 0 {
		for _, step := range []struct {
			sql string
			dst *int64
			err string
		}{
			{"DELETE FROM rag_query_logs WHERE session_id = ANY($1::uuid[])", &out.QueryLogs, "delete query logs"},
			{"DELETE FROM notifications WHERE session_id = ANY($1::uuid[])", &out.Notifications, "delete notifications"},
			{"DELETE FROM token_usage WHERE session_id = ANY($1::uuid[])", &out.TokenUsage, "delete token usage"},
		} {
			t, err := tx.Exec(ctx, step.sql, sessionIDs)
			if err != nil {
				return out, fmt.Errorf("%s: %w", step.err, err)
			}
			*step.dst = t.RowsAffected()
		}
	}

	// 3. Outbox rows addressed to this subject (the cascade covers session-bound
	//    ones; this catches any already unbound).
	tag, err = tx.Exec(ctx,
		"DELETE FROM platform_outbox WHERE platform::text = ANY($1::text[]) AND recipient_id = $2",
		metaFamily, metaUserID)
	if err != nil {
		return out, fmt.Errorf("delete outbox: %w", err)
	}
	out.Outbox = tag.RowsAffected()

	// 4. The identity map itself, and the CRM row.
	tag, err = tx.Exec(ctx,
		"DELETE FROM platform_user_sessions WHERE platform::text = ANY($1::text[]) AND platform_user_id = $2",
		metaFamily, metaUserID)
	if err != nil {
		return out, fmt.Errorf("delete user sessions: %w", err)
	}
	out.UserSessions = tag.RowsAffected()

	tag, err = tx.Exec(ctx,
		"DELETE FROM customer_profiles WHERE platform::text = ANY($1::text[]) AND platform_user_id = $2",
		metaFamily, metaUserID)
	if err != nil {
		return out, fmt.Errorf("delete customer profiles: %w", err)
	}
	out.Profiles = tag.RowsAffected()

	// 5. Count the messages before the cascade removes them, so the audit trail
	//    can state how much conversation content was destroyed.
	if len(sessionIDs) > 0 {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM chat_messages WHERE session_id = ANY($1::uuid[])", sessionIDs).
			Scan(&out.Messages); err != nil {
			return out, fmt.Errorf("count messages: %w", err)
		}
	}

	// 6. Finally the sessions: this cascades chat_messages,
	//    human_handoff_requests, message_notes, session_assignments,
	//    sla_breaches, platform_user_sessions and platform_outbox.
	tag, err = tx.Exec(ctx,
		"DELETE FROM sessions WHERE platform::text = ANY($1::text[]) AND platform_user_id = $2",
		metaFamily, metaUserID)
	if err != nil {
		return out, fmt.Errorf("delete sessions: %w", err)
	}
	out.Sessions = tag.RowsAffected()

	if err := tx.Commit(ctx); err != nil {
		return out, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// recordDeletionRequest writes the audit row. An existing row for the same
// (source, subject) keeps its original confirmation code and is updated in
// place, so a Meta retry yields the same code rather than a second code for
// the same request.
func (wh *Webhooks) recordDeletionRequest(ctx context.Context, code, source, metaUserID string, configID *int32, status, detail string, out deletionOutcome) (string, error) {
	var finalCode string
	err := wh.DB.QueryRow(ctx, `
		INSERT INTO deletion_requests
			(confirmation_code, source, meta_app_scoped_id, matched_config_id, status, detail,
			 sessions_deleted, messages_deleted, inbound_events_deleted, query_logs_deleted, completed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
			CASE WHEN $5 IN ('completed','unresolved') THEN NOW() ELSE NULL END)
		ON CONFLICT (source, meta_app_scoped_id) DO UPDATE
			SET status                 = EXCLUDED.status,
			    detail                 = EXCLUDED.detail,
			    matched_config_id      = EXCLUDED.matched_config_id,
			    sessions_deleted       = EXCLUDED.sessions_deleted,
			    messages_deleted       = EXCLUDED.messages_deleted,
			    inbound_events_deleted = EXCLUDED.inbound_events_deleted,
			    query_logs_deleted     = EXCLUDED.query_logs_deleted,
			    completed_at           = EXCLUDED.completed_at
		RETURNING confirmation_code`,
		code, source, metaUserID, configID, status, detail,
		out.Sessions, out.Messages, out.InboundEvents, out.QueryLogs).Scan(&finalCode)
	if err != nil {
		return "", err
	}
	return finalCode, nil
}

// MetaDataDeletion implements Meta's Data Deletion Request Callback.
//
// Request: POST form field signed_request. Reply: {"url":…,"confirmation_code":…}.
//
// The callback URL carries no shared header to match a tenant against, so each
// candidate app secret is tried in turn: the platform's own app first, then
// every active Meta-family config (tenants may bring their own app). A request
// verifying against none of them is rejected — an unauthenticated caller must
// never be able to name a subject and have data deleted.
func (wh *Webhooks) MetaDataDeletion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeDeletionJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed form"})
		return
	}
	signed := r.FormValue("signed_request")
	if signed == "" {
		writeDeletionJSON(w, http.StatusBadRequest, map[string]string{"error": "missing signed_request"})
		return
	}

	ctx := r.Context()
	payload, configID, ok := wh.verifyAgainstAnySecret(ctx, signed)
	if !ok {
		wh.log().Warn("meta data deletion rejected: signature verified against no app secret",
			"ip", r.RemoteAddr)
		writeDeletionJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
		return
	}
	if !subjectIDIsSafe(payload.UserID) {
		wh.log().Warn("meta data deletion rejected: implausible subject id", "len", len(payload.UserID))
		writeDeletionJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subject"})
		return
	}

	out, err := wh.deleteMetaSubject(ctx, payload.UserID)
	status := "completed"
	detail := "all matching records removed"
	switch {
	case err != nil:
		status = "failed"
		detail = err.Error()
		wh.log().Error("meta data deletion failed", "error", err.Error())
	case out.Total() == 0:
		// Verified and authentic, but nothing here is keyed by the id Meta
		// sent. Say so rather than implying a deletion happened.
		status = "unresolved"
		detail = "no record matches the app-scoped id Meta supplied; this deployment " +
			"keys end customers by page-scoped id (PSID/IGSID/wa_id) and does not persist " +
			"Facebook user ids. Queued for the manual path."
	}

	code, cerr := newConfirmationCode()
	if cerr != nil {
		wh.log().Error("generate confirmation code failed", "error", cerr.Error())
		writeDeletionJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	finalCode, rerr := wh.recordDeletionRequest(ctx, code, "meta_callback", payload.UserID, configID, status, detail, out)
	if rerr != nil {
		// Meta still needs a well-formed answer, and the deletion itself has
		// already committed, so do not fail the callback over the audit row.
		wh.log().Error("record deletion request failed", "error", rerr.Error())
		finalCode = code
	}

	wh.log().Info("meta data deletion handled",
		"status", status,
		"confirmation_code", finalCode,
		"issued_at_age_s", int(time.Since(time.Unix(payload.IssuedAt, 0)).Seconds()),
		"outcome", out.String())

	writeDeletionJSON(w, http.StatusOK, map[string]string{
		"url":               wh.statusURL(finalCode),
		"confirmation_code": finalCode,
	})
}

// verifyAgainstAnySecret tries the platform app secret and every active
// Meta-family tenant secret, returning the payload and the config that matched.
func (wh *Webhooks) verifyAgainstAnySecret(ctx context.Context, signed string) (*signedRequestPayload, *int32, bool) {
	if p, err := parseSignedRequest(wh.MetaAppSecret, signed); err == nil {
		return p, nil, true
	}
	rows, err := wh.DB.Query(ctx,
		"SELECT config_id, webhook_secret FROM platform_configs "+
			"WHERE platform::text = ANY($1::text[]) AND is_active = true AND webhook_secret IS NOT NULL",
		metaFamily)
	if err != nil {
		wh.log().Error("load meta secrets for deletion verify", "error", err.Error())
		return nil, nil, false
	}
	defer rows.Close()

	type candidate struct {
		id     int32
		secret string
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		var enc *string
		if err := rows.Scan(&c.id, &enc); err != nil {
			continue
		}
		if enc != nil {
			if plain, derr := wh.Sealer.Decrypt(*enc); derr == nil {
				c.secret = plain
			}
		}
		if c.secret != "" {
			cands = append(cands, c)
		}
	}
	for _, c := range cands {
		if p, err := parseSignedRequest(c.secret, signed); err == nil {
			id := c.id
			return p, &id, true
		}
	}
	return nil, nil, false
}

// log falls back to the default logger so the handler is safe in tests.
func (wh *Webhooks) log() *slog.Logger {
	if wh.Logger != nil {
		return wh.Logger
	}
	return slog.Default()
}

// statusURL is the page a data subject can open to see the outcome.
func (wh *Webhooks) statusURL(code string) string {
	base := strings.TrimSuffix(wh.PublicBaseURL, "/")
	if base == "" {
		base = "https://cs.wanfanginsulationmaterial.com"
	}
	return base + "/privacy/deletion-status?code=" + code
}

func writeDeletionJSON(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
