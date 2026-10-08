package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/redisstore"
)

// ---------------------------------------------------------------------------
// The rolling-spend guardrail: one variable, two meanings, one refusal.
// ---------------------------------------------------------------------------
//
// studio (AI Studio — today's production). Google enforces a spend-based rate
// limit on the account: $10 per rolling 10 minutes on Tier 1, $50 on Tier 2,
// $200 on Tier 3 (docs/GEMINI-RATE-LIMIT.md §3), and answers 429 once the
// window is full. token_usage.cost_estimate sums to the same figure Google is
// measuring, so a local threshold under that wall earns its keep: shedding a
// turn at 85% of it trades a customer-visible 500 for a graceful handoff.
// Under studio, GEMINI_SPEND_LIMIT_USD therefore mirrors an UPSTREAM wall, and
// its default (10) is that wall's Tier 1 value.
//
// vertex. None of that exists on the Gemini Enterprise Agent Platform. Vertex
// meters a project × region quota in RPM/TPM, and that quota is raised by a
// support ticket, not by spending more — there is no dollar ceiling upstream
// for a local dollar threshold to align with. Under vertex the same variable
// can only mean a SELF-IMPOSED budget: a business decision made here, with no
// wall behind it.
//
// So the same GEMINI_SPEND_LIMIT_USD=10 means two different things:
//
//	studio → "Google's Tier 1 wall, mirrored locally"  (a real ceiling)
//	vertex → nothing at all                            (a leftover)
//
// and the leftover is the accident worth engineering against. An operator who
// flips GEMINI_PROVIDER and leaves the old value in place keeps a $10/10min
// trigger that now sheds paying customers at a threshold that protects
// nobody: the customer is refused while the upstream is idle. That failure is
// silent in exactly the way migration 061's RLS policies were silent (see the
// warning in cmd/server/main.go and docs/DEVELOPMENT.md 「十」): the control
// looks armed, the logs look calm, and the only symptom is traffic being
// turned away.
//
// The previous answer to this risk was a sentence in the runbook — "升到
// Tier 2 后必须把 GEMINI_SPEND_LIMIT_USD 改成 50" (docs §11.6) — i.e. it
// relied on an operator remembering to edit an environment file in the middle
// of doing something else. That is not a control, it is a hope, and it is the
// same hope that left the RLS policies inert. So the resolution below refuses
// to guess: under vertex, a value that is one of AI Studio's three tier
// ceilings is NEVER accepted as a budget. It is a configuration error at boot
// (ValidateSpendConfig, next to gemini.ValidateProviderConfig) and it does not
// arm the gate at all — so even a deployment where the boot check was never
// wired up cannot reject customers on a meaningless number. Any other positive
// value is a deliberate budget, and behaves as one.

const spendWindow = 10 * time.Minute

// spendCacheKey caches the window sum for a few seconds: it is one indexed
// query over a small table, but it runs on the reply path.
const (
	spendCacheKey = "gemini:spend:10m"
	spendCacheTTL = 20 * time.Second
)

// The environment names this file reads, and the one provider value it
// branches on.
const (
	spendLimitEnv = "GEMINI_SPEND_LIMIT_USD"
	gateRatioEnv  = "GEMINI_SPEND_GATE_RATIO"
	providerEnv   = "GEMINI_PROVIDER"
	vertexName    = "vertex"
)

// Defaults for GEMINI_SPEND_LIMIT_USD when it is unset, per provider.
//
// studio keeps 10 — the measured Tier 1 ceiling (docs §3). Lowering, raising or
// "unifying" it would change production behaviour, which this increment must
// not do.
//
// vertex gets 0, which the rest of this file reads as "no ceiling": the honest
// default for a provider where 10 would be a fiction about a wall that does
// not exist. We do not impose a budget nobody chose. Note that 0 is not a new
// convention invented here — it is already the documented way to disable both
// gate and alert ("0 or negative disables...", see SpendLimitUSD).
const (
	studioDefaultLimitUSD = 10
	vertexDefaultLimitUSD = 0
)

