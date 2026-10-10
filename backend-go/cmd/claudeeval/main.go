// Command claudeeval measures this platform's model providers against its own
// criteria: the capability surface the reply path uses, and the Khmer reply quality
// that kb/evals/reply_eval.json measures.
//
// The reply half is an A/B: every case runs through every available arm (DeepSeek,
// the Gemini baseline, Claude when it can authenticate) with the same system prompt,
// the same grounding and the same rubric — deterministic checks plus the shared
// judge in internal/replyscore — so the arms differ only in the model. That is the
// question an operator actually has before switching a provider — "is it as good
// for Khmer customers?" — and a single-arm run cannot answer it.
//
// Arms are built from what the host can authenticate: Gemini from the service-account
// file, DeepSeek and Claude from the sealed key of their model_configs row. An arm
// that cannot be built is printed as SKIPPED, never silently dropped.
//
// Run it where the credentials live (the app host). It writes nothing: grounding is
// read-only and no usage observer is installed, so a run leaves no rows in token_usage.
//
//	set -a; . /opt/khmer-ai-cs/.env-go; set +a
//	./claudeeval -eval reply_eval.json -user 1 -limit 0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/anthropic"
	"khmer-ai-cs-go/internal/deepseek"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/llm"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/replyscore"
	"khmer-ai-cs-go/internal/security"
	"khmer-ai-cs-go/internal/usage"
)

// arm is one provider under test. The llm.Model interface is the same one the reply
// path calls, so what is measured here is what customers get.
type arm struct {
	name  string
	model llm.Model
	// bogus builds the same provider pinned to a model id that does not exist. The
	// capability check uses it to prove a typo fails loudly instead of being answered
	// by a mock; nil skips that one check for the arm.
	bogus func() llm.Model
}

