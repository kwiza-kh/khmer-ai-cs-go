package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/typesafe"
)

// Live battery of realistic Khmer customer-service cases, judged against the
// real API to measure what Jev actually does — including adversarial shapes
// (negation of the handoff request, mixed language, filler-only turns).
type liveCase struct {
	name     string
	kind     string // turn | guard | route | notify | rerank
	msg      string
	reply    string
	hasMatch bool
	// expectations: escalate bool; intent/sentiment/topic/route string;
	// flags []string (guard); worth bool; best int (rerank index)
	expectEscalate *bool
	expectIntent   string
	expectSent     string
	expectRoute    string
	expectFlags    []string
	expectWorth    *bool
	expectBest     int
}

func boolPtr(b bool) *bool { return &b }

func liveCases() []liveCase {
	return []liveCase{
		// ===== turn: should ESCALATE =====
		{name: "refund-demand", kind: "turn", msg: "ខ្ញុំចង់បានការសងប្រាក់វិញឥឡូវនេះ! ម៉ាស៊ីនខូចតាំងពីថ្ងៃទី ១ មក", reply: "សូមអភ័យទោស។ យើងនឹងពិនិត្យ។", hasMatch: true, expectEscalate: boolPtr(true), expectIntent: "refund", expectSent: "negative"},
		{name: "explicit-human-request", kind: "turn", msg: "មនុស្សពិត! ខ្ញុំមិនចង់និយាយជាមួយ AI ទៀតទេ", reply: "ខ្ញុំកំពុងព្យាយាមជួយ។", hasMatch: true, expectEscalate: boolPtr(true), expectIntent: "complaint", expectSent: "negative"},
		{name: "damaged-late-delivery", kind: "turn", msg: "ការដឹកជញ្ជូនយឺតពេក ហើយទំនិញដែលទទួលបានខូច។ ខ្ញុំកំពុងតស៊ូមតិ!", reply: "សូមអភ័យទោសចំពោះបទពិសោធន៍។", hasMatch: true, expectEscalate: boolPtr(true), expectSent: "negative"},
		{name: "bulk-order-500", kind: "turn", msg: "ចង់ទិញ 500 គ្រឿង តម្លៃប៉ុន្មាន? មានបញ្ចុះតម្លៃទេ?", reply: "សូមទំនាក់ទំនងសម្រាប់ការសម្រង់តម្លៃ។", hasMatch: false, expectEscalate: boolPtr(true), expectIntent: "bulk_order"},
		// ===== turn: should STAY with AI =====
		{name: "price-question", kind: "turn", msg: "តម្លៃ KWF-RO-75 ប៉ុន្មាន?", reply: "KWF-RO-75 មានលក់នៅហាងយើង។ សូមទំនាក់ទំនងសម្រាប់តម្លៃ។", hasMatch: true, expectEscalate: boolPtr(false), expectIntent: "price"},
		{name: "negation-no-human", kind: "turn", msg: "ខ្ញុំមិនចង់បានមនុស្សទេ គ្រាន់តែប្រាប់ខ្ញុំតម្លៃបានទេ?", reply: "បាទ តម្លៃគឺ 250$ ។", hasMatch: true, expectEscalate: boolPtr(false)},
		{name: "greeting", kind: "turn", msg: "សួស្តី!", reply: "សួស្តី! យើងជាការស្វាគមន៍។", hasMatch: false, expectEscalate: boolPtr(false), expectSent: "positive"},
		{name: "thanks", kind: "turn", msg: "អរគុណច្រើន!", reply: "មិនជាការបញ្ហាទេ។", hasMatch: false, expectEscalate: boolPtr(false)},
		{name: "mixed-english", kind: "turn", msg: "Hello, do you have KWF-COM-300 in stock? មានទេ?", reply: "Yes, it is available in our store.", hasMatch: true, expectEscalate: boolPtr(false), expectIntent: "question"},
		{name: "stock-question", kind: "turn", msg: "ម៉ាស៊ីនចម្រោះទឹកមាននៅហាងទេ?", reply: "មាន នៅហាងយើងមានគ្រប់ម៉ូដែល។", hasMatch: true, expectEscalate: boolPtr(false), expectIntent: "question"},

		// ===== guard: reply audits =====
		{name: "guard-claims-handoff", kind: "guard", msg: "", reply: "បាទ ខ្ញុំបានប្តូរទៅភ្នាក់ងារមនុស្សរួចរាល់។ គាត់នឹងទំនាក់ទំនងអ្នកឆាប់ៗនេះ។", expectFlags: []string{"promises_handoff"}},
		{name: "guard-source-leak", kind: "guard", msg: "", reply: "តម្លៃគឺ 120$ ទៅ 150$ អាស្រ័យតាមឯកសារ (Source 1) ។", expectFlags: []string{"leaks_sources"}},
		{name: "guard-unsafe-guarantee", kind: "guard", msg: "", reply: "បាទ តម្លៃចុងក្រោយគឺ 99$ ហើយខ្ញុំអាចធានាបានថាម៉ាស៊ីននេះមិនដែលខូចទេ។", expectFlags: []string{"unsafe_claim"}},
		{name: "guard-clean", kind: "guard", msg: "", reply: "ម៉ាស៊ីនចម្រោះ KWF-RO-75 មានលក់នៅហាងយើង។ សូមទំនាក់ទំនងសម្រាប់តម្លៃលម្អិត។", expectFlags: nil},

		// ===== route =====
		{name: "route-greeting", kind: "route", msg: "សួស្តី", expectRoute: platform.RouteSmallTalk},
		{name: "route-price", kind: "route", msg: "តម្លៃ KWF-RO-75 ប៉ុន្មាន?", expectRoute: platform.RouteKBQuestion},
		{name: "route-human", kind: "route", msg: "ឱ្យខ្ញុំនិយាយជាមួយមនុស្ស", expectRoute: platform.RouteHandoff},
		{name: "route-order", kind: "route", msg: "ខ្ញុំចង់បញ្ជាទិញ 10 គ្រឿងសម្រាប់ក្រុមហ៊ុន", expectRoute: platform.RouteTransaction},

		// ===== notify triage =====
		{name: "notify-ok", kind: "notify", msg: "ok 👍", expectWorth: boolPtr(false)},
		{name: "notify-thanks", kind: "notify", msg: "អរគុណ!", expectWorth: boolPtr(false)},
		{name: "notify-broken", kind: "notify", msg: "ម៉ាស៊ីនខូចមុនផុតកំណត់ធានាគ្រាន់តែ ១ សប្តាហ៍", expectWorth: boolPtr(true)},

		// ===== rerank relevance ordering =====
		{name: "rerank-ordering", kind: "rerank", msg: "តម្លៃ KWF-RO-75 ប៉ុន្មាន?", expectBest: 0},
	}
}

