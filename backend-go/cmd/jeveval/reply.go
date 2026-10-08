package main

// Reply-quality evaluation: how good are the Khmer replies the product actually
// serves?
//
//	DATABASE_URL=… PLATFORM_CREDENTIAL_KEY=… go run ./cmd/jeveval -mode reply \
//	    -eval kb/evals/reply_eval.json [-category price] [-min-score 8] [-judge=false] [-v]
//
// This is the missing third leg of this repo's measurement story. `rageval`
// scores RETRIEVAL (did the right chunks come back) and `jeveval -mode khmer`
// scores the JUDGE (can Jev read Khmer at all); neither can tell whether the
// sentence the customer received is correct, in the right language, in a
// consistent register, and free of the formatting noise the prompt forbids. Every
// prompt edit, model swap or KB re-wording changes those, and until this existed
// each of those changes was judged by reading a few replies and hoping.
//
// It runs the PRODUCTION path, not a reconstruction: the serving model config
// from `model_configs` (including the stored prompt and the region), retrieval
// through `rag.Service.Ground`, and the same `rag.AugmentMessage` the pipeline
// calls. Two layers of scoring, because they fail differently:
//
//   - DETERMINISTIC checks decide pass/fail. Facts that must appear (a price, a
//     MOQ), literals that must not (an invented price, "Source 1", a zero-width
//     space, a markdown table). These cannot be talked around by a model.
//   - A JUDGE (the fast model, same one production uses for aux work) scores the
//     axes no string check can: is this actually Khmer, is the register
//     consistent, does it read like a person. Its numbers are a trend across
//     runs, never a verdict: the rubric is in the eval file precisely so a human
//     can audit what it was asked.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/db"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/replyscore"
	"khmer-ai-cs-go/internal/security"
)

// judgeVerdict is what the judge model is asked to return, one JSON object.
type judgeVerdict struct {
	Language   int    `json:"language"` // 0-3: is it the asked-for language, cleanly
	Register   int    `json:"register"` // 0-3: consistent polite register
	Natural    int    `json:"natural"`  // 0-3: reads like a person, not translated English
	Format     int    `json:"format"`   // 0-3: chat-shaped (no markdown/citations)
	Reason     string `json:"reason"`   // one short sentence
	ScoreTotal int    `json:"-"`        // filled in locally
}

type caseResult struct {
	c       replyscore.Case
	reply   string
	context string
	noKB    bool
	missing []string
	leaks   []string
	format  []string
	verdict judgeVerdict
	judged  bool
	seconds float64
}

// decryptModelKey opens a sealed model_configs.api_key, tolerating a legacy
// plaintext value (the sealer passes unrecognised values through). A failure to
// open a sealed value yields the input, matching the server's behaviour.
func decryptModelKey(stored string) string {
	sealer, err := security.NewSealer(os.Getenv("PLATFORM_CREDENTIAL_KEY"))
	if err != nil {
		return stored
	}
	return sealer.DecryptOrKeep(stored)
}

func logToStderr() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// evalOptions are the knobs of one reply-quality run.
type evalOptions struct {
	evalPath string
	category string
	userID   int32
	// gate is the minimum judge total out of 12. Only meaningful with judge on:
	// the deterministic checks are pass/fail on their own.
	gate    float64
	judge   bool
	verbose bool
	// dump prints the grounding context exactly as the model received it. It is
	// the only way to tell "the model ignored a fact" from "the fact never made it
	// into the prompt" — a distinction this harness got wrong once already.
	dump bool
	// temperature overrides the deployment's sampling temperature for this run
	// only; negative means "keep whatever the deployment sends".
	temperature float64
}

// temperatureLabel renders the arm a run measured: the number, or the platform's
// own default when the deployment sends no temperature at all (which is not 0).
func temperatureLabel(t *float64) string {
	if t == nil {
		return "platform default (unset)"
	}
	return strconv.FormatFloat(*t, 'f', -1, 64)
}