func main() {
	mode := flag.String("mode", "all", "caps | reply | all")
	evalPath := flag.String("eval", "reply_eval.json", "-mode reply: path to the reply eval JSON")
	userID := flag.Int("user", 1, "-mode reply: tenant whose knowledge base grounds the cases")
	limit := flag.Int("limit", 0, "-mode reply: run only the first N cases (0 = all)")
	workers := flag.Int("workers", 3, "parallel cases")
	timeout := flag.Duration("timeout", 60*time.Second, "per model call")
	systemPrompt := flag.String("system", "", "system prompt for both arms (default: the deployment's stored prompt)")
	geminiModel := flag.String("gemini-model", envOr("GEMINI_MODEL", "gemini-3.8-flash"), "Gemini model for the baseline arm")
	geminiRegion := flag.String("gemini-region", envOr("GEMINI_VERTEX_REGION", "global"), "Vertex region for the Gemini arm")
	judgeModel := flag.String("judge-model", "", "-mode reply: the fast model that scores replies (default: the Gemini arm's model)")
	claudeModel := flag.String("claude-model", "claude-haiku-5-5", "Claude model under test")
	claudeKeyFlag := flag.String("anthropic-key", "", "Anthropic API key (default: the sealed key of the default model_configs row)")
	deepseekModel := flag.String("deepseek-model", "deepseek-flash", "DeepSeek model under test")
	deepseekKeyFlag := flag.String("deepseek-key", "", "DeepSeek API key (default: the sealed key of the default model_configs row)")
	jsonOut := flag.String("json", "", "also write the raw results to this path")
	flag.Parse()

	if *mode != "caps" && *mode != "reply" && *mode != "all" {
		fmt.Fprintln(os.Stderr, "claudeeval: -mode must be caps, reply or all")
		os.Exit(2)
	}
	ctx := context.Background()

	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "claudeeval:", err)
		os.Exit(2)
	}
	defer pool.Close()

	gem, err := newGemini(*geminiModel, *geminiRegion)
	if err != nil {
		fmt.Fprintln(os.Stderr, "claudeeval: gemini arm:", err)
		os.Exit(2)
	}
	// Arms are built from what this host can actually authenticate, and a missing arm
	// is SAID OUT LOUD: a comparison that quietly lost an arm reads as a clean result,
	// and this harness exists to tell providers apart, not to run whatever answers.
	var arms []arm
	deep, deepKey, err := newDeepSeek(ctx, pool, *deepseekModel, *deepseekKeyFlag)
	if err != nil {
		fmt.Printf("  deepseek arm: SKIPPED (%v)\n", err)
	} else {
		arms = append(arms, arm{name: "deepseek", model: deep, bogus: func() llm.Model {
			return deepseek.New(deepseek.Config{APIKey: deepKey, Model: "deepseek-does-not-exist-9-9", MaxTokens: 64})
		}})
	}
	// The Gemini arm's bogus client: same transport, an id no catalog has, so a typo
	// must fail rather than be answered.
	gemBogus := gemini.New("", "gemini-does-not-exist-9-9", 64)
	if region := gem.Region(); region != "" {
		_ = gemBogus.SetVertexRegion(region)
	}
	arms = append(arms, arm{name: "gemini", model: gem, bogus: func() llm.Model { return gemBogus }})
	claude, provider, claudeKey, err := newClaude(ctx, pool, *claudeModel, *claudeKeyFlag)
	if err != nil {
		fmt.Printf("  claude arm : SKIPPED (%v)\n", err)
	} else {
		arms = append(arms, arm{name: "claude", model: claude, bogus: func() llm.Model {
			return anthropic.New(anthropic.Config{APIKey: claudeKey, Model: "claude-does-not-exist-9-9"})
		}})
	}
	if len(arms) == 0 {
		fmt.Fprintln(os.Stderr, "claudeeval: no arm could be built — nothing to measure")
		os.Exit(2)
	}
	// The judge is the fast model. Keeping it separate from the Gemini ARM removes the
	// self-preference of a model scoring its own outputs: measured 2026-10-10, Gemini 3.8
	// judged by itself averaged 11.81-11.87/12 against DeepSeek, and 11.68 under the
	// flash-lite judge — a ~0.15 tilt, small but free to remove. With one arm only, the
	// default (arm model = judge model) is what the deployment already uses.
	judgeGen := gem
	if m := strings.TrimSpace(*judgeModel); m != "" && m != *geminiModel {
		judgeGen, err = newGemini(m, *geminiRegion)
		if err != nil {
			fmt.Fprintln(os.Stderr, "claudeeval: judge:", err)
			os.Exit(2)
		}
	}
	prompt := *systemPrompt
	if prompt == "" {
		if row, ok := llm.LoadDefault(ctx, pool); ok {
			prompt = row.SystemPrompt
		}
	}

	report := &report{
		started:  time.Now(),
		provider: provider,
		system:   promptSource(prompt),
	}
	fmt.Printf("claudeeval — %s\n", report.started.Format(time.RFC3339))
	for _, a := range arms {
		switch a.name {
		case "deepseek":
			fmt.Printf("  deepseek arm: deepseek (model=%s)\n", *deepseekModel)
		case "gemini":
			fmt.Printf("  gemini arm : gemini (model=%s, region=%s)\n", *geminiModel, gem.Region())
		case "claude":
			fmt.Printf("  claude arm : claude (model=%s, provider=%s)\n", *claudeModel, provider)
		}
	}
	if prompt != "" {
		fmt.Printf("  system     : %d chars from the deployment's stored prompt\n", len(prompt))
	} else {
		fmt.Printf("  system     : the clients' built-in default\n")
	}
	fmt.Printf("  judge      : %s\n", judgeGen.ModelName())
	fmt.Println()

	if *mode == "caps" || *mode == "all" {
		for _, a := range arms {
			runCaps(ctx, a, *timeout, report)
		}
	}
	if *mode == "reply" || *mode == "all" {
		runReplyEval(ctx, pool, gem, judgeGen.GenerateFast, prompt, arms, *evalPath, int32(*userID), *limit, *workers, *timeout, report)
	}

	report.print()
	if *jsonOut != "" {
		if err := report.writeJSON(*jsonOut); err != nil {
			fmt.Fprintln(os.Stderr, "claudeeval: json:", err)
			os.Exit(1)
		}
	}
	if report.failed() > 0 {
		os.Exit(1)
	}
}

// ── the report ───────────────────────────────────────────────────────────────

type checkResult struct {
	Name   string  `json:"name"`
	OK     bool    `json:"ok"`
	Detail string  `json:"detail"`
	Millis float64 `json:"ms"`
}