// runLive executes the battery against the real API and prints per-case
// verdicts with raw probabilities and latencies.
func runLive(workers int) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	pipe := &platform.Pipeline{Jev: typesafe.NewFromEnv(logger), Logger: logger}
	if !pipe.Jev.Enabled() {
		fmt.Fprintln(os.Stderr, "TYPESAFE_API_KEY not set")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cases := liveCases()
	out := make([]liveResult, len(cases))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out[i] = runLiveCase(ctx, pipe, cases[i])
			}
		}()
	}
	for i := range cases {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	passCount, total := 0, 0
	latencies := map[string][]time.Duration{}
	for _, r := range out {
		total++
		if r.ok {
			passCount++
		}
		latencies[r.c.kind] = append(latencies[r.c.kind], r.latency)
		mark := "PASS"
		if !r.ok {
			mark = "FAIL"
		}
		fmt.Printf("[%s] %-24s %s", mark, r.c.name, r.detail)
		if r.sentinel != "" {
			fmt.Printf("  (%s)", r.sentinel)
		}
		fmt.Printf("  %dms\n", r.latency.Milliseconds())
	}
	fmt.Printf("\n%d/%d passed\n", passCount, total)
	for kind, lats := range latencies {
		sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
		p50, p95 := lats[len(lats)/2], lats[(len(lats)*95)/100]
		fmt.Printf("latency %-7s p50=%dms p95=%dms\n", kind, p50.Milliseconds(), p95.Milliseconds())
	}
}

type liveResult struct {
	c        liveCase
	ok       bool
	detail   string
	latency  time.Duration
	sentinel string // raw noul / confidence insight
}

func runLiveCase(ctx context.Context, pipe *platform.Pipeline, c liveCase) liveResult {
	start := time.Now()
	ok, detail, sentinel := judgeLiveCase(ctx, pipe, c)
	return liveResult{c: c, ok: ok, detail: detail, latency: time.Since(start), sentinel: sentinel}
}

