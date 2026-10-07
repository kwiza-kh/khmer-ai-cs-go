package api

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
)

// The digest is the only thing that pushes operational state at the operator, so
// two rules matter: it stays silent when nothing needs attention (a daily "all
// zeroes" trains the operator to ignore it), and it never drops a counter that
// does — a payment about to lapse is exactly as urgent as a waiting merchant.
func TestFormatDigest(t *testing.T) {
	if title, detail, send := formatDigest(todoCounts{}); send || title != "" || detail != "" {
		t.Fatalf("an all-zero digest must not be sent: %q / %q", title, detail)
	}
	// Queued sends drain on their own: not worth a page.
	if _, _, send := formatDigest(todoCounts{OutboxBacklog: 7}); send {
		t.Error("a backlog-only digest must not be sent")
	}

	cases := []struct {
		name   string
		counts todoCounts
		want   []string
	}{
		{"waiting merchants", todoCounts{SupportOpen: 2}, []string{"商家消息待回复：2"}},
		{"broken channel", todoCounts{ChannelErrors: 1}, []string{"渠道异常：1"}},
		{"failed sends", todoCounts{OutboxFailed: 3}, []string{"发送失败（重试已耗尽）：3"}},
		{"lapse", todoCounts{ExpiringPaid: 1}, []string{"付费即将/已过期（14 天内）：1"}},
		{"quota", todoCounts{QuotaPressure: 4}, []string{"额度已用 >80%：4"}},
		{
			// Every counter present: none may be dropped on the way out.
			"everything",
			todoCounts{SupportOpen: 1, ChannelErrors: 1, OutboxFailed: 1, ExpiringPaid: 1, QuotaPressure: 1},
			[]string{"商家消息待回复：1", "渠道异常：1", "发送失败（重试已耗尽）：1", "付费即将/已过期（14 天内）：1", "额度已用 >80%：1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, detail, send := formatDigest(tc.counts)
			if !send {
				t.Fatalf("expected a digest for %+v", tc.counts)
			}
			if title == "" {
				t.Error("missing title")
			}
			for _, want := range tc.want {
				if !strings.Contains(detail, want) {
					t.Errorf("detail %q is missing %q", detail, want)
				}
			}
		})
	}
}

// The push has to agree with the card the operator opens next: both read the same
// query, and this pins that against the live schema (the digest is what they act
// on before ever looking at the console).
//
// Read-only; gated on DATABASE_URL.
func TestTodoCountsMatchTheCard(t *testing.T) {
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

	direct, err := app.todoCounts(context.Background())
	if err != nil {
		t.Fatalf("todoCounts: %v", err)
	}
	var card struct {
		SupportOpen   int64 `json:"support_open"`
		ChannelErrors int64 `json:"channel_errors"`
		OutboxFailed  int64 `json:"outbox_failed"`
		OutboxBacklog int64 `json:"outbox_backlog"`
		ExpiringPaid  int64 `json:"expiring_paid"`
		QuotaPressure int64 `json:"quota_pressure"`
	}
	opsCall(t, app.platformTodo, "/api/v1/platform/todo", &card)

	if direct.SupportOpen != card.SupportOpen ||
		direct.ChannelErrors != card.ChannelErrors ||
		direct.OutboxFailed != card.OutboxFailed ||
		direct.OutboxBacklog != card.OutboxBacklog ||
		direct.ExpiringPaid != card.ExpiringPaid ||
		direct.QuotaPressure != card.QuotaPressure {
		t.Fatalf("digest counts %+v != card %+v", direct, card)
	}
	// And the pushed text uses those same numbers.
	if _, detail, send := formatDigest(direct); send && direct.SupportOpen > 0 {
		if !strings.Contains(detail, "商家消息待回复：") {
			t.Errorf("digest text omits a non-zero counter: %q", detail)
		}
	}
}