type caseResult struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Language string   `json:"language"`
	Missing  []string `json:"missing"`
	Leaks    []string `json:"leaks"`
	Format   []string `json:"format"`
	// RawMarkup records that the MODEL emitted markdown, before the reply was passed
	// through gemini.SanitizeReply — the same door every outbound reply walks through in
	// production. Scoring uses the sanitized text (what a customer would see), so this
	// field is the model-behaviour signal: a provider that ignores the prompt's "no
	// markdown" rule shows up here even when the delivered reply is clean.
	RawMarkup  bool    `json:"raw_markup"`
	Millis     float64 `json:"ms"`
	PromptTok  int     `json:"prompt_tokens"`
	OutTok     int     `json:"output_tokens"`
	Cost       float64 `json:"cost_usd"`
	KhmerRatio float64 `json:"khmer_letter_ratio"`
	// Judge is the rubric verdict from the fast model (the same one production uses
	// for auxiliary work, so it is never the model under test). Available()==false
	// means the judge did not answer — reported, not averaged away.
	Judge replyscore.JudgeVerdict `json:"judge"`
	// ShortCircuit names the production rule that answered without the model (today:
	// an explicit human request, which stageKeywordHandoff handles deterministically).
	ShortCircuit string `json:"short_circuit,omitempty"`
	// Canonicalized records that the handoff sentence was enforced before scoring —
	// production does this in stageHandoffReply, so the score is the delivered text.
	Canonicalized bool   `json:"canonicalized,omitempty"`
	Err           string `json:"error,omitempty"`
	Reply         string `json:"reply,omitempty"`
}

type report struct {
	started  time.Time
	provider string
	system   string
	caps     map[string][]checkResult
	capOrder []string
	order    []string
	cases    map[string][]caseResult
}

func (r *report) addCheck(armName string, c checkResult) {
	if r.caps == nil {
		r.caps = map[string][]checkResult{}
	}
	if _, seen := r.caps[armName]; !seen {
		r.capOrder = append(r.capOrder, armName)
	}
	r.caps[armName] = append(r.caps[armName], c)
}

func (r *report) addCases(name string, results []caseResult) {
	if r.cases == nil {
		r.cases = map[string][]caseResult{}
	}
	r.cases[name] = results
	r.order = append(r.order, name)
}

func (r *report) failed() int {
	n := 0
	for _, list := range r.caps {
		for _, c := range list {
			if !c.OK {
				n++
			}
		}
	}
	return n
}

func (r *report) print() {
	if len(r.caps) > 0 {
		fmt.Println("── capabilities ─────────────────────────────────────────────")
		for _, armName := range r.capOrder {
			for _, c := range r.caps[armName] {
				mark := "✓"
				if !c.OK {
					mark = "✗"
				}
				fmt.Printf("%s %-9s %-26s %7.0fms  %s\n", mark, armName, c.Name, c.Millis, c.Detail)
			}
		}
		fmt.Println()
	}
	if len(r.order) == 0 {
		return
	}
	fmt.Println("── Khmer reply eval (same cases, same prompt, same grounding) ──")
	base := r.cases[r.order[0]]
	for i := range base {
		fmt.Printf("  %-9s %-3s %s\n", base[i].ID, base[i].Language, base[i].Category)
		for _, name := range r.order {
			c := r.cases[name][i]
			verdict := "ok"
			switch {
			case c.Err != "":
				verdict = "ERROR " + short(c.Err, 60)
			case len(c.Missing) > 0:
				verdict = "missing " + short(strings.Join(c.Missing, ","), 46)
			case len(c.Leaks) > 0:
				verdict = "leaked " + short(strings.Join(c.Leaks, ","), 46)
			case len(c.Format) > 0:
				verdict = "format " + short(strings.Join(c.Format, ","), 46)
			}
			if c.RawMarkup {
				verdict += "  [raw: markdown]"
			}
			judge := "-"
			if c.Judge.Available() {
				judge = fmt.Sprintf("%d/12", c.Judge.Total)
			}
			note := ""
			if c.ShortCircuit != "" {
				note = "  [" + c.ShortCircuit + "]"
			} else if c.Canonicalized {
				note = "  [handoff sentence enforced]"
			}
			fmt.Printf("    %-7s %5.0fms %4d+%-4d $%.5f km=%.2f judge=%-5s  %s%s\n",
				name, c.Millis, c.PromptTok, c.OutTok, c.Cost, c.KhmerRatio, judge, verdict, note)
			// A judge reason is only printed where it can change a decision: the
			// low scores. Every-reply prose would bury the summary this report exists for.
			if c.Judge.Available() && c.Judge.Total < 9 {
				fmt.Printf("                                     judge: %s\n", short(c.Judge.Reason, 72))
			}
		}
	}
	fmt.Println()
	fmt.Println("── summary ─────────────────────────────────────────────────")
	fmt.Printf("%-8s %8s %8s %10s %10s %10s %12s %12s %10s\n", "arm", "cases", "pass", "missing", "leaked", "format", "raw-md", "avg lat", "judge")
	for _, name := range r.order {
		cases := r.cases[name]
		var past, missing, leaked, format, rawMarkup int
		var total float64
		var judgeSum, judged int
		for _, c := range cases {
			total += c.Millis
			if c.RawMarkup {
				rawMarkup++
			}
			if c.Judge.Available() {
				judgeSum += c.Judge.Total
				judged++
			}
			switch {
			case c.Err != "":
			case len(c.Missing) > 0:
				missing++
			case len(c.Leaks) > 0:
				leaked++
			case len(c.Format) > 0:
				format++
			default:
				past++
			}
		}
		avg := total / float64(max(len(cases), 1))
		judge := "-"
		if judged > 0 {
			judge = fmt.Sprintf("%.2f/12", float64(judgeSum)/float64(judged))
		}
		if judged < len(cases) {
			// Say the denominator out loud: an average over a subset must not read
			// as a whole-arm number (same rule as jeveval's UNJUDGED line).
			judge += fmt.Sprintf(" (%d/%d)", judged, len(cases))
		}
		fmt.Printf("%-8s %8d %8d %10d %10d %10d %12d %9.0fms %10s\n", name, len(cases), past, missing, leaked, format, rawMarkup, avg, judge)
	}
}

