package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tenantRows drives listTenants and returns the rows + the reported total, so a
// test can check that the page and its count agree.
func tenantRows(t *testing.T, app *App, query string) ([]map[string]any, int64) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/tenants?"+query, nil)
	req = req.WithContext(context.WithValue(req.Context(), userKey,
		&CurrentUser{UserID: 1, Username: "probe", Role: "platform_admin"}))
	out, err := app.listTenants(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("listTenants(%s): %v", query, err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("listTenants(%s): unexpected shape %T", query, out)
	}
	rows, _ := m["data"].([]map[string]any)
	total, _ := m["total"].(int64)
	return rows, total
}

func hasMethod(row map[string]any, method string) bool {
	methods, _ := row["auth_methods"].([]string)
	return slices.Contains(methods, method)
}

// "Who registered through Telegram?" is the question this filter exists to
// answer: a Telegram signup has no e-mail, and its username is either the
// Telegram handle or "tg_<id>", so searching by name/phone/id is the only way
// to find the person. The filter must never widen the result set, and the
// count must match the page it labels.
//
// Read-only against a real database, gated on DATABASE_URL like the other
// tests that need real rows.
func TestTenantListSearchAndAuthFilter(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — this test needs a real migrated database")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	defer pool.Close()
	app := &App{DB: pool}

	all, totalAll := tenantRows(t, app, "page_size=200")
	if int64(len(all)) != totalAll {
		t.Fatalf("unfiltered page has %d rows but reports total %d", len(all), totalAll)
	}

	// The auth filter narrows, never widens, and every row really carries that
	// sign-in method.
	for _, method := range []string{"telegram", "google", "password"} {
		rows, total := tenantRows(t, app, "page_size=200&auth="+method)
		if int64(len(rows)) != total {
			t.Errorf("auth=%s: %d rows but total %d", method, len(rows), total)
		}
		if len(rows) > len(all) {
			t.Errorf("auth=%s returned more rows than the unfiltered list (%d > %d)", method, len(rows), len(all))
		}
		for _, r := range rows {
			if !hasMethod(r, method) {
				t.Errorf("auth=%s returned a row without that method: %v", method, r["auth_methods"])
			}
		}
	}

	// Identity search: pick a real Telegram account and find it by the things an
	// operator would actually be told on the phone (display name, phone, id).
	var (
		username, displayName, phone, sub string
	)
	err = pool.QueryRow(context.Background(),
		`SELECT username, COALESCE(display_name,''), COALESCE(phone,''), COALESCE(telegram_sub,'')
		 FROM users WHERE telegram_sub IS NOT NULL ORDER BY user_id LIMIT 1`).
		Scan(&username, &displayName, &phone, &sub)
	if err != nil {
		t.Skip("no Telegram-linked account in this database; identity search not exercised")
	}
	for _, term := range []string{displayName, phone, sub} {
		if term == "" {
			continue
		}
		rows, total := tenantRows(t, app, "page_size=200&q="+term)
		if int64(len(rows)) != total {
			t.Errorf("q=%s: %d rows but total %d", term, len(rows), total)
		}
		found := false
		for _, r := range rows {
			if r["username"] == username {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("searching %q did not find the Telegram account %q", term, username)
		}
	}

	// A term nobody matches returns an empty page, not the whole table — the
	// failure mode of a filter that silently stops applying.
	rows, total := tenantRows(t, app, "page_size=200&q="+strings.Repeat("zzz-no-such-tenant-", 3))
	if len(rows) != 0 || total != 0 {
		t.Fatalf("nonsense search returned %d rows (total %d), want 0", len(rows), total)
	}
}
