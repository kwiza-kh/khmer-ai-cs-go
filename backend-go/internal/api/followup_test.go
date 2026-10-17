package api

import (
	"context"
	"testing"
	"time"
)

// The sweep messages real customers, so the selection rules are the safety
// boundary: only a resolved, aged, silent session gets the one nudge.
func TestFollowupCandidatesPickOnlySilentAgedSessions(t *testing.T) {
	p := newMemberProbe(t)
	owner := p.user("fu_owner", "user")
	ctx := context.Background()

	seed := func(platformUserID, status string, resolvedAgo time.Duration, nudged bool, afterResolution bool) string {
		t.Helper()
		var resolved *time.Time
		if resolvedAgo > 0 {
			at := time.Now().Add(-resolvedAgo)
			resolved = &at
		}
		var followup *time.Time
		if nudged {
			at := time.Now().Add(-resolvedAgo + time.Minute)
			followup = &at
		}
		var sid string
		if err := p.pool.QueryRow(ctx,
			`INSERT INTO sessions (user_id, platform, platform_user_id, status, language, resolved_at, followup_sent_at, is_test)
			 VALUES ($1, 'telegram', $2, $3, 'km', $4, $5, FALSE) RETURNING session_id::text`,
			owner, platformUserID, status, resolved, followup).Scan(&sid); err != nil {
			t.Fatalf("seed session %s: %v", platformUserID, err)
		}
		if _, err := p.pool.Exec(ctx,
			`INSERT INTO platform_user_sessions (platform, platform_user_id, session_id, config_id) VALUES ('telegram', $1, $2, 6)`,
			platformUserID, sid); err != nil {
			t.Fatalf("seed mapping: %v", err)
		}
		if afterResolution {
			if _, err := p.pool.Exec(ctx,
				`INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1, 'user', 'text', 'one more thing', $2)`,
				sid, time.Now().Add(-resolvedAgo+2*time.Minute)); err != nil {
				t.Fatalf("seed follow-up message: %v", err)
			}
		}
		return sid
	}

	due := seed("fu-due", "resolved", 3*time.Hour, false, false)
	tooFresh := seed("fu-fresh", "resolved", 30*time.Minute, false, false)
	nudged := seed("fu-nudged", "resolved", 3*time.Hour, true, false)
	reopened := seed("fu-reopened", "resolved", 3*time.Hour, false, true)
	active := seed("fu-active", "active", 3*time.Hour, false, false)

	targets, err := p.app.followupCandidates(ctx, 2, 50)
	if err != nil {
		t.Fatalf("followupCandidates: %v", err)
	}
	got := map[string]bool{}
	for _, target := range targets {
		if target.UserID != owner {
			continue // other rows on the shared test database
		}
		got[target.SessionID] = true
	}
	if !got[due] {
		t.Error("an aged, silent, un-nudged session was not selected")
	}
	for name, sid := range map[string]string{"too fresh": tooFresh, "already nudged": nudged, "reopened": reopened, "active": active} {
		if got[sid] {
			t.Errorf("%s session was selected", name)
		}
	}
	// The mapping is what routes the nudge: a candidate without one cannot send.
	for _, target := range targets {
		if target.ConfigID == 0 || target.RecipientID == "" {
			t.Errorf("candidate without a delivery identity: %+v", target)
		}
	}
	// Cleanup this test's mapping rows before the harness deletes sessions.
	if _, err := p.pool.Exec(ctx, "DELETE FROM platform_user_sessions WHERE session_id = ANY($1::uuid[])",
		[]string{due, tooFresh, nudged, reopened, active}); err != nil {
		t.Logf("cleanup mapping: %v", err)
	}
}

func TestFollowupTextSpeaksTheSessionLanguage(t *testing.T) {
	if followupText("km") == followupText("en") {
		t.Error("Khmer and English follow-ups are the same string")
	}
	if followupText("zh") == followupText("en") {
		t.Error("Chinese and English follow-ups are the same string")
	}
	if followupText("xx") != followupText("en") {
		t.Error("an unknown language must fall back to English")
	}
}

func TestFollowupHoursIsOffWithoutTheEnvFlag(t *testing.T) {
	t.Setenv("FOLLOWUP_HOURS", "")
	if followupHours() != 0 {
		t.Error("unset FOLLOWUP_HOURS must disable the sweep")
	}
	t.Setenv("FOLLOWUP_HOURS", "garbage")
	if followupHours() != 0 {
		t.Error("a non-numeric FOLLOWUP_HOURS must disable the sweep, not crash")
	}
	t.Setenv("FOLLOWUP_HOURS", "12")
	if followupHours() != 12 {
		t.Error("FOLLOWUP_HOURS=12 was not read")
	}
}