func runReply(mode evalOptions) {
	if mode.evalPath == "" {
		fmt.Fprintln(os.Stderr, "reply: -eval is required (e.g. kb/evals/reply_eval.json)")
		os.Exit(2)
	}
	raw, err := os.ReadFile(mode.evalPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read eval file:", err)
		os.Exit(1)
	}
	var file replyscore.File
	if err := json.Unmarshal(raw, &file); err != nil {
		fmt.Fprintln(os.Stderr, "parse eval file:", err)
		os.Exit(1)
	}
	if len(file.Cases) == 0 {
		fmt.Fprintln(os.Stderr, "reply: eval file has no cases")
		os.Exit(2)
	}

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "reply: DATABASE_URL is not set — the whole point is to measure "+
			"the replies the deployed knowledge base produces, so retrieval cannot be skipped")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db:", err)
		os.Exit(1)
	}
	defer pool.Close()

	svc, err := servingRAGService(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reply:", err)
		os.Exit(2)
	}
	// Refuse to measure a tenant that has no indexed knowledge. This harness was
	// run for a whole round with -user 1 (a platform account) while the KB belongs
	// to another tenant: every case then measured "what does it answer with no
	// knowledge", the facts came back missing, and the conclusion drawn from it
	// was wrong. A tool that can silently measure the wrong world must refuse to.
	if err := requireIndexedKnowledge(ctx, pool, mode.userID); err != nil {
		fmt.Fprintln(os.Stderr, "reply:", err)
		os.Exit(2)
	}
	// -temperature overrides the deployment's own value for this run only: the
	// point is to compare arms (1.0 vs 0.7 vs 0.3) WITHOUT changing what production
	// sends, and without a second binary. It is applied after the DB value so an
	// operator can reproduce "what if we switched".
	if mode.temperature >= 0 {
		t := mode.temperature
		svc.Gemini.SetTemperature(&t)
	}
	// Warm the embedding connection the way the server does at boot, and FAIL
	// LOUDLY if it cannot be warmed: a cold connection drops the dense retrieval
	// leg (see gemini.WarmEmbeddings), so every case would be scored against a
	// lexical-only knowledge base that production does not use. The first version
	// of this harness skipped this and reported a cross-lingual retrieval failure
	// that was really a cold-start timeout.
	cold := ""
	if err := svc.Gemini.WarmEmbeddings(ctx, 45*time.Second, 6); err != nil {
		cold = err.Error()
		fmt.Fprintln(os.Stderr, "reply: ================= DENSE RETRIEVAL IS COLD =================")
		fmt.Fprintln(os.Stderr, "reply: ", err)
		fmt.Fprintln(os.Stderr, "reply: grounding in this run falls back to lexical-only; the")
		fmt.Fprintln(os.Stderr, "reply: numbers below do NOT describe production. Fix the embedding")
		fmt.Fprintln(os.Stderr, "reply: path (GEMINI_EMBED_BUDGET_MS / keep-warm) before trusting them.")
	}

	var results []caseResult
	for _, c := range file.Cases {
		if mode.category != "" && c.Category != mode.category {
			continue
		}
		results = append(results, runOneReplyCase(ctx, svc, c, mode))
	}
	if len(results) == 0 {
		fmt.Fprintln(os.Stderr, "reply: no cases matched the filters")
		os.Exit(2)
	}

	printReplyReport(results, file, mode, cold, temperatureLabel(svc.Gemini.Temperature()))
	if failed := replyFailures(results, mode); failed > 0 {
		os.Exit(1)
	}
}

// servingRAGService builds the retrieval + generation stack from the deployed
// configuration, exactly as the server does at boot: the default model_configs
// row (prompt included), its sealed API key, and the Vertex region the console
// last switched to.
func servingRAGService(ctx context.Context, pool *pgxpool.Pool) (*rag.Service, error) {
	svc := &rag.Service{DB: pool, Logger: logToStderr()}
	if apiKey, modelName, systemPrompt, maxTokens, region, temperature, ok := gemini.LoadDefaultConfig(ctx, pool); ok {
		svc.Gemini = gemini.FromPartsFull(decryptModelKey(apiKey), modelName, systemPrompt, maxTokens)
		if err := svc.Gemini.SetVertexRegion(region); err != nil {
			// Loud, but not fatal: the eval then measures the environment's
			// region, which is what a misconfigured deployment would serve from.
			fmt.Fprintln(os.Stderr, "reply: ignoring stored vertex region:", err)
		}
		// Same sampling temperature as the deployment, so the arm that is supposed
		// to represent "what production sends today" really is. -temperature below
		// overrides it for the other arms of a comparison.
		svc.Gemini.SetTemperature(temperature)
	} else if key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); key != "" {
		svc.Gemini = gemini.New(key, strings.TrimSpace(os.Getenv("GEMINI_MODEL")), 2048)
	} else {
		return nil, fmt.Errorf("no model config (no default model_configs row and no GEMINI_API_KEY)")
	}
	if !svc.Gemini.IsConfigured() {
		return nil, fmt.Errorf("the serving model is not configured (mock mode)")
	}
	return svc, nil
}

// requireIndexedKnowledge fails when the tenant has no ready documents.
func requireIndexedKnowledge(ctx context.Context, pool *pgxpool.Pool, userID int32) error {
	var docs, tenants int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FILTER (WHERE index_status = 'ready') FROM knowledge_documents WHERE uploaded_by = $1",
		userID).Scan(&docs); err != nil {
		return fmt.Errorf("could not count the tenant's knowledge: %w", err)
	}
	if docs > 0 {
		return nil
	}
	if err := pool.QueryRow(ctx,
		"SELECT count(DISTINCT uploaded_by) FROM knowledge_documents WHERE index_status = 'ready'").Scan(&tenants); err != nil {
		tenants = -1 // only used for the hint below
	}
	return fmt.Errorf("user %d has no indexed knowledge (ready docs = 0): the run would measure the "+
		"no-knowledge path and every fact check would fail for the wrong reason. "+
		"Pick the tenant that owns the KB (-user); %d tenant(s) have ready documents",
		userID, tenants)
}

