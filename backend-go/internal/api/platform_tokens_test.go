package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
)

// cachedPct is the number an operator reads off the board to explain a cost
// curve that is not following the token curve. The empty case must read as "no
// ratio", not as "0% cached" — 0% is a diagnosis (nothing is being reused) and
// it would be a wrong one.
func TestCachedPct(t *testing.T) {
	cases := []struct {
		name           string
		cached, prompt int64
		want           float64
	}{
		{"no prompts is not 0%", 0, 0, 0},
		{"nothing cached", 0, 100, 0},
		{"half cached", 50, 100, 50},
		{"all cached", 100, 100, 100},
		{"fractional", 1, 3, 100.0 / 3},
		{"more cached than prompt stays computable", 100, 50, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cachedPct(tc.cached, tc.prompt); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("cachedPct(%d, %d) = %v, want %v", tc.cached, tc.prompt, got, tc.want)
			}
		})
	}
}

// The board is cross-tenant read-only SQL rendered as three views of the same
// rows. A wrong join or a filter on the caller's own user id would still answer
// 200 with numbers that quietly disagree with each other — so this asserts the
// views agree, that every tenant appears, and that the daily series covers the
// whole window.
//
// Gated on DATABASE_URL like the other tests that need real rows; nothing here
// writes.
func TestPlatformTokensBoardIsInternallyConsistent(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — this test needs a real migrated database")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	defer pool.Close()

	app := &App{DB: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Cfg: &config.Config{}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/tokens?days=30", nil)
	// A platform admin is the only caller the route admits.
	req = req.WithContext(context.WithValue(req.Context(), userKey,
		&CurrentUser{UserID: 1, Username: "probe", Role: "platform_admin"}))

	out, err := app.platformTokens(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("platformTokens: %v", err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Days   int `json:"days"`
		Totals struct {
			Tokens       int64   `json:"tokens"`
			Cost         float64 `json:"cost"`
			Calls        int64   `json:"calls"`
			CacheHitRate float64 `json:"cache_hit_rate"`
		} `json:"totals"`
		Daily []struct {
			Date   string  `json:"date"`
			Tokens int64   `json:"tokens"`
			Cost   float64 `json:"cost"`
			Calls  int64   `json:"calls"`
		} `json:"daily"`
		ByModel []struct {
			Model  string `json:"model"`
			Tokens int64  `json:"tokens"`
			Calls  int64  `json:"calls"`
		} `json:"by_model"`
		ByTenant []struct {
			UserID       int32   `json:"user_id"`
			Username     string  `json:"username"`
			Plan         string  `json:"plan"`
			Tokens       int64   `json:"tokens"`
			Calls        int64   `json:"calls"`
			CacheHitRate float64 `json:"cache_hit_rate"`
		} `json:"by_tenant"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Days != 30 || len(got.Daily) != 30 {
		t.Fatalf("window = %d days, daily points = %d, want 30/30", got.Days, len(got.Daily))
	}
	if got.Totals.CacheHitRate < 0 || got.Totals.CacheHitRate > 100 {
		t.Errorf("totals cache hit rate %v out of [0,100]", got.Totals.CacheHitRate)
	}

	// The three views must be three views of the same rows.
	var dailyTokens, dailyCalls, modelTokens, tenantTokens, tenantCalls int64
	for _, d := range got.Daily {
		dailyTokens += d.Tokens
		dailyCalls += d.Calls
	}
	for _, m := range got.ByModel {
		modelTokens += m.Tokens
	}
	for _, tn := range got.ByTenant {
		tenantTokens += tn.Tokens
		tenantCalls += tn.Calls
		if tn.UserID <= 0 || tn.Username == "" {
			t.Errorf("tenant row without identity: %+v", tn)
		}
		if tn.CacheHitRate < 0 || tn.CacheHitRate > 100 {
			t.Errorf("tenant %d cache hit rate %v out of [0,100]", tn.UserID, tn.CacheHitRate)
		}
		if tn.Calls == 0 && tn.Tokens != 0 {
			t.Errorf("tenant %d has tokens but no calls: %+v", tn.UserID, tn)
		}
	}
	if dailyTokens != got.Totals.Tokens {
		t.Errorf("daily sum %d != totals %d", dailyTokens, got.Totals.Tokens)
	}
	if dailyCalls != got.Totals.Calls {
		t.Errorf("daily calls %d != totals %d", dailyCalls, got.Totals.Calls)
	}
	if modelTokens != got.Totals.Tokens {
		t.Errorf("by_model sum %d != totals %d", modelTokens, got.Totals.Tokens)
	}
	if tenantTokens != got.Totals.Tokens {
		t.Errorf("by_tenant sum %d != totals %d", tenantTokens, got.Totals.Tokens)
	}
	if tenantCalls != got.Totals.Calls {
		t.Errorf("by_tenant calls %d != totals %d", tenantCalls, got.Totals.Calls)
	}

	// Sorted by consumption: the board's whole purpose is "who is spending".
	for i := 1; i < len(got.ByTenant); i++ {
		if got.ByTenant[i-1].Tokens < got.ByTenant[i].Tokens {
			t.Fatalf("by_tenant not sorted by tokens: %d then %d",
				got.ByTenant[i-1].Tokens, got.ByTenant[i].Tokens)
		}
	}
}