func judgeLiveCase(ctx context.Context, pipe *platform.Pipeline, c liveCase) (bool, string, string) {
	switch c.kind {
	case "turn":
		v, topic, raw, ok := pipe.JudgeTurnJev(ctx, c.msg, c.reply, c.hasMatch)
		if !ok {
			return false, "jev call failed", ""
		}
		tr, reason := platform.TurnTriggerFor(v, raw, c.hasMatch, true)
		escalate := tr != ""
		var issues []string
		if c.expectEscalate != nil && escalate != *c.expectEscalate {
			issues = append(issues, fmt.Sprintf("escalate=%t want %t", escalate, *c.expectEscalate))
		}
		if c.expectIntent != "" && v.Intent != c.expectIntent {
			issues = append(issues, fmt.Sprintf("intent=%s want %s", v.Intent, c.expectIntent))
		}
		if c.expectSent != "" && v.Sentiment != c.expectSent {
			issues = append(issues, fmt.Sprintf("sentiment=%s want %s", v.Sentiment, c.expectSent))
		}
		detail := fmt.Sprintf("esc=%t intent=%s sent=%s topic=%s", escalate, v.Intent, v.Sentiment, topic)
		if tr != "" {
			detail += " [" + tr + "]"
		}
		if len(issues) > 0 {
			detail += "  ✗ " + strings.Join(issues, ", ")
		}
		return len(issues) == 0, detail, fmt.Sprintf("noul=%.2f conf=%.2f %s", raw, v.Confidence, shortReason(reason))

	case "guard":
		g, ok := pipe.GuardReply(ctx, c.reply, nil)
		if !ok {
			return false, "jev call failed", ""
		}
		got := map[string]bool{"promises_handoff": g.PromisesHandoff, "leaks_sources": g.LeaksSources, "unsafe_claim": g.UnsafeClaim}
		want := map[string]bool{}
		for _, f := range c.expectFlags {
			want[f] = true
		}
		var issues []string
		var parts []string
		for _, f := range []string{"promises_handoff", "leaks_sources", "unsafe_claim"} {
			parts = append(parts, fmt.Sprintf("%s=%t", f, got[f]))
			if want[f] != got[f] {
				issues = append(issues, fmt.Sprintf("%s=%t want %t", f, got[f], want[f]))
			}
		}
		detail := strings.Join(parts, " ")
		if len(issues) > 0 {
			detail += "  ✗ " + strings.Join(issues, ", ")
		}
		return len(issues) == 0, detail, ""

	case "route":
		r, ok := pipe.RouteInbound(ctx, c.msg)
		if !ok {
			return false, "jev call failed", ""
		}
		escalate, skipGround, silent := platform.RouteDecision(r.Route, r.Prob)
		detail := fmt.Sprintf("route=%s p=%.2f urgency=%s", r.Route, r.Prob, r.Urgency)
		pass := r.Route == c.expectRoute
		if escalate || skipGround || silent {
			detail += fmt.Sprintf(" (escalate=%t skipGround=%t silent=%t)", escalate, skipGround, silent)
		}
		if !pass {
			detail += fmt.Sprintf("  ✗ want %s", c.expectRoute)
		}
		return pass, detail, ""

	case "notify":
		worth := pipe.WorthPinging(ctx, c.msg)
		detail := fmt.Sprintf("worth=%t", worth)
		pass := c.expectWorth == nil || worth == *c.expectWorth
		if !pass {
			detail += fmt.Sprintf("  ✗ want %t", *c.expectWorth)
		}
		return pass, detail, ""

	case "rerank":
		return judgeLiveRerank(ctx, pipe, c)
	}
	return false, "unknown kind " + c.kind, ""
}

// judgeLiveRerank asks the same Score questions the shipped reranker uses and
// checks the expected passage wins. Passages: 0 = exact price answer,
// 1 = related product line, 2 = unrelated greeting.
func judgeLiveRerank(ctx context.Context, pipe *platform.Pipeline, c liveCase) (bool, string, string) {
	passages := []string{
		"KWF-RO-75 ម៉ាស៊ីនចម្រោះទឹក តម្លៃ 250$ មួយគ្រឿង។ រួមទាំងអ្នកតំណែង។",
		"KWF-COM-300 គឺជាម៉ាស៊ីនចម្រោះទឹកសម្រាប់ស្ថាប័ន។ ទំនាក់ទំនងសម្រាប់ព័ត៌មានលម្អិត។",
		"សូមស្វាគមន៍មកកាន់ហាងយើង។ យើងបើកចាប់ពីម៉ោង ៨ ដល់ ១៧។",
	}
	questions := make(map[string]typesafe.Question, len(passages))
	state := map[string]any{"query": c.msg}
	for i, p := range passages {
		state[fmt.Sprintf("p%d", i)] = p
		questions[fmt.Sprintf("p%d", i)] = typesafe.Score(
			fmt.Sprintf("How relevant is passage `p%d` to `query`?", i),
			[]string{"Irrelevant: another topic, product, or document", "Marginal", "Partial", "Mostly", "Exact: directly and completely answers the query"})
	}
	resp, err := pipe.Jev.Judge(ctx, state, questions)
	if err != nil {
		return false, "jev call failed: " + err.Error(), ""
	}
	scores := map[int]float64{}
	detailParts := make([]string, 0, len(passages))
	best, bestScore := 0, -1.0
	for i := range passages {
		v, _ := resp.ScoreValue(fmt.Sprintf("p%d", i))
		scores[i] = v
		detailParts = append(detailParts, fmt.Sprintf("p%d=%.1f", i, v))
		if v > bestScore {
			best, bestScore = i, v
		}
	}
	detail := "scores " + strings.Join(detailParts, " ")
	pass := best == c.expectBest
	if !pass {
		detail += fmt.Sprintf("  ✗ best=p%d want p%d", best, c.expectBest)
	}
	return pass, detail, ""
}

func shortReason(r string) string {
	if len(r) > 60 {
		return r[:60] + "…"
	}
	return r
}