func (r *report) writeJSON(path string) error {
	raw, err := json.MarshalIndent(map[string]any{
		"started":         r.started,
		"claude_provider": r.provider,
		"system_prompt":   r.system,
		"capabilities":    r.caps,
		"cases":           r.cases,
	}, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// ── capability checks ────────────────────────────────────────────────────────

func runCaps(ctx context.Context, a arm, timeout time.Duration, rep *report) {
	check := func(name string, fn func(context.Context) (string, error)) {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		started := time.Now()
		detail, err := fn(cctx)
		took := time.Since(started)
		if err != nil {
			rep.addCheck(a.name, checkResult{Name: name, OK: false, Detail: short(err.Error(), 90), Millis: ms(took)})
			return
		}
		rep.addCheck(a.name, checkResult{Name: name, OK: true, Detail: detail, Millis: ms(took)})
	}

	// Chat, one per language the product serves. The Khmer case also runs the
	// platform's own format invariants, so "it answered" is not the bar.
	for _, lc := range []struct{ lang, q string }{
		{"km", "តើក្រុមហ៊ុនរបស់អ្នកមានឈ្មោះអ្វី?"},
		{"en", "In one short sentence, what does the company do?"},
		{"zh", "请用一句话说明你们公司做什么。"},
	} {
		lc := lc
		check("chat "+lc.lang, func(cctx context.Context) (string, error) {
			res, err := a.model.Chat(cctx, lc.q, nil, lc.lang)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(res.Reply) == "" {
				return "", errors.New("empty reply")
			}
			detail := fmt.Sprintf("%d Khmer letters, %.2f ratio, %d+%d tok",
				replyscore.KhmerLetters(res.Reply), replyscore.KhmerLetterRatio(res.Reply),
				res.PromptTokens, res.OutputTokens)
			if problems := replyscore.FormatProblems(replyscore.Case{Language: lc.lang}, res.Reply); len(problems) > 0 {
				return "", errors.New(strings.Join(problems, "; "))
			}
			return detail, nil
		})
	}

	// Streaming: the widget path streams, so deltas must actually arrive.
	check("chat stream (deltas)", func(cctx context.Context) (string, error) {
		var deltas int
		var firstMS float64
		started := time.Now()
		res, err := a.model.ChatStream(cctx, "Say hello in one short Khmer sentence.", nil, "km", func(string) {
			deltas++
			if deltas == 1 {
				firstMS = ms(time.Since(started))
			}
		})
		if err != nil {
			return "", err
		}
		if deltas == 0 {
			return "", errors.New("no streaming deltas: the reply came back in one piece")
		}
		return fmt.Sprintf("%d deltas, first at %.0fms, %d tok", deltas, firstMS, res.OutputTokens), nil
	})

	// A staff reply in the history must survive: the customer's next question asks for
	// the figure the agent gave, and an answer with a different one is a failure.
	check("history + agent turn", func(cctx context.Context) (string, error) {
		history := []gemini.HistoryItem{
			{Role: "user", Content: "តើតម្លៃប៉ុន្មាន?"},
			{Role: "agent", Content: "ខ្ញុំបានផ្តល់តម្លៃពិសេស 77 ដុល្លារ សម្រាប់អ្នក។"},
		}
		res, err := a.model.Chat(cctx, "ដូច្នេះតម្លៃប៉ុន្មាន?", history, "km")
		if err != nil {
			return "", err
		}
		if !strings.Contains(res.Reply, "77") {
			return "", fmt.Errorf("the staff figure 77 is gone from the reply: %s", short(res.Reply, 60))
		}
		return "kept the agent's figure (77)", nil
	})

	// Persona override (migration 067): the per-turn prompt must win over the stored
	// one. The instruction is production-shaped (a role plus a signature the reply has
	// to carry), NOT a bracketed literal: deepseek-flash garbles tokens like
	// [PERSONA-OK] while copying real content (prices, product codes, the handoff
	// sentence) exactly, so the literal form measured a decoding quirk no persona uses
	// (2026-10-10).
	check("persona override", func(cctx context.Context) (string, error) {
		res, err := a.model.ChatAs(cctx, "Say hello to a customer.", nil, "en",
			"You are Anna, a sales agent for WANFANG. Reply in one short sentence and sign it exactly as: — Anna")
		if err != nil {
			return "", err
		}
		if !strings.Contains(res.Reply, "Anna") {
			return "", fmt.Errorf("persona prompt ignored: %s", short(res.Reply, 60))
		}
		return "the per-turn prompt was followed", nil
	})

	// Auxiliary path with the KB compile's shape: a JSON object, not prose.
	check("aux JSON (compile)", func(cctx context.Context) (string, error) {
		out, ok := a.model.GenerateFastMax(cctx, `Return ONLY a JSON object: {"ok": true, "lang": "km"}. No prose, no code fence.`, 30*time.Second, 512)
		if !ok {
			return "", errors.New("GenerateFastMax reported failure")
		}
		raw := strings.TrimSpace(out)
		raw = strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(raw, "```json"), "```"), "```")
		var v map[string]any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return "", fmt.Errorf("not JSON: %s", short(out, 60))
		}
		if v["ok"] != true {
			return "", fmt.Errorf("unexpected payload: %s", short(out, 60))
		}
		return "valid JSON", nil
	})

	// A model that does not exist must fail loudly. A silent mock answer here would
	// make every model-config typo look like a working deployment.
	if a.bogus == nil {
		return
	}
	check("unknown model id fails", func(cctx context.Context) (string, error) {
		res, err := a.bogus().Chat(cctx, "ping", nil, "en")
		if err == nil && strings.TrimSpace(res.Reply) != "" {
			return "", errors.New("an unknown model id was answered instead of failing")
		}
		if err == nil {
			return "", errors.New("no error and no reply")
		}
		return "refused: " + short(err.Error(), 60), nil
	})
}

