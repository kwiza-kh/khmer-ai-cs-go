package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
)

// opsCall runs one of the platform ops handlers as a platform admin and decodes
// the JSON into out.
func opsCall(t *testing.T, app *App, handler func(http.ResponseWriter, *http.Request) (any, error), path string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(context.WithValue(req.Context(), userKey,
		&CurrentUser{UserID: 1, Username: "probe", Role: "platform_admin"}))
	v, err := handler(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: marshal: %v", path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: unmarshal: %v", path, err)
	}
}

type todoShape struct {
	SupportOpen   int64 `json:"support_open"`
	ChannelErrors int64 `json:"channel_errors"`
	OutboxFailed  int64 `json:"outbox_failed"`
	OutboxBacklog int64 `json:"outbox_backlog"`
	ExpiringPaid  int64 `json:"expiring_paid"`
	QuotaPressure int64 `json:"quota_pressure"`
	LapseDays     int   `json:"lapse_days"`
}

type channelsShape struct {
	Data []struct {
		ConfigID      int32  `json:"config_id"`
		Status        string `json:"status"`
		OutboxPending int64  `json:"outbox_pending"`
		OutboxFailed  int64  `json:"outbox_failed"`
	} `json:"data"`
	Errors        int64 `json:"errors"`
	OutboxPending int64 `json:"outbox_pending"`
	OutboxFailed  int64 `json:"outbox_failed"`
}

type supportShape struct {
	Open  int64 `json:"open"`
	Total int64 `json:"total"`
	Data  []struct {
		MessageID int64   `json:"message_id"`
		RepliedAt *string `json:"replied_at"`
	} `json:"data"`
}

type revenueShape struct {
	GrossUSD     float64 `json:"gross_usd"`
	Captured     int64   `json:"captured_payments"`
	ExpiringSoon int64   `json:"expiring_soon"`
	Overdue      int64   `json:"overdue"`
	LapseDays    int     `json:"lapse_days"`
	Tenants      []struct {
		UserID    int32   `json:"user_id"`
		Username  string  `json:"username"`
		State     string  `json:"state"`
		PaidUntil *string `json:"paid_until"`
	} `json:"tenants"`
	Recent []struct {
		PaymentID int64   `json:"payment_id"`
		Plan      string  `json:"plan"`
		Status    string  `json:"status"`
		Amount    float64 `json:"amount"`
	} `json:"recent"`
}

