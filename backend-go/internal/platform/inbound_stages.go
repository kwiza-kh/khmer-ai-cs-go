// The inbound pipeline, one stage per decision.
//
// processInboundEvent used to be a single 343-line function in which the
// short-circuit points were bare `return nil` statements scattered between
// database writes and an LLM call: "the customer asked for a human", "Jev says
// this is junk", "the spend gate tripped", "the reply promised a handoff". Adding
// a condition meant re-reading the whole function to find out what it skipped,
// and the per-message state lived in ~25 locals that any future edit could
// accidentally share.
//
// The shape below is data-driven in the same way the channel layer is
// (capabilities.go): a stage is a name plus a function over ONE turn's state, the
// driver runs them in order, and a stage that returns next=false ends the turn
// deliberately. That mirrors AstrBot's stage chain
// (astrbot/core/pipeline/scheduler.py: a stage returns None to stop the event, an
// error to abort it) without copying its async-generator protocol — Go has no
// equivalent, and a `next bool` plus `defer` expresses the same intent without a
// reflective scheduler.
//
// Per-tenant state lives in inboundTurn, which is created per event and dropped
// when the turn ends. Nothing about a turn is stored on Pipeline, so two tenants
// being served concurrently cannot see each other's session, reply or routing
// decision. (AstrBot does the same thing at a coarser grain: one pipeline context
// and scheduler per configuration file, core_lifecycle.py:456-470.)
package platform

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/persona"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/usage"
)

// inboundTurn is the state of one customer message as it travels the stages.
// Every field is written by exactly one stage and read by the ones after it.
type inboundTurn struct {
	// Event is the queued inbound row. Display name and content are updated in
	// place because later stages persist them.
	Event *InboundEvent

	// --- set by load-config / resolve-profile / prepare-media ---
	Config        *configCred
	Content       string
	Avatar        string
	MediaURL      string
	PlatformMedia map[string]any

	// --- set by ensure-session / release-handoff ---
	SessionID     string
	UserMessageID int64
	SessionIsNew  bool
	SessionStatus string

	// --- set by resolve-persona ---
	// Persona, when non-nil, replaces the per-tenant system prompt for this
	// turn. Nil is the normal case (no binding) and every stage below must
	// behave as it did before personas existed.
	Persona *persona.Persona

	// --- set by prepare-grounding / route-inbound ---
	ReplyLang  string
	History    []gemini.HistoryItem
	GroundCh   chan rag.GroundingContext
	SkipGround bool
	// Urgency is the router's verdict for this turn ("" = unknown, the same
	// spelling routing.go uses).
	Urgency string

	// --- set by generate / guard-reply / after-hours ---
	Reply  string
	Canned bool
	// CannedLabel is the model_name recorded for a canned reply (empty means
	// "smalltalk-template"), so the console can tell a quota notice from small talk.
	CannedLabel string
	// HandoffReason overrides the generic escalation reason when a stage knows
	// why the turn is being handed over (the quota gate does).
	HandoffReason string
	Result        gemini.ChatResult
	GroundCtx     rag.GroundingContext
	// MemorySummary is the rolling note covering the turns older than
	// historyWindow (migration 083). "" while the window still holds everything.
	MemorySummary string
	// StageTimings collects the stages that cost real time (>= stageTimingFloor),
	// recorded by runInboundStages and logged once per turn.
	StageTimings []StageTiming
	// AttachMediaKey is the picture of the document the answer grounded on
	// (attach-doc-image stage). "" = deliver text only; persist-and-deliver
	// resolves it to a URL and turns the payload into a media message.
	AttachMediaKey string
	ClaimsHandoff  bool
}

// inboundStage is one step of the turn. next=false means the turn is over
// (answered, escalated or deliberately ignored) and the remaining stages are
// skipped; err aborts the turn and is returned to the queue so the event can be
// retried.
type inboundStage struct {
	Name string
	Run  func(ctx context.Context, t *inboundTurn) (next bool, err error)
}

// inboundStages is the ordered turn pipeline. A stage that does not apply
// (Jev disabled, TTS off, cache empty) returns next=true unchanged rather than
// being dropped from the list, so the order is a property of the deployment and
// not of a single message.
func (p *Pipeline) inboundStages() []inboundStage {
	return []inboundStage{
		{Name: "load-config", Run: p.stageLoadConfig},
		{Name: "resolve-profile", Run: p.stageResolveProfile},
		{Name: "prepare-media", Run: p.stagePrepareMedia},
		{Name: "ensure-session", Run: p.stageEnsureSession},
		{Name: "resolve-persona", Run: p.stageResolvePersona},
		{Name: "reopen-finished-session", Run: p.stageReopenFinishedSession},
		{Name: "release-handoff", Run: p.stageReleaseHandoff},
		{Name: "escalation-gate", Run: p.stageEscalationGate},
		{Name: "keyword-handoff", Run: p.stageKeywordHandoff},
		{Name: "bill-message", Run: p.stageBillMessage},
		{Name: "screen-inbound", Run: p.stageScreenInbound},
		{Name: "prepare-grounding", Run: p.stagePrepareGrounding},
		{Name: "route-inbound", Run: p.stageRouteInbound},
		{Name: "notify-owner", Run: p.stageNotifyOwner},
		{Name: "generate", Run: p.stageGenerate},
		{Name: "guard-reply", Run: p.stageGuardReply},
		{Name: "screen-reply", Run: p.stageScreenReply},
		{Name: "after-hours-preamble", Run: p.stageAfterHours},
		{Name: "handoff-reply", Run: p.stageHandoffReply},
		// After handoff-reply (the last stage that may rewrite the reply) and
		// before delivery: only the final text is worth attaching a picture to.
		{Name: "attach-doc-image", Run: p.stageAttachDocImage},
		{Name: "persist-and-deliver", Run: p.stagePersistAndDeliver},
		{Name: "post-delivery", Run: p.stagePostDelivery},
	}
}

