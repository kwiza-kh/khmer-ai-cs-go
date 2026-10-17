package api

import (
	"context"
	"strings"
	"testing"

	"khmer-ai-cs-go/internal/auth"
)

// A password rotation must kill API keys too: they are long-lived bearer tokens
// with no token_version, so leaving them active defeats the rotation.
func TestPasswordChangeRevokesAPIKeys(t *testing.T) {
	p := newMemberProbe(t)
	p.app.JWT = auth.NewJWT(strings.Repeat("k", 32), 1)
	owner := p.user("key_owner", "user")
	ctx := context.Background()

	hash, err := auth.HashPassword("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE users SET password_hash = $1 WHERE user_id = $2", hash, owner); err != nil {
		t.Fatalf("seed password: %v", err)
	}
	if _, err := p.pool.Exec(ctx,
		`INSERT INTO api_keys (user_id, name, key_prefix, key_hash, is_active, expires_at)
		 VALUES ($1, 'probe', 'kcs_', 'probe-hash-' || $1, true, NOW() + interval '30 days')`, owner); err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	user := p.as(owner)
	if _, err := p.call(user, `{"old_password":"old-password-1","new_password":"new-password-2"}`, p.app.changePassword); err != nil {
		t.Fatalf("changePassword: %v", err)
	}
	var active bool
	var version int
	if err := p.pool.QueryRow(ctx, "SELECT is_active FROM api_keys WHERE user_id = $1", owner).Scan(&active); err != nil {
		t.Fatalf("read key: %v", err)
	}
	if active {
		t.Error("API key still active after the password change")
	}
	if err := p.pool.QueryRow(ctx, "SELECT token_version FROM users WHERE user_id = $1", owner).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version == 0 {
		t.Error("token_version was not bumped")
	}
}
