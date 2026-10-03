package platform

import (
	"testing"
	"time"
)

// The roll-up is the operator's answer to "which channel is broken right now", so
// the counting and the ordering are the whole feature.

func TestSummariseIntegrationsCountsAndOrders(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	rows := []IntegrationRow{
		{ConfigID: 1, Platform: "telegram", AccountName: "@shop", Status: "connected", CheckedAt: now.Add(-time.Minute)},
		{ConfigID: 2, Platform: "line", Status: "error", Detail: "401 invalid token", CheckedAt: now.Add(-3 * time.Hour)},
		{ConfigID: 3, Platform: "whatsapp", Status: "error", Detail: "token expired", CheckedAt: now.Add(-30 * time.Minute)},
		{ConfigID: 4, Platform: "meta", Status: "unknown", CheckedAt: now.Add(-time.Hour)},
	}
	h := SummariseIntegrations(rows, 4, now)

	if h.Total != 4 || h.Connected != 1 || h.Errors != 2 || h.Unknown != 1 {
		t.Fatalf("counts = %+v", h)
	}
	if h.Healthy() {
		t.Fatal("two erroring channels is not healthy")
	}
	if len(h.Failing) != 2 {
		t.Fatalf("failing = %+v", h.Failing)
	}
	// Oldest failure first: the longest-broken channel is the one to fix.
	if h.Failing[0].ConfigID != 2 || h.Failing[1].ConfigID != 3 {
		t.Fatalf("order = %d,%d; want the oldest check first", h.Failing[0].ConfigID, h.Failing[1].ConfigID)
	}
	if h.Failing[0].StaleFor != "3h" || h.Failing[1].StaleFor != "30m" {
		t.Fatalf("stale durations = %q/%q", h.Failing[0].StaleFor, h.Failing[1].StaleFor)
	}
	if h.Failing[1].Detail != "token expired" {
		t.Fatalf("detail was dropped: %+v", h.Failing[1])
	}
	if h.Failing[0].CheckedAt == "" {
		t.Fatal("checked_at must be rendered")
	}
}

func TestSummariseIntegrationsReportsNeverCheckedConfigs(t *testing.T) {
	now := time.Now()
	// Four channels exist, only one has a health row.
	h := SummariseIntegrations([]IntegrationRow{{ConfigID: 1, Status: "connected", CheckedAt: now}}, 4, now)
	if h.NeverChecked != 3 {
		t.Fatalf("never checked = %d, want 3", h.NeverChecked)
	}
	if h.Healthy() {
		t.Fatal("a channel that was never checked is not healthy")
	}
}

func TestSummariseIntegrationsAllHealthy(t *testing.T) {
	now := time.Now()
	h := SummariseIntegrations([]IntegrationRow{
		{ConfigID: 1, Status: "connected", CheckedAt: now},
		{ConfigID: 2, Status: "connected", CheckedAt: now},
	}, 2, now)
	if !h.Healthy() || h.Errors != 0 || h.Failing != nil {
		t.Fatalf("healthy roll-up = %+v", h)
	}
}

func TestSummariseIntegrationsDedupesByConfig(t *testing.T) {
	now := time.Now()
	rows := []IntegrationRow{
		{ConfigID: 9, Status: "error", Detail: "first", CheckedAt: now},
		{ConfigID: 9, Status: "error", Detail: "second", CheckedAt: now},
	}
	h := SummariseIntegrations(rows, 1, now)
	if h.Errors != 1 || len(h.Failing) != 1 || h.Failing[0].Detail != "first" {
		t.Fatalf("roll-up = %+v", h)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second: "45s",
		12 * time.Minute: "12m",
		3 * time.Hour:    "3h",
		50 * time.Hour:   "2d",
		-5 * time.Second: "0s",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestIntegrationHealthWithoutDatabaseIsSafe(t *testing.T) {
	var p *Pipeline
	if h, err := p.IntegrationHealthFor(nil, nil); err != nil || h.Total != 0 {
		t.Fatalf("nil pipeline = %+v/%v", h, err)
	}
}
