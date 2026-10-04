package platform

import (
	"context"
	"strconv"
	"sync"
	"time"

	"khmer-ai-cs-go/internal/textutil"
	"khmer-ai-cs-go/internal/typesafe"
)

// Jev health alerting.
//
// Every Jev call site degrades to a slower path when Jev fails, which means an
// outage changes product behaviour without anyone noticing: turns that time out
// are decided by the fast model, and the fast model over-escalates. Measured on
// 2026-09-22, one such episode ran from 17:03 to 23:11 and was visible only as
// a handful of guard warnings in journalctl.
//
// The observer below is installed on the typesafe client and rides the existing
// PlatformAlert channel (Redis-deduped, 15 min per key), so an outage pages
// once and a recovery clears the key.

const (
	// jevDownAfter consecutive failures before paging. The keep-warm probe
	// fires every JEV_KEEPWARM_SEC (45s default), so 3 puts the page about
	// 2.5 minutes into an outage — fast enough to act on, slow enough that one
	// dropped connection does not page.
	jevDownAfter = 3

	// jevSlowAfter is the latency above which a SUCCESSFUL call is still
	// treated as degradation. The reply-path budgets are 3s (guard) and 4s
	// (route); a call slower than this is reachable but useless to them, and
	// produces exactly the same silent fallback as an error.
	jevSlowAfter = 3 * time.Second

	// jevSlowPageEvery throttles the slow-path page independently of the down
	// page, so a permanently slow Jev cannot mask a later outage.
	jevSlowPageEvery = 15 * time.Minute
)

// jevHealth implements typesafe.HealthObserver.
//
// The observer's methods run inline on whatever goroutine called Judge, so
// nothing here may block: the real page is handed to the critical background
// lane by the injected alert func.
type jevHealth struct {
	// alert and resolve are injected so the state machine is unit-testable
	// without Redis or a Telegram bot.
	alert   func(key, title, detail string)
	resolve func(key string)

	mu       sync.Mutex
	consec   int
	down     bool
	lastSlow time.Time
}

func newJevHealth(p *Pipeline) *jevHealth {
	return &jevHealth{
		alert: func(key, title, detail string) {
			SpawnCritical(func() {
				p.PlatformAlert(context.Background(), key, title, detail)
			})
		},
		resolve: func(key string) {
			SpawnCritical(func() {
				p.PlatformAlertResolved(context.Background(), key)
			})
		},
	}
}

// JudgeFailed implements typesafe.HealthObserver.
func (h *jevHealth) JudgeFailed(err error) {
	h.mu.Lock()
	h.consec++
	becameDown := !h.down && h.consec >= jevDownAfter
	if becameDown {
		h.down = true
	}
	fails := h.consec
	h.mu.Unlock()

	if !becameDown {
		return
	}
	detail := "Jev 连续 " + strconv.Itoa(fails) + " 次调用失败，客户回合正在静默回落到快模型" +
		"（实测 Jev 354ms vs 快模型 3234ms，且快模型过度升级）。"
	if err != nil {
		detail += "\n最后错误: " + textutil.Ellipsize(err.Error(), 200)
	}
	h.alert("jev-down", "Jev 不可用", detail)
}

// JudgeSucceeded implements typesafe.HealthObserver.
func (h *jevHealth) JudgeSucceeded(took time.Duration) {
	h.mu.Lock()
	wasDown := h.down
	h.down = false
	h.consec = 0
	// The slow path is throttled here rather than by PlatformAlert's dedup so a
	// down/recovered flap cannot reset the slow timer.
	pageSlow := false
	if took >= jevSlowAfter && time.Since(h.lastSlow) >= jevSlowPageEvery {
		h.lastSlow = time.Now()
		pageSlow = true
	}
	h.mu.Unlock()

	if wasDown {
		h.resolve("jev-down")
		h.alert("jev-recovered", "Jev 已恢复", "Jev 调用重新成功，客户回合恢复走 Jev 判定。")
	}
	if pageSlow {
		h.alert("jev-slow", "Jev 响应过慢",
			"Jev 调用成功但耗时 "+took.Round(time.Millisecond).String()+
				"，已超过 reply-path 预算（guard 3s / route 4s）——"+
				"这类回合同样会静默回落到快模型。检查 api.typesafe.ai 与保活探针。")
	}
}

// InstallJevHealth wires Jev health alerting onto the client. No-op when Jev is
// disabled (nil client).
func InstallJevHealth(p *Pipeline) {
	if p == nil || !p.Jev.Enabled() {
		return
	}
	p.Jev.SetHealthObserver(newJevHealth(p))
}

// ensure the interface is satisfied at compile time.
var _ typesafe.HealthObserver = (*jevHealth)(nil)