// studioTierCeilingsUSD are AI Studio's three spend-based rate limits. Under
// vertex they are never a budget: they are precisely the values an environment
// file is most likely to still be carrying from before the migration, because
// that is what the studio runbook told operators to put there.
//
// The comparison is exact, and that is correct for these three: they are
// exactly representable as float64 and strconv.ParseFloat maps "10", " 10 ",
// "10.00" and "1e1" all onto exactly 10. A near miss (10.5) is a number
// somebody typed on purpose.
var studioTierCeilingsUSD = []float64{10, 50, 200}

// SpendBasis says where the effective ceiling came from — the one fact an
// operator needs after a shed ("did we do that because Google was about to 429
// us, or because of a number we chose ourselves?"). It is logged on every shed
// and is the hook a caller can use to word a page correctly; see
// SpendLimitBasis.
type SpendBasis string

const (
	// BasisStudioTierCeiling — the mirrored AI Studio wall: shedding here is
	// protecting the account from a real upstream 429.
	BasisStudioTierCeiling SpendBasis = "studio-tier-ceiling"
	// BasisVertexSelfBudget — a budget this deployment set for itself. Nothing
	// upstream forced the shed; raising or unsetting the variable stops it.
	BasisVertexSelfBudget SpendBasis = "vertex-self-budget"
	// BasisVertexNoBudget — vertex with nothing armed: the gate is off.
	BasisVertexNoBudget SpendBasis = "vertex-no-budget"
	// BasisVertexLegacyRefused — vertex + an AI Studio tier number: refused, so
	// the gate is off and the boot check fails.
	BasisVertexLegacyRefused SpendBasis = "vertex-legacy-studio-value-refused"
	// BasisVertexInvalidRefused — vertex + a value that is not a number:
	// refused, so the gate is off and the boot check fails.
	BasisVertexInvalidRefused SpendBasis = "vertex-invalid-value-refused"
)

// resolvedLimit is the outcome of reading the environment, and the reason the
// three public views (SpendLimitUSD, SpendLimitBasis, ValidateSpendConfig)
// cannot disagree with each other: they all call resolveSpendLimit.
type resolvedLimit struct {
	limit float64
	basis SpendBasis
	// fault is non-nil only for a vertex deployment whose configuration must
	// not boot. Studio never faults: its meaning has not changed, and a new
	// boot failure on the production path is exactly what this increment must
	// not introduce.
	fault error
}

// resolveSpendLimit is the single decision point for "what is the ceiling, and
// what does it mean".
func resolveSpendLimit() resolvedLimit {
	raw := strings.TrimSpace(os.Getenv(spendLimitEnv))

	if studioWallApplies() {
		// studio: byte-for-byte the historical resolution, including its
		// leniency. An unparseable value falls back to the Tier 1 default
		// rather than failing — production has always behaved that way and the
		// shape of this branch is not what this increment is changing.
		limit := float64(studioDefaultLimitUSD)
		if raw != "" {
			if f, err := strconv.ParseFloat(raw, 64); err == nil {
				limit = f
			}
		}
		return resolvedLimit{limit: limit, basis: BasisStudioTierCeiling}
	}

	// No AI Studio wall applies (Vertex, or a Claude provider): nothing may be
	// inherited by default.
	if raw == "" {
		return resolvedLimit{limit: float64(vertexDefaultLimitUSD), basis: BasisVertexNoBudget}
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return resolvedLimit{
			limit: 0,
			basis: BasisVertexInvalidRefused,
			fault: fmt.Errorf("%s=%q is not a number while %s: no budget would be armed, "+
				"yet the operator has been told one is — and no upstream spend limit exists under this regime to catch "+
				"what that missing guardrail was supposed to catch. Set a number, or unset %s",
				spendLimitEnv, raw, regimeName(), spendLimitEnv),
		}
	}
	if isStudioTierCeilingUSD(f) {
		return resolvedLimit{
			limit: 0,
			basis: BasisVertexLegacyRefused,
			fault: fmt.Errorf("%s=%s is one of AI Studio's spend-based rate limits "+
				"(Tier 1 $10 / Tier 2 $50 / Tier 3 $200 per 10 minutes) while %s: that regime enforces "+
				"no such limit, so this value would shed customer turns at a threshold that protects "+
				"nothing. Either unset %s (no budget, the honest default) or set it "+
				"to a value that is not one of 10/50/200 to declare a deliberate self-imposed budget",
				spendLimitEnv, raw, regimeName(), spendLimitEnv),
		}
	}
	if f <= 0 {
		// Explicitly disabled, same reading as studio's "0 or negative".
		return resolvedLimit{limit: f, basis: BasisVertexNoBudget}
	}
	return resolvedLimit{limit: f, basis: BasisVertexSelfBudget}
}