// ── the Khmer reply eval, both arms ──────────────────────────────────────────

func runReplyEval(ctx context.Context, pool *pgxpool.Pool, gem *gemini.Service, judge replyscore.Ask, systemPrompt string,
	arms []arm, evalPath string, userID int32, limit, workers int, timeout time.Duration, rep *report) {

	file, err := replyscore.Load(evalPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "claudeeval: eval file:", err)
		os.Exit(2)
	}
	cases := file.Cases
	if limit > 0 && limit < len(cases) {
		cases = cases[:limit]
	}

	var ready int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM knowledge_documents WHERE uploaded_by = $1 AND index_status = 'ready'", userID).Scan(&ready); err != nil {
		fmt.Fprintln(os.Stderr, "claudeeval: count knowledge:", err)
		os.Exit(2)
	}
	if ready == 0 {
		fmt.Fprintf(os.Stderr, "claudeeval: tenant %d has no ready documents; the eval would measure the no-knowledge path\n", userID)
		os.Exit(2)
	}

	// Grounding goes through the production retrieval path, on the Gemini client:
	// retrieval is Gemini's job whatever serves generation, and both arms must see
	// exactly the same context.
	svc := &rag.Service{DB: pool, Gemini: gem, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	fmt.Printf("reply eval: %d cases, tenant %d (%d ready docs), workers %d\n\n", len(cases), userID, ready, workers)

	type indexed struct {
		i int
		c replyscore.Case
	}
	jobs := make(chan indexed)
	results := make(map[string][]caseResult, len(arms))
	for _, a := range arms {
		results[a.name] = make([]caseResult, len(cases))
	}
	var wg sync.WaitGroup
	for w := 0; w < max(workers, 1); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				for _, a := range arms {
					results[a.name][job.i] = scoreCase(ctx, svc, a, job.c, userID, systemPrompt, timeout, judge)
				}
			}
		}()
	}
	for i, c := range cases {
		jobs <- indexed{i: i, c: c}
	}
	close(jobs)
	wg.Wait()

	for _, a := range arms {
		rep.addCases(a.name, results[a.name])
	}
}

