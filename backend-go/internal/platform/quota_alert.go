package platform

import (
	"context"
	"strconv"

	"khmer-ai-cs-go/internal/textutil"
)

// Gemini quota and spend alerting.
//
// Both failures are quiet by construction: the reply path degrades into an
// escalation instead of an answer, which is the right behaviour for the
// customer but leaves the operator with nothing to see. PlatformAlert pages
// once per key (Redis-deduped, 15 minutes).

// IsQuotaExhausted reports whether a Gemini error means "stop, not retry" —
// see isQuotaExhausted for the rate-limit-versus-balance reasoning. Exported
// so the authenticated chat handlers, which do not go through this pipeline,
// can page the same condition.
func IsQuotaExhausted(err error) bool { return isQuotaExhausted(err) }

// AlertQuotaExhausted pages the operator when generation stopped on quota.
func (p *Pipeline) AlertQuotaExhausted(ctx context.Context, err error) {
	detail := "AI 回复已停止，回合已转人工。检查 Google AI Studio 的配额与预付款余额。"
	if err != nil {
		detail += "\n" + textutil.TruncateRunes(err.Error(), 300)
	}
	p.PlatformAlert(ctx, "gemini-quota", "AI 配额/余额耗尽", detail)
}

// AlertSpendGate pages when the rolling spend crossed the local gate: the
// operator sees the number before Google's own 429 delivers it as a customer-
// visible error.
func (p *Pipeline) AlertSpendGate(ctx context.Context, spent, limit float64) {
	p.PlatformAlert(ctx, "gemini-spend-gate", "AI 消费速率接近上限",
		"最近 10 分钟消费 $"+strconv.FormatFloat(spent, 'f', 3, 64)+
			"，闸值 $"+strconv.FormatFloat(limit, 'f', 2, 64)+
			"。AI 回合已主动转人工，避免撞上 429 后客户看到错误；升档或降载后自动恢复。")
}