// StageTiming is one stage's wall time inside a turn. Recorded for stages that
// cost something (>= stageTimingFloor) so the per-turn timing line stays short.
type StageTiming struct {
	Name string
	Dur  time.Duration
}

// stageTimingFloor keeps the timing line to stages a person would notice. A turn
// of a dozen stages that each cost 5ms has nothing to say.
const stageTimingFloor = 250 * time.Millisecond

// formatStageTimings renders the slowest stages, biggest first.
func formatStageTimings(timings []StageTiming, max int) string {
	if len(timings) == 0 {
		return "-"
	}
	sorted := append([]StageTiming(nil), timings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Dur > sorted[j].Dur })
	if len(sorted) > max {
		sorted = sorted[:max]
	}
	parts := make([]string, 0, len(sorted))
	for _, s := range sorted {
		parts = append(parts, fmt.Sprintf("%s=%.1fs", s.Name, s.Dur.Seconds()))
	}
	return strings.Join(parts, " ")
}

// runInboundStages drives one turn. Kept as a free function so the driver's
// semantics (order, short-circuit, error) are testable with arbitrary stages and
// no database.
func runInboundStages(ctx context.Context, stages []inboundStage, t *inboundTurn) error {
	for _, st := range stages {
		start := time.Now()
		next, err := st.Run(ctx, t)
		if d := time.Since(start); d >= stageTimingFloor {
			t.StageTimings = append(t.StageTimings, StageTiming{Name: st.Name, Dur: d})
		}
		if err != nil {
			return err
		}
		if !next {
			return nil
		}
	}
	return nil
}

// load-config — everything downstream needs the decrypted credentials.
func (p *Pipeline) stageLoadConfig(ctx context.Context, t *inboundTurn) (bool, error) {
	cfg, err := p.loadConfig(ctx, t.Event.ConfigID)
	if err != nil {
		return false, err
	}
	t.Config = cfg
	return true, nil
}

// resolve-profile — display name + avatar, best effort, never blocks.
//
// Most channels only need the profile when we do not already have a display name.
// A channel whose Capabilities say AvatarNeedsRehost is asked every time: its
// avatar is not a URL a browser can load (Telegram hands out a file id), so it is
// downloaded and re-hosted, and that rehosted URL can expire.
func (p *Pipeline) stageResolveProfile(ctx context.Context, t *inboundTurn) (bool, error) {
	ev, cfg := t.Event, t.Config
	rehostAvatar := CapabilitiesFor(cfg.Platform).AvatarNeedsRehost
	if ch, chErr := NewChannel(p, cfg); chErr == nil && (ev.UserDisplayName == "" || rehostAvatar) {
		if name, pic, perr := ch.Profile(ctx, ev.PlatformUserID); perr == nil {
			if ev.UserDisplayName == "" {
				ev.UserDisplayName = name
			}
			if rehostAvatar {
				if pic != "" && p.customerAvatar(ctx, cfg, ev.PlatformUserID) == "" {
					t.Avatar = p.storeTelegramAvatar(ctx, cfg, ev.PlatformUserID, pic)
				}
			} else if pic != "" {
				t.Avatar = pic
			}
		}
	}
	return true, nil
}

// prepare-media — voice → transcribe, images → describe, files → store.
func (p *Pipeline) stagePrepareMedia(ctx context.Context, t *inboundTurn) (bool, error) {
	if t.Event.Media != nil {
		t.Content, t.MediaURL, t.PlatformMedia = p.prepareMedia(ctx, t.Event, t.Config, t.Content)
		// A photo or voice note we could not read leaves the turn with no words
		// at all. Generating from empty content means answering a question the
		// customer never asked (the model fills the silence), so the turn goes to
		// a human: the failure is already recorded in
		// platform_media.extract_failed, and now the customer gets a promise of a
		// person instead of an invention.
		if strings.TrimSpace(t.Content) == "" {
			if failed, _ := t.PlatformMedia["extract_failed"].(string); failed != "" {
				t.handOffWithoutReply("media-unreadable", "Inbound media could not be read ("+failed+"); customer handed to a human instead of answered")
				if p.Logger != nil {
					p.Logger.Warn("inbound media unreadable; handing off instead of answering an empty message",
						"event_id", t.Event.EventID, "reason", failed)
				}
			}
		}
	}
	return true, nil
}

// ensure-session — create or resume the session and persist the customer message.
func (p *Pipeline) stageEnsureSession(ctx context.Context, t *inboundTurn) (bool, error) {
	sessionID, userMessageID, isNew, sessionStatus, err := p.ensureSession(
		ctx, t.Event, t.Config, t.Content, t.Avatar, t.MediaURL, t.PlatformMedia)
	if err != nil {
		return false, err
	}
	t.SessionID, t.UserMessageID, t.SessionIsNew, t.SessionStatus = sessionID, userMessageID, isNew, sessionStatus
	return true, nil
}

