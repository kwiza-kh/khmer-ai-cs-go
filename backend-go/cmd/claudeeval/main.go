// Command claudeeval measures the Claude provider against this platform's own
// criteria: the capability surface the reply path uses, and the Khmer reply quality
// that kb/evals/reply_eval.json measures.
//
// The reply half is an A/B: every case runs through BOTH clients (Claude and Gemini)
// with the same system prompt, the same grounding and the same eval rubric, so the
// two arms differ only in the model. That is the question an operator actually has
// before switching a tenant — "is it as good for Khmer customers?" — and a single-arm
// run cannot answer it.
//
// Run it where the credentials live (the app host): the Anthropic key is read from the
// sealed model_configs row, decrypted with PLATFORM_CREDENTIAL_KEY, unless
// -anthropic-key is given; Gemini is built from GEMINI_VERTEX_SA_FILE. It writes
// nothing: grounding is read-only and no usage observer is installed, so a run leaves
// no rows in token_usage.
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
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/llm"
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
	claudeModel := flag.String("claude-model", "claude-haiku-5-5", "Claude model under test")
	claudeKeyFlag := flag.String("anthropic-key", "", "Anthropic API key (default: the sealed key of the default model_configs row)")
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
	claude, provider, claudeKey, err := newClaude(ctx, pool, *claudeModel, *claudeKeyFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "claudeeval: claude arm:", err)
		os.Exit(2)
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
	fmt.Printf("  claude arm : claude (model=%s, provider=%s)\n", *claudeModel, provider)
	fmt.Printf("  gemini arm : gemini (model=%s, region=%s)\n", *geminiModel, gem.Region())
	if prompt != "" {
		fmt.Printf("  system     : %d chars from the deployment's stored prompt\n", len(prompt))
	} else {
		fmt.Printf("  system     : the clients' built-in default\n")
	}
	fmt.Println()

	claudeArm := arm{name: "claude", model: claude}
	geminiArm := arm{name: "gemini", model: gem}

	if *mode == "caps" || *mode == "all" {
		runCaps(ctx, claudeArm, claudeKey, provider, *timeout, report)
	}
	if *mode == "reply" || *mode == "all" {
		runReplyEval(ctx, pool, gem, prompt, []arm{claudeArm, geminiArm}, *evalPath, int32(*userID), *limit, *workers, *timeout, report)
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
	Err        string  `json:"error,omitempty"`
	Reply      string  `json:"reply,omitempty"`
}

type report struct {
	started  time.Time
	provider string
	system   string
	caps     []checkResult
	order    []string
	cases    map[string][]caseResult
}

func (r *report) addCheck(c checkResult) {
	r.caps = append(r.caps, c)
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
	for _, c := range r.caps {
		if !c.OK {
			n++
		}
	}
	return n
}

func (r *report) print() {
	if len(r.caps) > 0 {
		fmt.Println("── capabilities ─────────────────────────────────────────────")
		for _, c := range r.caps {
			mark := "✓"
			if !c.OK {
				mark = "✗"
			}
			fmt.Printf("%s %-26s %7.0fms  %s\n", mark, c.Name, c.Millis, c.Detail)
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
			fmt.Printf("    %-7s %5.0fms %4d+%-4d $%.5f km=%.2f  %s\n",
				name, c.Millis, c.PromptTok, c.OutTok, c.Cost, c.KhmerRatio, verdict)
		}
	}
	fmt.Println()
	fmt.Println("── summary ─────────────────────────────────────────────────")
	fmt.Printf("%-8s %8s %8s %10s %10s %10s %12s %12s\n", "arm", "cases", "past", "missing", "leaked", "format", "raw-km", "avg lat")
	for _, name := range r.order {
		cases := r.cases[name]
		var past, missing, leaked, format, rawMarkup int
		var total float64
		for _, c := range cases {
			total += c.Millis
			if c.RawMarkup {
				rawMarkup++
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
		fmt.Printf("%-8s %8d %8d %10d %10d %10d %12d %9.0fms\n", name, len(cases), past, missing, leaked, format, rawMarkup, avg)
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

func runCaps(ctx context.Context, a arm, key, provider string, timeout time.Duration, rep *report) {
	check := func(name string, fn func(context.Context) (string, error)) {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		started := time.Now()
		detail, err := fn(cctx)
		took := time.Since(started)
		if err != nil {
			rep.addCheck(checkResult{Name: name, OK: false, Detail: short(err.Error(), 90), Millis: ms(took)})
			return
		}
		rep.addCheck(checkResult{Name: name, OK: true, Detail: detail, Millis: ms(took)})
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

	// Persona override (migration 067): the per-turn prompt must win over the stored one.
	check("persona override", func(cctx context.Context) (string, error) {
		res, err := a.model.ChatAs(cctx, "ping", nil, "en", "Reply with exactly [PERSONA-OK] and nothing else.")
		if err != nil {
			return "", err
		}
		if !strings.Contains(res.Reply, "[PERSONA-OK]") {
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
	check("unknown model id fails", func(cctx context.Context) (string, error) {
		svc := anthropic.New(anthropic.Config{APIKey: key, Model: "claude-does-not-exist-9-9"})
		res, err := svc.Chat(cctx, "ping", nil, "en")
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

func runReplyEval(ctx context.Context, pool *pgxpool.Pool, gem *gemini.Service, systemPrompt string,
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
					results[a.name][job.i] = scoreCase(ctx, svc, a, job.c, userID, systemPrompt, timeout)
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

func scoreCase(ctx context.Context, svc *rag.Service, a arm, c replyscore.Case, userID int32, systemPrompt string, timeout time.Duration) caseResult {
	out := caseResult{ID: c.ID, Category: c.Category, Language: c.Language}
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
	// Score what the customer would actually receive: production passes every reply
	// through gemini.SanitizeReply (citation leftovers + Khmer hygiene + the chat markup
	// strip), so a raw-model metric would report a formatting defect that never ships.
	raw := res.Reply
	sanitized := gemini.SanitizeReply(raw)
	out.Reply = sanitized
	out.RawMarkup = strings.Contains(raw, "**") || strings.Contains(raw, "```") || hasHeadingMarker(raw)
	out.PromptTok = res.PromptTokens
	out.OutTok = res.OutputTokens
	out.Cost = usage.EstimateCostFor(a.model.ModelName(), res.PromptTokens, res.OutputTokens, res.CachedTokens)
	out.KhmerRatio = replyscore.KhmerLetterRatio(sanitized)
	out.Missing = replyscore.MissingFacts(c, sanitized)
	out.Leaks = replyscore.Leaked(c, sanitized)
	out.Format = replyscore.FormatProblems(c, sanitized)
	return out
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

// newClaude builds the arm under test. The key comes from -anthropic-key or from the
// sealed key of the default row, which is where the console stores it.
func newClaude(ctx context.Context, pool *pgxpool.Pool, model, keyFlag string) (*anthropic.Service, string, string, error) {
	row, ok := llm.LoadDefault(ctx, pool)
	if !ok {
		return nil, "", "", errors.New("no default model_configs row: nothing names the provider")
	}
	key := strings.TrimSpace(keyFlag)
	if key == "" {
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