// providerIsVertex mirrors internal/gemini's providerKindFromEnv: anything that
// is not exactly "vertex" (case-insensitive, surrounding space ignored) is
// studio, so an unset, empty or misspelled value keeps today's path.
//
// The two-line rule is duplicated rather than imported on purpose: this package
// is on the reply path and should not pull internal/gemini's HTTP/TLS stack in
// for one unexported helper, and importing it would make gemini unable to
// record usage directly without a cycle. TestProviderMirrorsGeminiProvider-
// Resolution pins the rule itself; if internal/gemini ever exports an accessor,
// this should call it instead.
func providerIsVertex() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(providerEnv)), vertexName)
}

// studioWallApplies reports whether AI Studio's tier ceiling is the wall in force:
// the studio transport is configured AND the Gemini client serves generation. The
// tier numbers are Google's limits on Gemini calls. A Claude provider has no such
// wall, so the same number is then a budget question, answered by the vertex branch.
func studioWallApplies() bool {
	return !providerIsVertex() && servingIsGemini()
}

// servingProvider is the generation provider in force, published by internal/llm
// on every reload. The reply path reads it per call, so it is an atomic value.
var servingProvider atomic.Value

// SetServingProvider records the generation provider in force. Empty means the
// default, Gemini, which is what every deployment serves until it switches.
func SetServingProvider(provider string) {
	servingProvider.Store(provider)
}

// servingIsGemini mirrors internal/llm.IsClaude: a value this package does not
// recognise is Gemini, the reading every model_configs row has always had.
func servingIsGemini() bool {
	p, _ := servingProvider.Load().(string)
	return p != "anthropic" && p != "anthropic-vertex"
}

// regimeName names the spend regime the vertex branch applies, for the operator
// who reads a refusal.
func regimeName() string {
	if !servingIsGemini() {
		p, _ := servingProvider.Load().(string)
		return "the serving provider (" + p + ")"
	}
	return providerEnv + "=vertex"
}

// isStudioTierCeilingUSD reports whether f is one of AI Studio's tier ceilings.
func isStudioTierCeilingUSD(f float64) bool {
	for _, c := range studioTierCeilingsUSD {
		if f == c {
			return true
		}
	}
	return false
}

// SpendLimitUSD — the account's rolling 10-minute ceiling in USD, as it applies
// to the provider this deployment is actually configured for.
//
//	studio → GEMINI_SPEND_LIMIT_USD, default 10: the AI Studio tier ceiling,
//	         mirrored locally (Tier 2 is 50, Tier 3 is 200, and the value must
//	         be raised with the tier or the gate sheds turns Google would have
//	         accepted).
//	vertex → GEMINI_SPEND_LIMIT_USD, default 0: OUR budget, not Google's. 0 or
//	         negative means "no ceiling" and disables both gate and alert, which
//	         is the default because nobody has agreed to a vertex budget. One of
//	         AI Studio's tier numbers (10/50/200) is refused outright — see
//	         resolveSpendLimit.
//
// 0 or negative disables both gate and alert. Use SpendLimitBasis for why the
// number is what it is, and ValidateSpendConfig for the boot-time verdict.
func SpendLimitUSD() float64 {
	return resolveSpendLimit().limit
}

