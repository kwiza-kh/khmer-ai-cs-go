package api

import (
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestResolveWidgetTokenAcceptsBodyToken pins the widget POST path:
// widgetChat and widgetFeedback decode the JSON body first and then resolve
// the embedded token. The public widget page posts the token in the JSON body
// (not as a query parameter or X-Widget-Token header), so this is the path the
// visitor actually exercises; without it the widget loads its config but every
// send/feedback call fails with "无效的小组件令牌".
func TestResolveWidgetTokenAcceptsBodyToken(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — widget token test needs a real migrated database")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	defer pool.Close()

	username := fmt.Sprintf("wtest_%d", time.Now().UnixNano())
	token := fmt.Sprintf("wt_test_%d", time.Now().UnixNano())
	if len(token) > 64 {
		t.Fatalf("generated token too long: %d", len(token))
	}

	var userID int32
	err = pool.QueryRow(ctx,
		"INSERT INTO users (username, email, password_hash) VALUES ($1,$2,$3) RETURNING user_id",
		username, username+"@example.com", "test-password-hash").Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, "DELETE FROM users WHERE user_id = $1", userID); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	}()

	_, err = pool.Exec(ctx,
		"INSERT INTO widget_tokens (user_id, name, token) VALUES ($1,$2,$3)",
		userID, "widget-handler-test", token)
	if err != nil {
		t.Fatalf("insert widget token: %v", err)
	}

	a := &App{DB: pool}
	req := httptest.NewRequest("POST", "/api/v1/widget/chat", nil)
	got, apiErr := a.resolveWidgetToken(req, token)
	if apiErr != nil {
		t.Fatalf("resolveWidgetToken returned error: %+v", apiErr)
	}
	if got.Token != token || got.ownerID != userID {
		t.Fatalf("resolved wrong token row: token=%q ownerID=%d, want token=%q ownerID=%d",
			got.Token, got.ownerID, token, userID)
	}
}

// TestResolveWidgetTokenRejectsMissing pins the fail-closed branch without
// needing a database: an empty token must be rejected before any DB access.
func TestResolveWidgetTokenRejectsMissing(t *testing.T) {
	a := &App{}
	req := httptest.NewRequest("POST", "/api/v1/widget/chat", nil)
	got, apiErr := a.resolveWidgetToken(req, "")
	if apiErr == nil {
		t.Fatal("resolveWidgetToken accepted an empty token")
	}
	if got != nil {
		t.Fatalf("resolveWidgetToken returned a row for an empty token: %+v", got)
	}
	if apiErr.Status != 401 || apiErr.Message != "无效的小组件令牌" {
		t.Fatalf("unexpected error: status=%d message=%q", apiErr.Status, apiErr.Message)
	}
}