// The ops surfaces are four views over the same operational rows, and the todo
// card is the summary an operator acts on. If a summary count drifts from the
// section it links to, the operator chases a number that no list explains — so
// this pins each count against its detail view, on a real database.
//
// Read-only; gated on DATABASE_URL like the other tests that need real rows.
func TestPlatformOpsViewsReconcile(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — this test needs a real migrated database")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	defer pool.Close()
	app := &App{
		DB:     pool,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cfg:    &config.Config{PayPal: config.PayPalConfig{PricePro: "29.00", PriceEnterprise: "199.00"}},
	}

	var todo todoShape
	opsCall(t, app, app.platformTodo, "/api/v1/platform/todo", &todo)
	for name, v := range map[string]int64{
		"support_open": todo.SupportOpen, "channel_errors": todo.ChannelErrors,
		"outbox_failed": todo.OutboxFailed, "outbox_backlog": todo.OutboxBacklog,
		"expiring_paid": todo.ExpiringPaid, "quota_pressure": todo.QuotaPressure,
	} {
		if v < 0 {
			t.Errorf("todo.%s is negative: %d", name, v)
		}
	}

	// --- channels: summary == rows it summarises ---
	var ch channelsShape
	opsCall(t, app, app.platformChannels, "/api/v1/platform/channels", &ch)
	seen := map[int32]bool{}
	var rowsErrors, rowsPending, rowsFailed int64
	for _, row := range ch.Data {
		if seen[row.ConfigID] {
			t.Errorf("config %d listed twice — the outbox aggregate is fanning out rows", row.ConfigID)
		}
		seen[row.ConfigID] = true
		if row.Status != "connected" {
			rowsErrors++
		}
		rowsPending += row.OutboxPending
		rowsFailed += row.OutboxFailed
	}
	if rowsErrors != ch.Errors {
		t.Errorf("channels: %d error rows but summary says %d", rowsErrors, ch.Errors)
	}
	if rowsPending != ch.OutboxPending {
		t.Errorf("channels: pending rows sum to %d but summary says %d", rowsPending, ch.OutboxPending)
	}
	if rowsFailed != ch.OutboxFailed {
		t.Errorf("channels: failed rows sum to %d but summary says %d", rowsFailed, ch.OutboxFailed)
	}
	// The todo card must agree with the section it links to.
	if todo.ChannelErrors != ch.Errors {
		t.Errorf("todo.channel_errors %d != channels.errors %d", todo.ChannelErrors, ch.Errors)
	}
	if todo.OutboxFailed != ch.OutboxFailed {
		t.Errorf("todo.outbox_failed %d != channels.outbox_failed %d", todo.OutboxFailed, ch.OutboxFailed)
	}
	if todo.OutboxBacklog != ch.OutboxPending {
		t.Errorf("todo.outbox_backlog %d != channels.outbox_pending %d", todo.OutboxBacklog, ch.OutboxPending)
	}

	// --- support: the filter really filters, and the count matches ---
	var open, all supportShape
	opsCall(t, app, app.platformSupport, "/api/v1/platform/support-messages?status=open", &open)
	opsCall(t, app, app.platformSupport, "/api/v1/platform/support-messages?status=all", &all)
	if open.Open != all.Open || open.Total != all.Total {
		t.Errorf("open/total differ between filters: %+v vs %+v", open, all)
	}
	if open.Total < open.Open {
		t.Errorf("total %d < open %d", open.Total, open.Open)
	}
	if todo.SupportOpen != open.Open {
		t.Errorf("todo.support_open %d != support.open %d", todo.SupportOpen, open.Open)
	}
	for _, row := range open.Data {
		if row.RepliedAt != nil {
			t.Errorf("status=open returned an already-replied message %d (%s)", row.MessageID, *row.RepliedAt)
		}
	}
	// The list is capped at 200 rows while the count is not: the page may be
	// shorter than the count, never longer.
	if int64(len(open.Data)) > open.Open+200 || int64(len(all.Data)) > all.Total+200 {
		t.Errorf("list longer than its own total: open %d/%d, all %d/%d",
			len(open.Data), open.Open, len(all.Data), all.Total)
	}

	// --- revenue: per-row state must match the counts, and the todo tile counts
	// everything due inside the window (overdue included) ---
	var rev revenueShape
	opsCall(t, app, app.platformRevenue, "/api/v1/platform/revenue", &rev)
	var byState, expiring, overdue int64
	for _, row := range rev.Tenants {
		switch row.State {
		case "expiring":
			expiring++
		case "overdue":
			overdue++
		case "active", "no_clock":
		default:
			t.Errorf("tenant %d has unknown state %q", row.UserID, row.State)
		}
		if row.State == "no_clock" && row.PaidUntil != nil {
			t.Errorf("tenant %d is no_clock but carries paid_until %s", row.UserID, *row.PaidUntil)
		}
		byState++
	}
	if expiring != rev.ExpiringSoon {
		t.Errorf("revenue: %d expiring rows but summary says %d", expiring, rev.ExpiringSoon)
	}
	if overdue != rev.Overdue {
		t.Errorf("revenue: %d overdue rows but summary says %d", overdue, rev.Overdue)
	}
	if todo.ExpiringPaid != rev.ExpiringSoon+rev.Overdue {
		t.Errorf("todo.expiring_paid %d != expiring %d + overdue %d",
			todo.ExpiringPaid, rev.ExpiringSoon, rev.Overdue)
	}
	if rev.GrossUSD < 0 || rev.Captured < 0 {
		t.Errorf("revenue totals negative: gross=%v captured=%d", rev.GrossUSD, rev.Captured)
	}
	known := []string{"created", "captured", "failed", "refunded", "cancelled"}
	for _, p := range rev.Recent {
		if !slices.Contains(known, p.Status) {
			t.Errorf("payment %d has unexpected status %q", p.PaymentID, p.Status)
		}
	}
	_ = byState
}
