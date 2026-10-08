package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The users list is scoped to the caller's tenant, and the header stats must agree
// with the page they label. That scoping used to be a Sprintf-built WHERE fragment
// and is now one static clause with bound values — exactly the kind of rewrite that
// can widen a tenant's view by accident, so the rule is pinned against real rows.
//
// Read-only against a real database, gated on DATABASE_URL like the other
// tests that need real rows.
func TestUserListIsScopedToTheCallerAndItsStatsAgree(t *testing.T) {
	pool := openTestPool(t)
	app := &App{DB: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	tenantRows, tenantTotal, tenantStats := userList(t, app, &CurrentUser{UserID: 1, Username: "probe", Role: "admin"}, "")
	platformRows, platformTotal, platformStats := userList(t, app, &CurrentUser{UserID: 1, Username: "probe", Role: "platform_admin"}, "")

	if platformTotal == 0 {
		t.Fatal("the platform admin saw no users at all — the static WHERE matches nothing")
	}
	// The IS NULL guard is what makes one clause serve both roles: it must not
	// collapse the cross-tenant view down to the caller's own user id.
	if platformTotal < tenantTotal {
		t.Errorf("platform total %d < tenant total %d: the scope guard narrowed the unrestricted view", platformTotal, tenantTotal)
	}
	// The stat card and the page it labels are one query pair; if they disagree the
	// console shows "3 users" above a list of 1.
	if tenantStats.Total != tenantTotal {
		t.Errorf("tenant header total %d != page total %d", tenantStats.Total, tenantTotal)
	}
	if platformStats.Total != platformTotal {
		t.Errorf("platform header total %d != page total %d", platformStats.Total, platformTotal)
	}

	// Every row a tenant admin sees must belong to that tenant: the caller itself,
	// or an agent seat they own. Anything else is the enumeration bug this scope
	// exists to prevent.
	owned := ownedUserIDs(t, pool, 1)
	for _, id := range userIDsOf(tenantRows) {
		if !owned[id] {
			t.Errorf("tenant admin saw user %d, who belongs to another tenant", id)
		}
	}
	// And the tenant's view must be a subset of the platform's, not a different set.
	inPlatform := map[int32]bool{}
	for _, id := range userIDsOf(platformRows) {
		inPlatform[id] = true
	}
	for _, id := range userIDsOf(tenantRows) {
		if !inPlatform[id] {
			t.Errorf("user %d is in the tenant view but not in the platform view", id)
		}
	}

	// ?search= must narrow, never widen: a term taken from a real row's username.
	if len(platformRows) > 0 {
		name, _ := platformRows[0]["username"].(string)
		if name != "" {
			term := name[:min(3, len(name))]
			hits, hitTotal, _ := userList(t, app, &CurrentUser{UserID: 1, Role: "platform_admin"}, "search="+term)
			if hitTotal > platformTotal {
				t.Errorf("search %q widened the list: %d hits over %d rows", term, hitTotal, platformTotal)
			}
			for _, row := range hits {
				u, _ := row["username"].(string)
				e, _ := row["email"].(string)
				if !strings.Contains(strings.ToLower(u), strings.ToLower(term)) && !strings.Contains(strings.ToLower(e), strings.ToLower(term)) {
					t.Errorf("search %q returned %q / %q, which do not match", term, u, e)
				}
			}
		}
	}
}

// The sessions list carries the same rewrite: one static clause, the term bound,
// and the ?q= filter must still narrow within the caller's own sessions.
//
// Read-only against a real database, gated on DATABASE_URL.
func TestSessionListFilterNarrowsInsideTheCallersRows(t *testing.T) {
	pool := openTestPool(t)
	app := &App{DB: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	caller := &CurrentUser{UserID: 1, Username: "probe", Role: "admin"}

	rows, total := sessionList(t, app, caller, "")
	for _, row := range rows {
		if id, _ := row["user_id"].(int32); id != caller.UserID {
			t.Fatalf("session list returned a row owned by user %d, want only %d", id, caller.UserID)
		}
	}
	if total < int64(len(rows)) {
		t.Errorf("total %d < the %d rows returned", total, len(rows))
	}

	// A term that cannot match must return nothing, not everything: '%'||$2||'%'
	// binds the user's text as a pattern, and an empty result is the filter working.
	none, noneTotal := sessionList(t, app, caller, "q=zzz-no-such-session-"+strings.Repeat("x", 12))
	if len(none) != 0 || noneTotal != 0 {
		t.Errorf("a nonsense term matched %d row(s) (total %d)", len(none), noneTotal)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

type userPage struct {
	rows  []map[string]any
	total int64
	stats struct{ Total, Active, NewWeek, Admins int64 }
}

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — this test needs a real migrated database")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func userList(t *testing.T, app *App, caller *CurrentUser, query string) ([]map[string]any, int64, struct{ Total, Active, NewWeek, Admins int64 }) {
	t.Helper()
	var stats struct{ Total, Active, NewWeek, Admins int64 }
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users?"+query, nil)
	req = req.WithContext(context.WithValue(req.Context(), userKey, caller))
	out, err := app.listUsers(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("listUsers(%s): %v", query, err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("listUsers(%s): unexpected shape %T", query, out)
	}
	rows, _ := m["data"].([]map[string]any)
	total, _ := m["total"].(int64)
	if raw, ok := m["stats"].(map[string]any); ok {
		stats.Total, _ = raw["total"].(int64)
		stats.Active, _ = raw["active"].(int64)
		stats.NewWeek, _ = raw["new_week"].(int64)
		stats.Admins, _ = raw["admins"].(int64)
	}
	return rows, total, stats
}

func sessionList(t *testing.T, app *App, caller *CurrentUser, query string) ([]map[string]any, int64) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/inbox/sessions?"+query, nil)
	req = req.WithContext(context.WithValue(req.Context(), userKey, caller))
	out, err := app.listSessions(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("listSessions(%s): %v", query, err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("listSessions(%s): unexpected shape %T", query, out)
	}
	rows, _ := m["data"].([]map[string]any)
	total, _ := m["total"].(int64)
	return rows, total
}

// ownedUserIDs is the rule's own answer for "who belongs to this tenant", read from
// the table: the owner plus the seats they hold.
func ownedUserIDs(t *testing.T, pool *pgxpool.Pool, owner int32) map[int32]bool {
	t.Helper()
	owned := map[int32]bool{owner: true}
	rows, err := pool.Query(t.Context(), "SELECT agent_user_id FROM agent_teams WHERE owner_user_id = $1", owner)
	if err != nil {
		t.Fatalf("agent_teams: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err == nil {
			owned[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("agent_teams rows: %v", err)
	}
	return owned
}

func userIDsOf(rows []map[string]any) []int32 {
	out := make([]int32, 0, len(rows))
	for _, row := range rows {
		if id, ok := row["user_id"].(int32); ok {
			out = append(out, id)
		}
	}
	return out
}