// resolve-persona — a session- or conversation-bound persona (migration 067)
// replaces the tenant's system prompt for this turn.
//
// It runs right after ensure-session because the session id is what the session
// scope binds to, and it must run before cache-lookup: the reply cache is keyed
// on tenant+question+language with no persona dimension, so an answer cached
// under the tenant prompt must not be served on a persona turn, and a persona's
// answer must not be stored for the tenant prompt to serve later.
//
// No binding is the normal case, and a failed lookup is not worth aborting a
// customer's turn over: both leave the turn on the per-tenant prompt, which is
// exactly what every deployment ran before this stage existed.
func (p *Pipeline) stageResolvePersona(ctx context.Context, t *inboundTurn) (bool, error) {
	// conversationID is "": this platform has no conversation entity above
	// sessions — 001_init.sql gives one sessions row per customer thread — so the
	// session scope is the narrowest binding that can exist today.
	// persona.Resolve walks session → conversation → global, so the conversation
	// scope starts matching the day a conversation id exists, with no change here.
	per, ok, err := persona.NewStore(p.DB).ForTurn(ctx, t.Config.UserID, t.SessionID, "")
	if err != nil {
		p.Logger.Warn("persona lookup failed; answering with the tenant system prompt",
			"session_id", t.SessionID, "error", err.Error())
		return true, nil
	}
	if ok {
		t.Persona = &per
	}
	return true, nil
}

// personaHistory turns a persona's begin_dialogs into the turns that lead the
// history.
//
// 067_personas.sql stores them as a plain JSON array of strings with no roles
// (the shape AstrBot's begin_dialogs has), so the only reading available is
// positional: the customer's opening line first, then the persona's, alternating.
// Starting on 'user' is what keeps the block from being dropped wholesale —
// RepairHistoryShape (gemini.go) discards every leading non-user turn.
func personaHistory(dialogs []string) []gemini.HistoryItem {
	out := make([]gemini.HistoryItem, 0, len(dialogs))
	for i, d := range dialogs {
		role := "user"
		if i%2 == 1 {
			role = "model"
		}
		out = append(out, gemini.HistoryItem{Role: role, Content: d})
	}
	return out
}

// reopen-finished-session — a customer replying on a resolved/closed session
// reopens it, so they always get an answer.
func (p *Pipeline) stageReopenFinishedSession(ctx context.Context, t *inboundTurn) (bool, error) {
	if t.SessionIsNew && (t.SessionStatus == "resolved" || t.SessionStatus == "closed") {
		_, _ = p.DB.Exec(ctx, "UPDATE sessions SET status='active', resolved_at=NULL, closed_at=NULL WHERE session_id=$1", t.SessionID)
		t.SessionStatus = "active"
		p.Logger.Info("customer replied on a finished session; reopened", "session_id", t.SessionID)
	}
	return true, nil
}

// release-handoff — the customer cancels ("不需要人工") or every open request is
// already resolved → hand the session back to the AI.
func (p *Pipeline) stageReleaseHandoff(ctx context.Context, t *inboundTurn) (bool, error) {
	if t.SessionStatus == "handoff" && p.maybeReleaseHandoff(ctx, t.Config, t.SessionID, t.Content) {
		t.SessionStatus = "active"
	}
	return true, nil
}

// escalation-gate — a non-active session gets the canned ack once per
// escalation, not an AI reply.
func (p *Pipeline) stageEscalationGate(ctx context.Context, t *inboundTurn) (bool, error) {
	if t.SessionStatus != "active" {
		p.ackHandoffOnce(ctx, t.Event, t.Config, t.SessionID)
		return false, nil
	}
	return true, nil
}

// keyword-handoff — the customer explicitly asked for a human. The AI stays
// silent and the conversation lands in the handoff queue; the 🔔 ping from
// escalateToHuman covers this action, which is why notify-owner deliberately
// skips it (the 💬 message ping would double up).
func (p *Pipeline) stageKeywordHandoff(ctx context.Context, t *inboundTurn) (bool, error) {
	matched, ok := humanRequestKeyword(t.Content)
	if !ok {
		return true, nil
	}
	p.Logger.Info("auto handoff: customer requested a human", "session_id", t.SessionID, "keyword", matched)
	p.escalateToHuman(ctx, t.Event, t.Config, t.SessionID, "customer_request",
		"Customer asked for a human agent (matched: "+matched+")", "")
	return false, nil
}

// bill-message — consume one message from the tenant's monthly quota. This is
// the enforcement point that was missing: the counter used to advance
// asynchronously and let every turn through, which left the plans in applyPlan
// unenforceable.
//
// It runs before retrieval and generation, so an exhausted tenant stops costing
// money the moment the cap is hit; and it counts every received customer message
// regardless of routing outcome, because junk silence is a service decision, not
// a free-usage one.
//
// Exhaustion answers with the handoff acknowledgement instead of a model reply:
// the customer keeps a human path, and the owner gets the handoff request that
// says the plan is what stopped the answers. A billing failure returns the error
// so the event is retried rather than answered for free.
func (p *Pipeline) stageBillMessage(ctx context.Context, t *inboundTurn) (bool, error) {
	userID := t.Config.UserID
	err := p.chargeInboundTurn(ctx, userID, t.Event.EventID)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, usage.ErrMessageQuotaExhausted) {
		return false, err
	}
	t.ReplyLang = p.turnReplyLang(ctx, userID, t.Content)
	t.handOffWithoutReply("quota-notice", "Tenant message quota exhausted; customer handed to a human instead of answered")
	p.Logger.Warn("tenant message quota exhausted; handing off instead of answering",
		"user_id", userID, "session_id", t.SessionID)
	return true, nil
}