// SpendLimitBasis names where SpendLimitUSD's number came from, so an operator
// (or a page, or the admin endpoint) can tell a mirrored upstream wall from a
// budget this deployment chose. See the Basis* constants.
func SpendLimitBasis() SpendBasis {
	return resolveSpendLimit().basis
}

// ValidateSpendConfig reports, as an error, a vertex deployment whose spend
// configuration would be a silent trap — the spend-side twin of
// gemini.ValidateProviderConfig(), meant to be called beside it while the
// server is starting.
//
// The trap it exists for: GEMINI_PROVIDER moves to vertex while
// GEMINI_SPEND_LIMIT_USD still holds a studio number (10/50/200, the tier
// ceilings). That number no longer corresponds to anything upstream, but the
// gate would keep refusing customers on it, and — because the guardrail is
// doing exactly what it was told — the logs would look healthy. An operator
// should not have to remember to edit an environment variable to avoid
// rejecting paying traffic, so this is checked in code, at boot, where a
// mistake is a non-zero exit instead of a quiet loss of customers.
//
// It is a no-op for studio (the default), so wiring it in cannot change
// production behaviour. It reads only the environment and never the network.
func ValidateSpendConfig() error {
	return resolveSpendLimit().fault
}

// GateRatio — the fraction of the ceiling at which the gate starts shedding.
// Below 1.0 on purpose: turns already in flight have not been recorded yet
// (cost is written after generation), so the last few percent of the window is
// always slightly understated.
func GateRatio() float64 {
	if v := strings.TrimSpace(os.Getenv(gateRatioEnv)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			return f
		}
	}
	return 0.85
}