func scoreCase(ctx context.Context, svc *rag.Service, a arm, c replyscore.Case, userID int32, systemPrompt string, timeout time.Duration, judge replyscore.Ask) caseResult {
	out := caseResult{ID: c.ID, Category: c.Category, Language: c.Language}

	// An explicit human request never reaches the model in production: the
	// keyword-handoff stage answers with the canned acknowledgement in the customer's
	// language and opens the queue entry. Score exactly that — measuring an arm here
	// would measure a path customers never take.
	if keyword, matched := platform.HumanRequestKeyword(c.Question); matched {
		out.ShortCircuit = "keyword-handoff:" + keyword
		finalize(ctx, &out, c, platform.HandoffAcknowledgement(c.Language), judge)
		return out
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	message := c.Question
	if ground := svc.Ground(cctx, userID, nil, c.Question, c.Language, nil, 0); ground.HasMatch {
		message = rag.AugmentMessage(c.Question, &ground)
	}

	started := time.Now()
	res, err := a.model.ChatAs(cctx, message, nil, c.Language, systemPrompt)
	out.Millis = ms(time.Since(started))
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.PromptTok = res.PromptTokens
	out.OutTok = res.OutputTokens
	out.Cost = usage.EstimateCostFor(a.model.ModelName(), res.PromptTokens, res.OutputTokens, res.CachedTokens)
	// RawMarkup records that the MODEL emitted markdown, before the sanitizer every
	// outbound reply walks through in production: a provider that ignores the prompt's
	// "no markdown" rule shows up here even when the delivered reply is clean.
	out.RawMarkup = strings.Contains(res.Reply, "**") || strings.Contains(res.Reply, "```") || hasHeadingMarker(res.Reply)
	finalize(ctx, &out, c, gemini.SanitizeReply(res.Reply), judge)
	return out
}

// finalize scores the text a customer would receive. Production post-processes every
// reply before delivery — the sanitizer strips markdown, and a turn that is handing off
// gets the platform's own sentence in the customer's language (stageHandoffReply) — so
// the deterministic checks and the judge must see that text, not the raw model output.
// The handoff category additionally asserts the guard: a promise the guard cannot match
// is a promise no agent is paged for (claude-haiku-5-5 paraphrased it on 2026-10-10).
func finalize(ctx context.Context, out *caseResult, c replyscore.Case, delivered string, judge replyscore.Ask) {
	if c.Category == "handoff" {
		if next := platform.CanonicalHandoffReply(delivered, c.Language); next != delivered {
			delivered = next
			out.Canonicalized = true
		}
	}
	out.Reply = delivered
	out.KhmerRatio = replyscore.KhmerLetterRatio(delivered)
	out.Missing = replyscore.MissingFacts(c, delivered)
	out.Leaks = replyscore.Leaked(c, delivered)
	out.Format = replyscore.FormatProblems(c, delivered)
	if c.Category == "handoff" && !platform.ReplyClaimsHandoff(delivered) {
		out.Format = append(out.Format, "handoff promise the guard cannot match: ReplyClaimsHandoff=false, no agent would be notified")
	}
	out.Judge = replyscore.Judge(ctx, judge, c, delivered)
}

// hasHeadingMarker reports a line-leading markdown heading in the RAW reply: one to six
// '#' followed by a space, the same shape gemini's markup strip removes. "#777, Road No.
// 2" is an address, not a heading, and must not be reported as markup.
func hasHeadingMarker(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		i := 0
		for i < len(line) && line[i] == '#' && i < 7 {
			i++
		}
		if i > 0 && i <= 6 && i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			return true
		}
	}
	return false
}

// ── clients ─────────────────────────────────────────────────────────────────

func openPool(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required (the eval grounds through the tenant's knowledge base)")
	}
	return pgxpool.New(ctx, dsn)
}

