// Integration health, summarised for the operator.
//
// platform_connection_health already records per-config status ('connected',
// 'error', 'unknown'), and connectionCheckFailed is careful to write 'error' with
// the reason. What was missing is the roll-up a person can act on: which
// integrations are broken right now, oldest check first. Without it the only way
// to find out is to open every channel page — which is how a dead channel stays
// dead until a customer complains.
//
// Borrowed from AstrBot's failed_plugin_dict (star/star_manager.py: a failed
// integration is recorded and visible in the console instead of only in a log).
package platform

import (
	"context"
	"sort"
	"time"
)

// IntegrationRow is one row of platform_connection_health joined with its config.
type IntegrationRow struct {
	ConfigID    int32
	Platform    string
	AccountName string
	Status      string
	Detail      string
	CheckedAt   time.Time
}

// FailingIntegration is one broken channel in the roll-up.
type FailingIntegration struct {
	ConfigID    int32  `json:"config_id"`
	Platform    string `json:"platform"`
	AccountName string `json:"account_name"`
	Detail      string `json:"detail"`
	CheckedAt   string `json:"checked_at"`
	// StaleFor is how long the failure has been recorded, so an operator can
	// tell a blip from an outage that has lasted all night.
	StaleFor string `json:"stale_for"`
}

// IntegrationHealth is the roll-up.
type IntegrationHealth struct {
	Total     int `json:"total"`
	Connected int `json:"connected"`
	Errors    int `json:"errors"`
	// Unknown counts configs whose health was never checked (a channel created
	// but never used, or a webhook that never fired).
	Unknown int `json:"unknown"`
	// NeverChecked counts rows the health table has no entry for at all.
	NeverChecked int                  `json:"never_checked"`
	Failing      []FailingIntegration `json:"failing"`
}

// Healthy reports whether every channel is connected.
func (h IntegrationHealth) Healthy() bool {
	return h.Errors == 0 && h.Unknown == 0 && h.NeverChecked == 0
}

// SummariseIntegrations rolls up the health rows. Pure, so the counting and the
// ordering are testable without a database. configuredTotal lets the caller pass
// the number of configs that exist, so a channel with no health row can still be
// reported as never checked rather than silently missing.
func SummariseIntegrations(rows []IntegrationRow, configuredTotal int, now time.Time) IntegrationHealth {
	out := IntegrationHealth{Total: configuredTotal}
	seen := map[int32]bool{}
	for _, r := range rows {
		if seen[r.ConfigID] {
			continue // the health table is upserted per config; keep the first
		}
		seen[r.ConfigID] = true
		switch r.Status {
		case "connected":
			out.Connected++
		case "error":
			out.Errors++
			out.Failing = append(out.Failing, FailingIntegration{
				ConfigID:    r.ConfigID,
				Platform:    r.Platform,
				AccountName: r.AccountName,
				Detail:      r.Detail,
				CheckedAt:   r.CheckedAt.UTC().Format(time.RFC3339),
				StaleFor:    humanDuration(now.Sub(r.CheckedAt)),
			})
		default:
			out.Unknown++
		}
	}
	if out.Total < len(seen) {
		// A health row for a config the caller did not count still exists.
		out.Total = len(seen)
	}
	out.NeverChecked = out.Total - len(seen)
	if out.NeverChecked < 0 {
		out.NeverChecked = 0
	}
	// Oldest failure first: the thing that has been broken longest is the thing
	// to fix first.
	sort.SliceStable(out.Failing, func(i, j int) bool {
		return out.Failing[i].CheckedAt < out.Failing[j].CheckedAt
	})
	return out
}

// humanDuration renders a coarse interval ("3h", "12m", "45s") for the console.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return itoa(int(d/(24*time.Hour))) + "d"
	case d >= time.Hour:
		return itoa(int(d/time.Hour)) + "h"
	case d >= time.Minute:
		return itoa(int(d/time.Minute)) + "m"
	default:
		return itoa(int(d/time.Second)) + "s"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// IntegrationHealthFor reads the roll-up for one tenant (userID nil = every
// tenant, for the operator console).
func (p *Pipeline) IntegrationHealthFor(ctx context.Context, userID *int32) (IntegrationHealth, error) {
	if p == nil || p.DB == nil {
		return IntegrationHealth{}, nil
	}
	const q = "SELECT c.config_id, c.platform::text, COALESCE(h.account_name,''), COALESCE(h.status,'unknown'), " +
		"COALESCE(h.detail,''), COALESCE(h.checked_at, NOW()) " +
		"FROM platform_configs c LEFT JOIN platform_connection_health h ON h.config_id = c.config_id " +
		"WHERE ($1::int IS NULL OR c.user_id = $1) AND c.is_active"
	rows, err := p.DB.Query(ctx, q, userID)
	if err != nil {
		return IntegrationHealth{}, err
	}
	defer rows.Close()
	var (
		out   []IntegrationRow
		total int
	)
	for rows.Next() {
		var r IntegrationRow
		if err := rows.Scan(&r.ConfigID, &r.Platform, &r.AccountName, &r.Status, &r.Detail, &r.CheckedAt); err != nil {
			return IntegrationHealth{}, err
		}
		out = append(out, r)
		total++
	}
	if err := rows.Err(); err != nil {
		return IntegrationHealth{}, err
	}
	return SummariseIntegrations(out, total, time.Now()), nil
}