// RecentSpend returns the USD spent on model calls in the rolling window.
// Redis-cached for a few seconds; nil Redis falls through to the database.
func RecentSpend(ctx context.Context, db *pgxpool.Pool, rdb *redisstore.Client) (float64, error) {
	if db == nil {
		return 0, errors.New("no database")
	}
	if rdb != nil {
		if v, err := rdb.GetString(ctx, spendCacheKey); err == nil {
			if f, perr := strconv.ParseFloat(strings.TrimSpace(v), 64); perr == nil {
				return f, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var spent float64
	if err := db.QueryRow(ctx,
		"SELECT COALESCE(SUM(cost_estimate), 0) FROM token_usage WHERE created_at > $1",
		time.Now().Add(-spendWindow)).Scan(&spent); err != nil {
		return 0, err
	}
	if rdb != nil {
		_ = rdb.SetString(ctx, spendCacheKey, strconv.FormatFloat(spent, 'f', 6, 64), spendCacheTTL)
	}
	return spent, nil
}

// spendSource supplies the window sum. The indirection exists so a test can
// drive the gate's decision — and its log line — without a database.
type spendSource func(context.Context, *pgxpool.Pool, *redisstore.Client) (float64, error)

// Budget returns (spent, limit, over) for the current window. It fails OPEN:
// when the figure cannot be computed the caller takes its normal path, because
// a metering outage must never stop customer replies.
//
// It fails open for a refused limit too, and that is a decision rather than an
// oversight: a threshold that means nothing under the active provider must not
// be allowed to turn customers away, and the alternative — trusting it and
// logging loudly — is exactly the silent loss this resolution exists to
// prevent. The refusal is reported by SpendLimitBasis and, audibly, by
// ValidateSpendConfig and the ERROR line logged here.
func Budget(ctx context.Context, db *pgxpool.Pool, rdb *redisstore.Client) (float64, float64, bool) {
	return budget(ctx, db, rdb, RecentSpend, spendLog)
}

// budget is Budget with its two side-effect seams exposed: where the window sum
// comes from, and where the guardrail's log lines go. Production passes
// RecentSpend and the process-wide throttle.
func budget(ctx context.Context, db *pgxpool.Pool, rdb *redisstore.Client, recent spendSource, log *throttle) (float64, float64, bool) {
	r := resolveSpendLimit()
	if r.fault != nil {
		logRefusedLimit(log, r)
		return 0, 0, false
	}
	if r.limit <= 0 {
		return 0, r.limit, false
	}
	spent, err := recent(ctx, db, rdb)
	if err != nil {
		return 0, r.limit, false
	}
	if spent >= r.limit*GateRatio() {
		logShed(log, r.basis, spent, r.limit)
		return spent, r.limit, true
	}
	return spent, r.limit, false
}

// ---------------------------------------------------------------------------
// Observability
// ---------------------------------------------------------------------------

// Which ceiling tripped has been invisible until now: the number went to the
// operator (platform.AlertSpendGate) without saying whether it was Google's
// wall or our own choice, so "should we raise it?" could not be answered from
// the alert alone. Both bases are logged here, in the guardrail itself, so the
// answer does not depend on which caller asked.
const (
	shedLogEvery    = time.Minute
	refusedLogEvery = 5 * time.Minute
)

// spendLog is the process-wide throttle; tests pass their own.
var spendLog = newThrottle()

// throttle keeps the guardrail's log lines from becoming the incident. A full
// 10-minute window can shed thousands of turns, and every line would say the
// same thing; one line a minute is enough for an operator to see the state and
// for journalctl to show it started when it started.
type throttle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newThrottle() *throttle { return &throttle{last: make(map[string]time.Time)} }

func (t *throttle) allow(key string, every time.Duration) bool {
	return t.allowAt(key, every, time.Now())
}

// allowAt is allow with the clock supplied, so the interval can be tested
// without sleeping.
func (t *throttle) allowAt(key string, every time.Duration, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[key]; ok && now.Sub(last) < every {
		return false
	}
	t.last[key] = now
	return true
}

// logShed records one shed turn, and — the point of this file's second half —
// which ceiling caused it.
func logShed(log *throttle, basis SpendBasis, spent, limit float64) {
	if !log.allow("shed", shedLogEvery) {
		return
	}
	slog.Default().Warn("Gemini spend gate shed a turn",
		"basis", string(basis),
		"meaning", basisMeaning(basis),
		"spent_usd", spent,
		"limit_usd", limit,
		"gate_ratio", GateRatio())
}

// logRefusedLimit makes a refused configuration audible even if the boot check
// (ValidateSpendConfig) was never wired into main: a guardrail that silently
// stops guarding is the same class of failure as one that silently guards the
// wrong number, so this repeats rather than announcing itself once.
func logRefusedLimit(log *throttle, r resolvedLimit) {
	if !log.allow("refused:"+string(r.basis), refusedLogEvery) {
		return
	}
	slog.Default().Error("Gemini spend configuration refused — the spend gate is NOT armed",
		"error", r.fault.Error(),
		"basis", string(r.basis),
		"effective_limit_usd", 0,
		"effect", "no turn is shed on this threshold; the configured value is meaningless under the active provider")
}

// basisMeaning is the sentence that makes a bare number actionable in a log
// line: it is what tells an operator whether raising the limit would be
// overriding a policy or walking into Google's 429.
func basisMeaning(basis SpendBasis) string {
	switch basis {
	case BasisStudioTierCeiling:
		return "Google's upstream wall: AI Studio's spend-based rate limit for the account's tier is real, " +
			"so shedding here is what keeps the 429 off the customer's screen"
	case BasisVertexSelfBudget:
		return "self-imposed budget only: Vertex enforces no spend-based rate limit, so no upstream pressure " +
			"forced this shed — raise GEMINI_SPEND_LIMIT_USD or unset it to stop shedding"
	default:
		return "unknown basis: the guardrail's own bug, not an upstream limit"
	}
}