func runOneReplyCase(ctx context.Context, svc *rag.Service, c replyscore.Case, mode evalOptions) caseResult {
	res := caseResult{c: c}
	started := time.Now()

	// Production retrieval, then production augmentation: if the reply is wrong
	// because the chunks were wrong, that is rageval's finding, and this run still
	// reports it honestly rather than papering over it with a perfect prompt.
	ground := svc.Ground(ctx, mode.userID, nil, c.Question, c.Language, nil, 0)
	res.noKB = !ground.HasMatch
	message := c.Question
	if ground.HasMatch {
		message = rag.AugmentMessage(c.Question, &ground)
	}
	chat, err := svc.Gemini.Chat(ctx, message, nil, c.Language)
	res.seconds = time.Since(started).Seconds()
	if err != nil {
		res.format = append(res.format, "generation error: "+err.Error())
		return res
	}
	if chat.UsedMock {
		res.format = append(res.format, "mock reply (model not reachable)")
		return res
	}
	res.reply = chat.Reply
	if mode.dump {
		res.context = ground.ContextStr
	}

	res.missing = replyscore.MissingFacts(c, chat.Reply)
	res.leaks = replyscore.Leaked(c, chat.Reply)
	res.format = replyscore.FormatProblems(c, chat.Reply)
	// The handoff category is checked against the REAL detector, not a string
	// proxy: a reply that promises an agent while platform.ReplyClaimsHandoff
	// returns false is the failure mode the prompt's per-language sentence exists
	// to prevent — the customer waits, and no agent is ever notified.
	if c.Category == "handoff" && !platform.ReplyClaimsHandoff(chat.Reply) {
		res.format = append(res.format, "commits to a handoff but platform.ReplyClaimsHandoff does not match — "+
			"no agent would be notified")
	}
	if mode.judge {
		res.verdict = judgeReply(ctx, svc, c, chat.Reply)
		res.judged = true
	}
	return res
}

func judgePrompt(c replyscore.Case, reply string) string {
	return "You are auditing ONE customer-service reply from a Cambodian EPS/insulation supplier.\n" +
		"The customer wrote in " + c.Language + ". The reply must be in " + c.Language + ".\n\n" +
		"Question: " + c.Question + "\n" +
		"Reply: " + reply + "\n\n" +
		"Score the reply on four axes, each 0-3:\n" +
		"- language: 3 = fluent, idiomatic " + c.Language + "; 0 = wrong language or unreadable\n" +
		"- register: 3 = one consistent polite customer-service register; 0 = mixed/cold\n" +
		"- natural: 3 = reads like a Cambodian salesperson wrote it; 0 = word-for-word translation\n" +
		"- format: 3 = clean chat text; 0 = markup, citations, invisible junk, wall of text\n" +
		// The house chat convention, stated because it is a convention and not a
		// judgment call: without it the judge systematically penalises the list
		// style the product asks for (measured: it flagged '- ' bullets as markdown
		// in 4 of the first 23 cases). It is NOT told which pronoun to expect —
		// that disagreement is a finding for a native speaker, not something to
		// suppress.
		"Note: this product's chat convention allows plain paragraphs and list lines starting with '- ' or '• '. " +
		"Those are NOT formatting failures; only **bold**, ## headings, tables, code fences and citation markers are.\n" +
		"Answer with JSON only: {\"language\":n,\"register\":n,\"natural\":n,\"format\":n,\"reason\":\"<one short sentence>\"}"
}

func judgeReply(ctx context.Context, svc *rag.Service, c replyscore.Case, reply string) judgeVerdict {
	text, ok := svc.Gemini.GenerateFast(ctx, judgePrompt(c, reply), 45*time.Second)
	if !ok {
		return judgeVerdict{Reason: "judge unavailable"}
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return judgeVerdict{Reason: "judge returned no JSON"}
	}
	var v judgeVerdict
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil {
		return judgeVerdict{Reason: "judge JSON unparseable"}
	}
	v.ScoreTotal = v.Language + v.Register + v.Natural + v.Format
	return v
}