// turnReplyLang resolves the language a customer-facing reply must use: the
// merchant's saved preference, else the script of the customer's own message,
// else Khmer. Shared by prepare-grounding and the quota notice so both speak to
// the customer in the same language.
func (p *Pipeline) turnReplyLang(ctx context.Context, userID int32, content string) string {
	if lang := p.ownerLanguage(ctx, userID); lang != "" {
		return lang
	}
	if det := gemini.DetectLanguage(content); det != "" {
		return det
	}
	return "km"
}

// prepare-grounding — reply language, history, and the speculative retrieval.
//
// Retrieval is the slowest pre-generation step (embedding + search + rerank,
// ~2.5s) and it does not depend on the routing decision — only on whether we keep
// its result. Starting it here lets it run concurrently with Jev's routing call
// instead of being purely additive to every grounded turn.
func (p *Pipeline) stagePrepareGrounding(ctx context.Context, t *inboundTurn) (bool, error) {
	replyLang := p.turnReplyLang(ctx, t.Config.UserID, t.Content)
	t.ReplyLang = replyLang
	t.History = p.loadHistory(ctx, t.SessionID, t.UserMessageID)
	// What the 20-message window cannot see. Empty for short threads, a labelled
	// note once the conversation outgrows it (migration 083, session_memory.go).
	t.MemorySummary = p.loadSessionMemory(ctx, t.SessionID)

	// Copy what the worker needs: it outlives this turn's struct, and reading the
	// turn from another goroutine is exactly the sharing this refactor exists to
	// avoid.
	groundCh := make(chan rag.GroundingContext, 1)
	t.GroundCh = groundCh
	userID, sessionID, content, history := t.Config.UserID, t.SessionID, t.Content, t.History
	go func() {
		res := rag.GroundingContext{}
		func() {
			defer func() {
				if r := recover(); r != nil {
					p.Logger.Warn("background retrieval panic recovered", "panic", r)
				}
			}()
			res = p.RAG.Ground(ctx, userID, &sessionID, content, replyLang, history, 0)
		}()
		groundCh <- res // exactly one send, so the channel never leaks
	}()
	return true, nil
}

// route-inbound — one Jev batch decides whether this turn needs a human, needs no
// retrieval at all, is junk that deserves silence, or takes the full grounded
// path — plus how urgent it is (drives handoff priority). An unknown route (Jev
// off/slow/unsure) keeps the previous behaviour untouched.
func (p *Pipeline) stageRouteInbound(ctx context.Context, t *inboundTurn) (bool, error) {
	r, ok := p.RouteInbound(ctx, t.Content)
	if !ok {
		return true, nil
	}
	escalate, skip, silent := routeDecision(r.Route, r.Prob, t.Content)
	if escalate {
		p.Logger.Info("auto handoff: jev routed the message to a human",
			"session_id", t.SessionID, "p", r.Prob, "urgency", r.Urgency)
		p.escalateToHuman(ctx, t.Event, t.Config, t.SessionID, "customer_request",
			"Jev routed the message as a human request (p="+strconv.FormatFloat(r.Prob, 'f', 2, 64)+")", r.Urgency)
		return false, nil
	}
	if silent {
		// Junk: spam/ads/gibberish. No reply, no generation, no delivery — and
		// because the owner ping and the typing indicator live in the next stage,
		// junk silence is complete: no ping with no reply behind it, no typing
		// that promises an answer. The customer message itself stays in the inbox
		// for the owner. The speculative retrieval above is abandoned; the
		// buffered channel lets that worker finish without blocking.
		p.Logger.Info("junk dropped: jev routed the message as no-reply",
			"session_id", t.SessionID, "p", r.Prob)
		return false, nil
	}
	if skip && LooksLikeQuestion(t.Content) {
		// See the veto in routeDecision: the router called this pleasantries, the
		// message is a question, so take the grounded path and say why in the log —
		// this is a router misclassification the operator should be able to count.
		p.Logger.Info("chit-chat shortcut refused: the message is a question",
			"session_id", t.SessionID, "p", r.Prob)
		skip = false
	}
	t.SkipGround = skip
	t.Urgency = r.Urgency
	return true, nil
}

// notify-owner — ping the owner's bot about a new customer message (background,
// throttled per session) and show the typing indicator.
//
// Both live after routing on purpose: the keyword/Jev escalations above already
// sent the single 🔔 notification, and junk must not ping or promise a typing
// indicator for an answer that is never coming.
func (p *Pipeline) stageNotifyOwner(ctx context.Context, t *inboundTurn) (bool, error) {
	userID, sessionID, platform, name, content := t.Config.UserID, t.SessionID, t.Event.Platform, t.Event.UserDisplayName, t.Content
	SpawnCritical(func() {
		p.NotifyNewCustomerMessage(ctx, userID, sessionID, platform, name, content)
	})
	// The typing indicator is a provider API round-trip (~0.5–0.9s measured on
	// Messenger) and purely cosmetic: the customer sees dots while the model
	// works. Sending it inline made every turn pay that round-trip before
	// generation even started (turn timing, 2026-10-16).
	recipient := t.Event.PlatformUserID
	cfg := t.Config
	runTyping := func() { p.sendTyping(ctx, cfg, recipient) }
	go func() {
		defer func() {
			if r := recover(); r != nil && p.Logger != nil {
				p.Logger.Warn("typing indicator panicked", "panic", fmt.Sprint(r))
			}
		}()
		runTyping()
	}()
	return true, nil
}

