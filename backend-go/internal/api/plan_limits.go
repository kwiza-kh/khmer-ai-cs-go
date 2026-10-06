package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"khmer-ai-cs-go/internal/usage"
)

// The two plan limits that are a live count rather than a metered counter: how
// many channels a tenant may connect and how many agents it may seat. Messages
// and documents are consumed from a counter (usage.ConsumeMessageQuota and
// consumeDocQuota) because they are per-turn and per-upload; these are "how many
// exist right now", so they compare a count against the plan.
//
// Every number comes from usage.Plans — the same table the upgrade card renders —
// so a tenant can never be blocked by a limit they were not shown.
const (
	sqlTenantPlanName = "SELECT plan FROM tenant_billing WHERE user_id = $1"

	sqlCountActiveChannels = "SELECT count(*) FROM platform_configs WHERE user_id = $1 AND is_active = true"

	sqlCountActiveSeats = "SELECT count(*) FROM agent_teams WHERE owner_user_id = $1 AND is_active = true"
)

// querier is the slice of pgxpool.Pool / pgx.Tx the plan gates need. Invite
// acceptance re-checks the seat count inside its transaction (with the owner row
// locked), so the same SQL has to run on either.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// tenantPlan resolves the tenant's plan. A missing billing row means free: the
// row is provisioned lazily, and these gates must agree with what applyPlan
// would write for a tenant that has never been touched.
func (a *App) tenantPlan(ctx context.Context, userID int32) usage.Plan {
	return a.tenantPlanFrom(ctx, a.DB, userID)
}

func (a *App) tenantPlanFrom(ctx context.Context, q querier, userID int32) usage.Plan {
	var name string
	if err := q.QueryRow(ctx, sqlTenantPlanName, userID).Scan(&name); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			a.Logger.Warn("tenant plan lookup failed; treating as free", "user_id", userID, "error", err.Error())
		}
		return planByName(usage.PlanFree)
	}
	spec, ok := usage.PlanByName(name)
	if !ok {
		// An unknown plan is not "unlimited": fall back to the smallest tier.
		a.Logger.Warn("tenant carries an unknown plan; treating as free", "user_id", userID, "plan", name)
		return planByName(usage.PlanFree)
	}
	return spec
}

func planByName(name string) usage.Plan {
	spec, _ := usage.PlanByName(name)
	return spec
}

// checkChannelLimit refuses a new channel once the plan's allowance is in use.
func (a *App) checkChannelLimit(ctx context.Context, userID int32) error {
	spec := a.tenantPlan(ctx, userID)
	if spec.Channels >= usage.Unlimited {
		return nil
	}
	var used int64
	if err := a.DB.QueryRow(ctx, sqlCountActiveChannels, userID).Scan(&used); err != nil {
		// Counting is an auxiliary read: a failed count must not block connecting
		// a channel the tenant may well be entitled to (fail open, like the other
		// auxiliary paths — the money paths are the counters, and those fail loud).
		a.Logger.Warn("channel count failed; allowing the connection", "user_id", userID, "error", err.Error())
		return nil
	}
	if used >= spec.Channels {
		return &ApiError{http.StatusPaymentRequired,
			fmt.Sprintf("当前套餐（%s）最多连接 %d 个渠道，请升级套餐", spec.Name, spec.Channels)}
	}
	return nil
}

// checkSeatLimit refuses a new agent seat once the plan's allowance is in use.
func (a *App) checkSeatLimit(ctx context.Context, userID int32) error {
	return a.checkSeatLimitFrom(ctx, a.DB, userID)
}

func (a *App) checkSeatLimitFrom(ctx context.Context, q querier, userID int32) error {
	spec := a.tenantPlanFrom(ctx, q, userID)
	if spec.Seats >= usage.Unlimited {
		return nil
	}
	var used int64
	if err := q.QueryRow(ctx, sqlCountActiveSeats, userID).Scan(&used); err != nil {
		a.Logger.Warn("seat count failed; allowing the invite", "user_id", userID, "error", err.Error())
		return nil
	}
	if used >= spec.Seats {
		return &ApiError{http.StatusPaymentRequired,
			fmt.Sprintf("当前套餐（%s）最多 %d 个客服席位，请升级套餐", spec.Name, spec.Seats)}
	}
	return nil
}