// newGemini builds the baseline arm from the service-account file the deployment uses.
// It does not read model_configs: after a provider switch that table's default row is a
// Claude row, and Gemini's own settings then live in the process environment.
func newGemini(model, region string) (*gemini.Service, error) {
	sa := strings.TrimSpace(os.Getenv("GEMINI_VERTEX_SA_FILE"))
	svc := gemini.New("", model, 2048)
	if sa != "" {
		if err := svc.SetVertexRegion(region); err != nil {
			return nil, err
		}
	}
	if !svc.IsConfigured() {
		return nil, fmt.Errorf("Gemini is not configured (no %s, no GEMINI_API_KEY)", "GEMINI_VERTEX_SA_FILE")
	}
	return svc, nil
}

// newDeepSeek builds the DeepSeek arm. The key comes from -deepseek-key or from the
// sealed key of a DeepSeek row: the default row when DeepSeek serves, otherwise the row
// kept alongside the serving provider. The key is also returned so the caller can build
// the bogus-model client for the capability check.
func newDeepSeek(ctx context.Context, pool *pgxpool.Pool, model, keyFlag string) (*deepseek.Service, string, error) {
	key := strings.TrimSpace(keyFlag)
	row, ok := llm.LoadDefault(ctx, pool)
	if !ok || row.Provider != llm.ProviderDeepSeek {
		if r, found := llm.LoadProvider(ctx, pool, llm.ProviderDeepSeek); found {
			row, ok = r, true
		}
	}
	if !ok && key == "" {
		return nil, "", errors.New("no default model_configs row: nothing names the provider")
	}
	if key == "" {
		if row.Provider != llm.ProviderDeepSeek {
			return nil, "", fmt.Errorf("no DeepSeek row found (default is %q); pass -deepseek-key", row.Provider)
		}
		if row.APIKey == "" {
			return nil, "", errors.New("the default row has no stored key and -deepseek-key was not given")
		}
		sealer, err := security.NewSealer(os.Getenv("PLATFORM_CREDENTIAL_KEY"))
		if err != nil {
			return nil, "", fmt.Errorf("PLATFORM_CREDENTIAL_KEY: %w", err)
		}
		key = sealer.DecryptOrKeep(row.APIKey)
	}
	row.ModelName = model
	svc := deepseek.New(llm.DeepSeekConfig(row, key))
	if !svc.IsConfigured() {
		return nil, "", errors.New("the DeepSeek client is not configured")
	}
	return svc, key, nil
}

// newClaude builds the arm under test. The key comes from -anthropic-key or from the
// sealed key of a Claude row: the default row when Claude serves, otherwise the Claude
// row kept alongside the serving provider.
func newClaude(ctx context.Context, pool *pgxpool.Pool, model, keyFlag string) (*anthropic.Service, string, string, error) {
	row, ok := llm.LoadDefault(ctx, pool)
	if !ok || row.Provider != llm.ProviderAnthropic {
		if r, found := llm.LoadProvider(ctx, pool, llm.ProviderAnthropic); found {
			row, ok = r, true
		}
	}
	if !ok {
		return nil, "", "", errors.New("no default model_configs row: nothing names the provider")
	}
	key := strings.TrimSpace(keyFlag)
	if key == "" {
		if row.Provider != llm.ProviderAnthropic {
			return nil, "", "", fmt.Errorf("no Claude row found (default is %q); pass -anthropic-key", row.Provider)
		}
		if row.APIKey == "" {
			return nil, "", "", errors.New("the default row has no stored key and -anthropic-key was not given")
		}
		sealer, err := security.NewSealer(os.Getenv("PLATFORM_CREDENTIAL_KEY"))
		if err != nil {
			return nil, "", "", fmt.Errorf("PLATFORM_CREDENTIAL_KEY: %w", err)
		}
		key = sealer.DecryptOrKeep(row.APIKey)
	}
	row.ModelName = model
	svc := anthropic.New(llm.ClaudeConfig(row, key))
	if !svc.IsConfigured() {
		return nil, "", "", fmt.Errorf("the Claude client is not configured (provider=%s)", row.Provider)
	}
	return svc, row.Provider, key, nil
}

func promptSource(prompt string) string {
	if prompt == "" {
		return "built-in default"
	}
	return "model_configs.system_prompt"
}

// ── small helpers ───────────────────────────────────────────────────────────

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