// generate — collect the grounding, apply the spend gate, call the model.
func (p *Pipeline) stageGenerate(ctx context.Context, t *inboundTurn) (bool, error) {
	if t.SkipGround {
		// Small talk: Jev was confident the message carries no request, so answer
		// from the fixed template and skip generation entirely. The speculative
		// retrieval is dropped — the buffered channel lets that worker finish
		// without blocking. Paying for one unused retrieval on chit-chat is
		// cheaper than making every real question wait for the routing call.
		if smallTalkCanned() {
			t.Reply = SmallTalkReply(t.ReplyLang)
			t.Canned = true
		}
	} else {
		select {
		case t.GroundCtx = <-t.GroundCh:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if t.Canned {
		return true, nil
	}

	// Spend gate: shed before Google's wall instead of after it. The rolling
	// ceiling is the number Google itself enforces ($10 per 10 minutes on
	// Tier 1); crossing it comes back as a 429 the customer experiences as an
	// error. Handing the turn to a human is the same outcome delivered
	// gracefully — and unlike the 429 path it happens before retrieval and
	// generation are paid for.
	if spent, limit, over := usage.Budget(ctx, p.DB, p.Redis); over {
		p.AlertSpendGate(ctx, spent, limit)
		p.escalateToHuman(ctx, t.Event, t.Config, t.SessionID, "ai_decision",
			"AI 消费速率接近上限，AI 主动让路给人工", t.Urgency)
		return false, nil
	}

	message := t.Content
	if t.GroundCtx.HasMatch {
		message = rag.AugmentMessage(t.Content, &t.GroundCtx)
	}
	// Ahead of everything: a constraint from turn 2 ("5 boxes", "before Friday")
	// is still true at turn 40 even though the window dropped it.
	if mem := strings.TrimSpace(t.MemorySummary); mem != "" {
		message = sessionMemoryLine(mem) + "\n\n" + message
	}
	history, systemPrompt := t.History, ""
	if t.Persona != nil {
		history = append(personaHistory(t.Persona.BeginDialogs), t.History...)
		systemPrompt = t.Persona.SystemPrompt
	}
	result, err := p.serving().ChatAs(ctx, message, history, t.ReplyLang, systemPrompt)
	if err != nil {
		if IsQuotaExhausted(err) {
			// An exhausted quota or a drained balance is not a transient failure:
			// the rolling window outlives every retry the inbound queue would
			// attempt (5 attempts spread over 5s-15min), so the event would end up
			// 'failed' with the customer unanswered and nobody the wiser. Page the
			// operator, hand the conversation to a human and let the event
			// complete.
			p.AlertQuotaExhausted(ctx, err)
			p.escalateToHuman(ctx, t.Event, t.Config, t.SessionID, "ai_decision",
				"AI 配额/余额耗尽，AI 无法生成回复", t.Urgency)
			return false, nil
		}
		if !errors.Is(err, gemini.ErrNoReply) {
			return false, fmt.Errorf("AI 响应失败: %w", err)
		}
		// A blocked or empty generation is deterministic — the provider will not
		// produce text on the second attempt either — so retrying only delays the
		// handoff, and the customer would otherwise receive no answer at all. Hand
		// the turn to a human on the first refusal.
		p.Logger.Warn("model returned no usable reply; handing off instead",
			"session_id", t.SessionID, "model", p.serving().ModelName(), "error", err.Error())
		t.handOffWithoutReply("no-reply", "Model returned no usable reply (blocked or empty); customer handed to a human instead of answered")
		return true, nil
	}
	t.Result = result
	sid := t.SessionID
	usage.Record(ctx, p.DB, t.Config.UserID, &sid, p.serving().ModelName(), result.PromptTokens, result.OutputTokens, result.CachedTokens, result.ThoughtsTokens)
	t.Reply = result.Reply
	if !result.UsedMock {
		t.Reply = gemini.SanitizeReply(t.Reply)
	}
	if strings.TrimSpace(t.Reply) == "" {
		// Belt and braces behind the providers' own empty-response errors: an empty
		// chat bubble is worse than no answer, so hand the turn to a human rather
		// than deliver one. Recorded rather than retried — a provider that answers
		// 200 with no text has nothing to say on the second attempt either.
		p.Logger.Warn("model returned an empty reply; handing off instead",
			"session_id", sid, "model", p.serving().ModelName())
		t.handOffWithoutReply("empty-reply", "Model returned an empty reply; customer handed to a human instead of answered")
	}
	return true, nil
}

// guard-reply — the semantic guard (bounded at 1.5s) catches paraphrased source
// leaks, handoff promises and unconfirmed commitments the regex nets miss, plus
// the citation check against the grounding passages. Delivery is not enqueued
// yet, so this stage can still edit the reply.
//
// Canned small talk skips it: that is our own fixed text, so there is nothing
// to audit.
func (p *Pipeline) stageGuardReply(ctx context.Context, t *inboundTurn) (bool, error) {
	if t.Canned {
		return true, nil
	}
	srcTexts := make([]string, 0, len(t.GroundCtx.Sources))
	for _, src := range t.GroundCtx.Sources {
		srcTexts = append(srcTexts, src.Content)
	}
	if g, ok := p.GuardReply(ctx, t.Reply, srcTexts); ok {
		if g.LeaksSources {
			t.Reply = StripCitationLines(t.Reply)
		}
		t.ClaimsHandoff = g.PromisesHandoff
		if g.UnsafeClaim {
			p.alertQuality(ctx, t.Config.UserID, t.SessionID, "AI 回复包含待确认承诺",
				"Jev 标记该回复做出了需店员确认的承诺（价格/交期/库存等），请在收件箱检查该会话。")
		}
		if !g.SupportedBySources {
			p.alertQuality(ctx, t.Config.UserID, t.SessionID, "AI 回复脱离知识库作答",
				"Jev 标记该回复的事实性断言没有命中知识库原文（可能是幻觉），请核对后回复客户。")
		}
	}
	// Store the guarded answer for future identical asks — after the guard (the
	// cache must never serve what the guard would have edited) and before the
	return true, nil
}

// after-hours-preamble — a reply delivered outside business hours says so first, in
// the customer's own language (it was Khmer-only until 2026-10-10, so an English or
// Chinese customer got a Khmer sentence glued to their reply — the same class as the
// handoff-language bug).
func (p *Pipeline) stageAfterHours(ctx context.Context, t *inboundTurn) (bool, error) {
	if !p.isOpenNow(ctx, t.Config.UserID, t.Config.Platform) {
		t.Reply = closedHoursPreamble(t.ReplyLang) + "\n\n" + t.Reply
	}
	return true, nil
}

// closedHoursPreamble is the customer-facing "we are closed" line.
func closedHoursPreamble(language string) string {
	switch language {
	case "en":
		return "We are currently closed. An agent will reply during working hours."
	case "zh":
		return "我们目前不在服务时间，客服将在工作时间内回复您。"
	default:
		return "យើងកំពុងបិទសេវាកម្មនៅពេលនេះ។ ភ្នាក់ងារនឹងឆ្លើយតបនៅពេលម៉ោងធ្វើការ។"
	}
}

// handoff-reply — a turn that is handing off must say so in the customer's language
// with the platform's own sentence. Runs after the after-hours preamble (prepended text)
// and before delivery, so it is the last stage that may touch the reply; post-delivery
// then sees a sentence the guard matches and creates the handoff request deterministically.
//
// This is where "the model must end with the exact sentence" stops being a hope: the
// reply-quality eval measured all three providers failing it in different ways, and a
// promise of a human that nobody is paged for is the one reply the product must never
// send (see CanonicalHandoffReply).
func (p *Pipeline) stageHandoffReply(_ context.Context, t *inboundTurn) (bool, error) {
	if !t.ClaimsHandoff && !ReplyClaimsHandoff(t.Reply) {
		return true, nil
	}
	if next := CanonicalHandoffReply(t.Reply, t.ReplyLang); next != t.Reply {
		t.Reply = next
		if p.Logger != nil {
			p.Logger.Info("handoff reply normalized to the platform sentence",
				"session_id", t.SessionID, "language", t.ReplyLang)
		}
	}
	t.ClaimsHandoff = true
	return true, nil
}

// persist-and-deliver — store the model reply, fan it out to the inbox and queue
// the delivery (👍/👎 feedback rides on Telegram). A cache hit is a real answer
// with zero token cost, and model_name says so.
func (p *Pipeline) stagePersistAndDeliver(ctx context.Context, t *inboundTurn) (bool, error) {
	tokensUsed := t.Result.PromptTokens + t.Result.OutputTokens
	modelName := p.serving().ModelName()
	if t.Canned {
		tokensUsed, modelName = 0, "smalltalk-template"
		if t.CannedLabel != "" {
			modelName = t.CannedLabel
		}
	}
	modelMessageID, created, err := p.persistModelReply(ctx, t, modelName, tokensUsed)
	if err != nil {
		return false, err
	}
	p.publishMessage(ctx, t.Config.UserID, t.SessionID, modelMessageID, "model")
	feedbackPayload := map[string]any{"feedback": true}
	// The matched document's own picture rides along (attach-doc-image). The URL
	// is signed here, seconds before delivery, so a slow turn cannot hand the
	// provider a link that expired while the turn was still running.
	if t.AttachMediaKey != "" {
		if url := p.docImageURL(t.AttachMediaKey); url != "" {
			feedbackPayload["kind"] = "media"
			feedbackPayload["media_type"] = "image"
			feedbackPayload["media_url"] = url
		}
	}
	if err := p.enqueueDelivery(ctx, t.Event, t.Config, t.SessionID, modelMessageID, t.Reply, feedbackPayload); err != nil {
		return false, err
	}

	// Voice reply (opt-in): when the customer sent a voice note and TTS is
	// active, deliver the same answer as playable audio too — but only on the
	// attempt that wrote the reply. Re-synthesising on a retry would pay for TTS
	// twice and send the customer a second voice note with the same words.
	if created {
		if mediaKind, _ := t.PlatformMedia["kind"].(string); p.Cfg.TTSActive() && (mediaKind == "voice" || mediaKind == "audio") {
			p.enqueueVoiceReply(ctx, t.Event, t.Config, t.SessionID, t.Reply)
		}
	}
	return true, nil
}

// handOffWithoutReply replaces the turn's answer with the handoff
// acknowledgement and makes post-delivery raise the human handoff request. Every
// path where the model cannot answer (quota exhausted, blocked generation, empty
// reply) goes through here, so the turn still leaves a trace in the inbox.
func (t *inboundTurn) handOffWithoutReply(label, reason string) {
	t.Reply = HandoffAcknowledgement(t.ReplyLang)
	t.Canned = true
	t.CannedLabel = label
	t.ClaimsHandoff = true
	t.HandoffReason = reason
}

// The ledger names the tenant it charged (user_id): the row is keyed by event,
// but "this tenant paid for this turn" is the fact it records, and the static
// tenant gate needs a tenant column to check.
const sqlClaimInboundTurn = "INSERT INTO inbound_turn_ledger (event_id, user_id, quota_charged) VALUES ($1, $2, true) ON CONFLICT (event_id) DO NOTHING"

// chargeInboundTurn consumes one message from the tenant's monthly quota and
// records the charge against the event in the same transaction.
//
// bill-message runs on every attempt, and an event is retried up to maxAttempts
// times (claimInbound also re-claims a lock older than five minutes), so a bare
// increment used to consume two or five messages from a paying tenant's quota for
// one customer message. The ledger row makes it once per event. The row and the
// increment share a transaction because that is what the row is for: inserting
// the marker first and charging after let a transient billing error look like a
// completed charge, so the retry answered for free and never billed.
//
// ErrMessageQuotaExhausted is committed, not rolled back: the row records that
// this event's charge was decided (the quota was full), so a retry neither
// charges again nor loops on the same refusal, and the caller answers with the
// handoff acknowledgement. quota_charged = false is that record — a retry reads
// it and repeats the handoff rather than answering for free. Any other error
// rolls the whole thing back, so the retry charges properly.
func (p *Pipeline) chargeInboundTurn(ctx context.Context, userID int32, eventID int64) error {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("charge inbound turn: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, sqlClaimInboundTurn, eventID, userID)
	if err != nil {
		return fmt.Errorf("charge inbound turn: ledger: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// An earlier attempt already decided this event's charge. Read the decision
		// instead of assuming "charged": the row records a refusal too, and a
		// retry that treated a refusal as a charge answered an over-quota tenant
		// for free (the first attempt hit the quota wall, a later stage failed
		// once, and the retry generated a real reply). Charged turns must not be
		// billed twice, but a refusal has to be repeated.
		var charged bool
		if err := tx.QueryRow(ctx,
			"SELECT quota_charged FROM inbound_turn_ledger WHERE event_id = $1 AND user_id = $2",
			eventID, userID).Scan(&charged); err != nil {
			return fmt.Errorf("charge inbound turn: ledger decision: %w", err)
		}
		if charged {
			return nil
		}
		return usage.ErrMessageQuotaExhausted
	}
	err = usage.ConsumeMessageQuota(ctx, tx, userID)
	if err != nil && !errors.Is(err, usage.ErrMessageQuotaExhausted) {
		return fmt.Errorf("charge inbound turn: %w", err)
	}
	if errors.Is(err, usage.ErrMessageQuotaExhausted) {
		// Record the decision, not just the claim: this is the flag the retry
		// above reads to repeat the handoff instead of billing or answering.
		if _, uerr := tx.Exec(ctx,
			"UPDATE inbound_turn_ledger SET quota_charged = false WHERE event_id = $1 AND user_id = $2",
			eventID, userID); uerr != nil {
			return fmt.Errorf("charge inbound turn: record refusal: %w", uerr)
		}
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return fmt.Errorf("charge inbound turn: commit: %w", cerr)
	}
	return err
}

// persistModelReply writes the model row once per inbound event and reports
// whether this attempt is the one that created it.
//
// stagePersistAndDeliver re-runs with every retry; a second INSERT used to mint a
// second message_id, and platform_outbox dedupes on chat_message_id — so the new
// id was also a new outbox row and the customer received the same reply twice.
// The ledger row (locked FOR UPDATE) makes the pair of attempts that can exist
// for one event — claimInbound re-claims a stale lock while the original turn is
// still running — serialise on one message instead of racing to insert two.
func (p *Pipeline) persistModelReply(ctx context.Context, t *inboundTurn, modelName string, tokensUsed int) (int64, bool, error) {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the inbound event itself before touching the ledger. Two attempts for
	// one event are possible (claimInbound re-claims a stale lock while the
	// original turn still runs), and the ledger row's FOR UPDATE below only locks a
	// row that already exists — so without this the pair could both find no row and
	// both write a reply. Serialising on the event row is what makes "at most one
	// model row" true rather than likely.
	var eventLocked int
	if err := tx.QueryRow(ctx,
		"SELECT 1 FROM platform_inbound_events WHERE event_id = $1 AND config_id = $2 FOR UPDATE",
		t.Event.EventID, t.Event.ConfigID).Scan(&eventLocked); err != nil {
		return 0, false, fmt.Errorf("persist model reply: event lock: %w", err)
	}
	var existing int64
	err = tx.QueryRow(ctx,
		"SELECT COALESCE(model_message_id, 0) FROM inbound_turn_ledger WHERE event_id = $1 AND user_id = $2 FOR UPDATE",
		t.Event.EventID, t.Config.UserID).Scan(&existing)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("persist model reply: ledger: %w", err)
		}
		// No ledger row: the turn reached this stage without bill-message (a
		// rename of the event table would be the alarm). Create it here so the
		// reply is still written at most once. fails-vs-log below is "fail":
		// a missing lock would be the duplicate this function exists to stop.
		if _, err := tx.Exec(ctx, sqlClaimInboundTurn, t.Event.EventID, t.Config.UserID); err != nil {
			return 0, false, fmt.Errorf("persist model reply: ledger: %w", err)
		}
	}
	if existing != 0 {
		return existing, false, nil
	}
	var modelMessageID int64
	// media_url stores the object KEY, like every other chat_messages row (the
	// inbox presigns it when it renders); the customer's copy carries a signed URL
	// in the delivery payload instead.
	var attachArg any
	if t.AttachMediaKey != "" {
		attachArg = t.AttachMediaKey
	}
	err = tx.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, media_url, tokens_used, model_name, used_mock, sources_json, created_at) "+
			"VALUES ($1,'model','text',$2,$3,$4,$5,$6,$7,$8) RETURNING message_id",
		t.SessionID, t.Reply, attachArg, tokensUsed, modelName, t.Result.UsedMock,
		sourcesJSON(t.GroundCtx), time.Now()).
		Scan(&modelMessageID)
	if err != nil {
		return 0, false, fmt.Errorf("persist model reply: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE inbound_turn_ledger SET model_message_id = $2 WHERE event_id = $1 AND user_id = $3", t.Event.EventID, modelMessageID, t.Config.UserID); err != nil {
		return 0, false, fmt.Errorf("persist model reply: ledger: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE sessions SET model_message_count = model_message_count + 1, first_response_at = COALESCE(first_response_at, $1) WHERE session_id = $2", time.Now(), t.SessionID); err != nil {
		return 0, false, fmt.Errorf("persist model reply: session counter: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("persist model reply: %w", err)
	}
	return modelMessageID, true, nil
}

// post-delivery — make a promised handoff true, then classify the turn in the
// background (never blocks the customer).
func (p *Pipeline) stagePostDelivery(ctx context.Context, t *inboundTurn) (bool, error) {
	// Keep the assistant's memory current, off the customer's path: the note only
	// matters to a later turn, and a refresh is one bounded fast-model call per
	// memoryRefreshEvery messages.
	p.refreshSessionMemoryAsync(t.Config.UserID, t.SessionID)
	// Auto-handoff triggers 2+3. The reply announced a handoff to the customer
	// ("已为您转接人工…") — make it true: create the request now. No canned ack
	// (ev=nil) since the reply itself already told the customer.
	if ReplyClaimsHandoff(t.Reply) || t.ClaimsHandoff {
		reason := "AI reply announced a handoff to the customer"
		if t.HandoffReason != "" {
			reason = t.HandoffReason
		}
		p.escalateToHuman(ctx, nil, t.Config, t.SessionID, "ai_decision", reason, t.Urgency)
		return false, nil
	}
	grounded := t.GroundCtx.HasMatch
	p.classifyTurnAsync(t.Config.UserID, t.SessionID, t.Content, t.Reply, grounded)
	return true, nil
}

// screen-inbound — content safety on the way in. Runs after the message is
// persisted (the inbox keeps the evidence either way) and before anything pays
// for retrieval or generation.
//
// Two configured outcomes: hand the conversation to a human without an AI reply
// (the default), or end the turn silently. Either way the customer is not fed to
// the model and the operator gets a log line naming the strategy and the reason.
func (p *Pipeline) stageScreenInbound(ctx context.Context, t *inboundTurn) (bool, error) {
	gate := p.safetyGate()
	if !gate.Enabled() {
		return true, nil
	}
	v, flagged := gate.Check(ctx, t.Content)
	if !flagged {
		return true, nil
	}
	p.Logger.Warn("content safety flagged the customer message",
		"session_id", t.SessionID, "strategy", v.Strategy, "reason", v.Reason)
	if safetyInboundAction() == SafetyActionDrop {
		return false, nil
	}
	p.escalateToHuman(ctx, t.Event, t.Config, t.SessionID, "ai_decision",
		"Content safety flagged the customer message ("+v.Strategy+")", t.Urgency)
	return false, nil
}

// screen-reply — content safety on the way out. Runs after the guard (so it sees
// the reply that would actually be sent) and before the after-hours preamble and
// the delivery.
//
// A flagged reply is REPLACED, never dropped: the customer is owed an answer.
// The replacement is the same language-aware acknowledgement the handoff path
// uses, and ClaimsHandoff makes post-delivery create the handoff request — so the
// message the customer reads is true.
func (p *Pipeline) stageScreenReply(ctx context.Context, t *inboundTurn) (bool, error) {
	gate := p.safetyGate()
	if !gate.Enabled() {
		return true, nil
	}
	v, flagged := gate.Check(ctx, t.Reply)
	if !flagged {
		return true, nil
	}
	p.Logger.Warn("content safety flagged the model reply; replacing it",
		"session_id", t.SessionID, "strategy", v.Strategy, "reason", v.Reason)
	t.Reply = handoffAcknowledgement(t.ReplyLang)
	t.ClaimsHandoff = true
	return true, nil
}