func printReplyReport(results []caseResult, file replyscore.File, mode evalOptions, cold, temperature string) {
	passed, judged, scoreSum := 0, 0, 0
	for _, r := range results {
		status := "ok  "
		if !r.ok(mode) {
			status = "FAIL"
		}
		judgedScore := "-"
		if r.judged && r.verdict.ScoreTotal > 0 {
			judgedScore = fmt.Sprintf("%d/12", r.verdict.ScoreTotal)
			judged++
			scoreSum += r.verdict.ScoreTotal
		}
		fmt.Printf("%s %-22s %-10s judge=%-6s %5.1fs\n", status, r.c.ID, r.c.Category, judgedScore, r.seconds)
		for _, m := range r.missing {
			fmt.Printf("       missing fact: %s\n", m)
		}
		for _, l := range r.leaks {
			fmt.Printf("       must not appear: %s\n", l)
		}
		for _, f := range r.format {
			fmt.Printf("       format: %s\n", f)
		}
		if r.noKB {
			verdict := ""
			if requiresGrounding(r.c.Category) {
				verdict = " — this category needs grounding, so the case FAILS"
			} else {
				verdict = " — expected for this category (procedural/trap), not a failure"
			}
			fmt.Printf("       no KB match (RETRIEVAL, not generation — check with rageval before blaming the prompt)%s\n", verdict)
		}
		if r.judged && r.verdict.Reason != "" {
			fmt.Printf("       judge: %s\n", r.verdict.Reason)
		}
		if mode.verbose {
			fmt.Printf("       reply: %s\n", strings.ReplaceAll(r.reply, "\n", " ⏎ "))
		}
		if mode.dump && r.context != "" {
			fmt.Printf("       context-in: %s\n", strings.ReplaceAll(r.context, "\n", " ⏎ "))
		}
		if r.ok(mode) {
			passed++
		}
	}
	avg := 0.0
	if judged > 0 {
		avg = float64(scoreSum) / float64(judged)
	}
	unjudged := ""
	if mode.judge && judged < len(results) {
		// Say it out loud: the average covers the judged subset only, and the rest
		// are failures now — a reader must be able to tell "0 failures" from "1
		// never judged".
		unjudged = fmt.Sprintf("  ⚠ %d/%d UNJUDGED (counted as failures)", len(results)-judged, len(results))
	}
	fmt.Printf("\n%d/%d cases pass; judge average %.2f/12 (gate %.2f/12)  temperature=%s%s\n",
		passed, len(results), avg, mode.gate, temperature, unjudged)
	if cold != "" {
		fmt.Printf("\n⚠ DENSE RETRIEVAL WAS COLD (%s) — grounding was lexical-only; "+
			"these numbers do not describe production.\n", cold)
	}
	if len(file.Rubric) > 0 {
		fmt.Println("rubric (from the eval file, so the judge's question can be audited):")
		sort.Slice(file.Rubric, func(i, j int) bool { return file.Rubric[i].Score < file.Rubric[j].Score })
		for _, level := range file.Rubric {
			fmt.Printf("  %d = %s\n", level.Score, level.Meaning)
		}
	}
}

func replyFailures(results []caseResult, mode evalOptions) int {
	failed := 0
	for _, r := range results {
		if !r.ok(mode) {
			failed++
		}
	}
	return failed
}

func (r caseResult) ok(mode evalOptions) bool {
	if r.noKB && requiresGrounding(r.c.Category) {
		return false
	}
	if len(r.missing) > 0 || len(r.leaks) > 0 || len(r.format) > 0 {
		return false
	}
	if mode.judge {
		// -judge is a gate, so an UNSCORED case is a failure, not a pass.
		//
		// The judge is a model call with a 45s budget; when it expires, judgeReply
		// returns ScoreTotal 0 / "judge unavailable". The old condition failed only a
		// *low* score, so a run could print "24/24 cases pass; judge average
		// 11.83/12" with one case never judged at all — the average silently divided
		// by 23, and the one reply nobody looked at was the one reported as fine
		// (2026-10-04). Same class of false green as sqlcheck skipping without
		// SQLCHECK_REQUIRED: a gate that cannot fail is not a gate.
		if r.verdict.ScoreTotal <= 0 {
			return false
		}
		if float64(r.verdict.ScoreTotal) < mode.gate {
			return false
		}
	}
	return true
}

// requiresGrounding — which categories are only meaningful when the knowledge base
// answered. A miss there means the run measured the wrong world and the case must
// fail loudly rather than be scored on an ungrounded reply.
//
// handoff is deliberately absent, and finding that out cost a wrong conclusion:
// "connect me to a human" is a procedural request with no KB answer, so a miss is
// NORMAL — and the first version of this rule failed such a case even though the
// reply was the exact required Khmer handoff sentence, which made temperature 0.3
// look like it broke instruction-following when it had done nothing of the sort.
// trap is absent for the same reason: there the fact checks are the judge, and the
// correct answer to a nonexistent grade is "not recorded".
func requiresGrounding(category string) bool {
	switch category {
	case "handoff", "trap":
		return false
	}
	return true
}
