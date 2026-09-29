// Command jeveval shadows Jev's turn judgment over real historical turns and
// scores it against handoff ground truth, so the escalation threshold is
// calibrated on this product's data instead of copied from docs.
//
// Input: a CSV exported from the production DB (see docs/DEVELOPMENT.md,
// "Jev 校准") with columns message_id,session_id,created_at,customer_msg,
// reply,has_match,escalated.
//
// Usage: TYPESAFE_API_KEY=… go run ./cmd/jeveval -csv turns.csv
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"khmer-ai-cs-go/internal/db"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/typesafe"
)

type turn struct {
	customerMsg string
	reply       string
	hasMatch    bool
	escalated   bool   // ground truth: a handoff request followed within 30 min
	trigger     string // which layer created that handoff ("" when none)
	userID      int32
}

type judged struct {
	t          turn
	topic      string
	raw        float64
	ok         bool
	intent     string
	sentiment  string
	confidence float64
}

func main() {
	csvPath := flag.String("csv", "", "path to the exported turns CSV")
	workers := flag.Int("workers", 8, "parallel Jev calls")
	mode := flag.String("mode", "threshold", "threshold | agree | rerank | live | khmer | speed | reply")
	gate := flag.String("gate", "stack", "decision policy to scan: stack | noul | jev")
	// -mode reply only. min-score is not called -gate because -gate already names
	// the Jev decision policy above, and two meanings on one flag is how a
	// calibrated threshold gets silently reused as a quality bar.
	replyEval := flag.String("eval", "", "-mode reply: path to the reply eval JSON")
	replyMinScore := flag.Float64("min-score", 8.0, "-mode reply: minimum judge total out of 12")
	replyCategory := flag.String("category", "", "-mode reply: only run cases in this category")
	replyJudge := flag.Bool("judge", true, "-mode reply: run the rubric judge (needs GEMINI_MODEL fast model)")
	replyUser := flag.Int("user", 1, "-mode reply: tenant user id for retrieval")
	verbose := flag.Bool("v", false, "-mode reply: print each reply")
	dump := flag.Bool("dump", false, "-mode reply: print the grounding context as the model received it")
	// Negative means "keep the deployment's own temperature". Temperatures are >= 0
	// by definition, so no separate bool is needed and the flag reads as the arm it
	// selects: -temperature 0.7.
	replyTemperature := flag.Float64("temperature", -1, "-mode reply: override the sampling temperature for this run only")
	flag.Parse()
	switch *mode {
	case "reply":
		runReply(evalOptions{
			evalPath: *replyEval,
			category: *replyCategory,
			userID:   int32(*replyUser),
			gate:     *replyMinScore,
			judge:    *replyJudge,
			verbose:  *verbose,
			dump:     *dump,

			temperature: *replyTemperature,
		})
		return
	case "agree":
		runAgree(*csvPath, *workers)
		return
	case "rerank":
		runRerank(*csvPath, *workers)
		return
	case "live":
		runLive(*workers)
		return
	case "khmer":
		runKhmer(*workers)
		return
	case "speed":
		runSpeed(*workers)
		return
	}
	if *csvPath == "" {
		fmt.Fprintln(os.Stderr, "usage: jeveval -csv turns.csv")
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pipe := &platform.Pipeline{Jev: typesafe.NewFromEnv(logger), Logger: logger}
	if !pipe.Jev.Enabled() {
		fmt.Fprintln(os.Stderr, "TYPESAFE_API_KEY not set")
		os.Exit(2)
	}

	turns, err := loadTurns(*csvPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load csv:", err)
		os.Exit(1)
	}
	fmt.Printf("turns=%d workers=%d\n", len(turns), *workers)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	out := make([]judged, len(turns))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				t := turns[i]
				v, topic, raw, ok := pipe.JudgeTurnJev(ctx, t.customerMsg, t.reply, t.hasMatch)
				out[i] = judged{t: t, topic: topic, raw: raw, ok: ok,
					intent: v.Intent, sentiment: v.Sentiment, confidence: v.Confidence}
			}
		}()
	}
	for i := range turns {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var failed, positives int
	topics := map[string]int{}
	intents := map[string]int{}
	for _, j := range out {
		if !j.ok {
			failed++
			continue
		}
		topics[j.topic]++
		intents[j.intent]++
		if j.t.escalated {
			positives++
		}
	}
	fmt.Printf("jev failures=%d/%d  ground-truth escalations=%d\n", failed, len(out), positives)
	fmt.Println("topic distribution:", topics)
	fmt.Println("intent distribution:", intents)

	fmt.Println("\n== all ground-truth handoffs ==")
	scanTable(out, *gate, func(t turn) bool { return true })
	fmt.Println("\n== classifier-owned population (keyword customer_request turns excluded; negatives kept) ==")
	bestF1, bestTh := scanTable(out, *gate, func(t turn) bool {
		return t.trigger != "customer_request"
	})
	fmt.Printf("\nsubset best F1=%.3f at threshold=%.2f (current default JEV_TURN_ESCALATE_MIN=0.60)\n", bestF1, bestTh)
}

// scanTable prints the confusion matrix per candidate threshold over the
// selected turns and returns the best F1 and its threshold.
func scanTable(out []judged, gate string, include func(turn) bool) (float64, float64) {
	fmt.Println("threshold  TP  FP  FN  TN  precision  recall  F1")
	bestF1, bestTh := -1.0, 0.0
	for th := 0.30; th <= 0.95; th += 0.05 {
		var tp, fp, fn, tn int
		for _, j := range out {
			if !j.ok || !include(j.t) {
				continue
			}
			decide := verdictGate(j, th, gate)
			if decide && j.t.escalated {
				tp++
			} else if decide && !j.t.escalated {
				fp++
			} else if !decide && j.t.escalated {
				fn++
			} else {
				tn++
			}
		}
		prec, rec := 0.0, 0.0
		if tp+fp > 0 {
			prec = float64(tp) / float64(tp+fp)
		}
		if tp+fn > 0 {
			rec = float64(tp) / float64(tp+fn)
		}
		f1 := 0.0
		if prec+rec > 0 {
			f1 = 2 * prec * rec / (prec + rec)
		}
		fmt.Printf("%5.2f     %3d %3d %3d %3d   %6.3f   %6.3f  %6.3f\n", th, tp, fp, fn, tn, prec, rec, f1)
		if f1 > bestF1 {
			bestF1, bestTh = f1, th
		}
	}
	return bestF1, bestTh
}

// ruleIntent mirrors TurnTrigger's human-owned intent set plus the
// negative-sentiment rule.
func ruleIntent(j judged) bool {
	switch j.intent {
	case "complaint", "refund", "legal", "customization", "bulk_order":
		return true
	}
	return j.sentiment == "negative"
}

func verdictGate(j judged, th float64, gate string) bool {
	switch gate {
	case "noul":
		return j.raw >= th
	case "confirm":
		// Intent/sentiment rules fire only when the model's own escalate
		// probability corroborates them; the solo-noul path is off.
		if ruleIntent(j) && j.raw >= th {
			return true
		}
		return !j.t.hasMatch && j.confidence < 0.35 && j.intent != "small_talk" && j.intent != ""
	case "jev":
		// Calibrated Jev policy: the model's own escalate probability is the
		// gate; the intent/sentiment rules (written for the fast model's
		// label semantics) are replaced by it. The no-knowledge-base rule
		// stays: it is grounded in retrieval facts, not labels.
		if j.raw >= th {
			return true
		}
		return !j.t.hasMatch && j.confidence < 0.35 && j.intent != "small_talk" && j.intent != ""
	default:
		return verdictAt(j, th)
	}
}

// verdictAt replays the shipped decision stack (TurnTrigger plus the
// thresholded escalate Noul) for one candidate threshold.
func verdictAt(j judged, th float64) bool {
	v := gemini.TurnVerdict{
		Sentiment:  j.sentiment,
		Intent:     j.intent,
		Confidence: j.confidence,
		Escalate:   j.raw >= th,
	}
	trigger, _ := platform.TurnTrigger(v, j.t.hasMatch, true)
	return v.Escalate || trigger != ""
}

func loadTurns(path string) ([]turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("empty csv")
	}
	var out []turn
	for _, row := range rows[1:] {
		if len(row) < 7 {
			continue
		}
		hm, _ := strconv.ParseBool(row[5])
		trigger := ""
		if len(row) > 6 {
			trigger = row[6]
		}
		var uid int64
		if len(row) > 7 {
			uid, _ = strconv.ParseInt(row[7], 10, 32)
		}
		out = append(out, turn{customerMsg: row[3], reply: row[4], hasMatch: hm,
			escalated: trigger != "", trigger: trigger, userID: int32(uid)})
	}
	return out, nil
}

// runAgree shadows Jev against the fast-model JudgeTurn on the same turns:
// the drop-in question is whether the two judges agree, not whether either
// matches the old system's handoffs.
func runAgree(csvPath string, workers int) {
	turns, err := loadTurns(csvPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load csv:", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	gemKey := os.Getenv("GEMINI_API_KEY")
	if gemKey == "" {
		fmt.Fprintln(os.Stderr, "GEMINI_API_KEY not set")
		os.Exit(2)
	}
	gem := gemini.New(gemKey, gemini.FastModel, 2048)
	pipe := &platform.Pipeline{Jev: typesafe.NewFromEnv(logger), Logger: logger}
	if !pipe.Jev.Enabled() {
		fmt.Fprintln(os.Stderr, "TYPESAFE_API_KEY not set")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	type pair struct {
		t                turn
		jv, gv           gemini.TurnVerdict
		jok, gok         bool
		jDecide, gDecide bool
	}
	out := make([]pair, len(turns))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				t := turns[i]
				jv, _, _, jok := pipe.JudgeTurnJev(ctx, t.customerMsg, t.reply, t.hasMatch)
				gv, gok := gem.JudgeTurn(ctx, t.customerMsg, t.reply, t.hasMatch)
				jDec := jok && (jv.Escalate || turnTrigger(jv, t.hasMatch))
				gDec := gok && (gv.Escalate || turnTrigger(gv, t.hasMatch))
				out[i] = pair{t: t, jv: jv, gv: gv, jok: jok, gok: gok, jDecide: jDec, gDecide: gDec}
			}
		}()
	}
	for i := range turns {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var both, intentAgree, sentAgree, escAgree, decAgree int
	var jOnly, gOnly, jFail, gFail int
	var samples []string
	for _, p := range out {
		if !p.jok {
			jFail++
		}
		if !p.gok {
			gFail++
		}
		if !p.jok || !p.gok {
			continue
		}
		both++
		if p.jv.Intent == p.gv.Intent {
			intentAgree++
		}
		if p.jv.Sentiment == p.gv.Sentiment {
			sentAgree++
		}
		if p.jv.Escalate == p.gv.Escalate {
			escAgree++
		}
		if p.jDecide == p.gDecide {
			decAgree++
		} else {
			if p.jDecide {
				jOnly++
			} else {
				gOnly++
			}
			if len(samples) < 12 {
				samples = append(samples, fmt.Sprintf("  jev=%s/%s/esc=%t gem=%s/%s/esc=%t | %s",
					p.jv.Intent, p.jv.Sentiment, p.jv.Escalate, p.gv.Intent, p.gv.Sentiment, p.gv.Escalate,
					truncateRunesLocal(p.t.customerMsg, 60)))
			}
		}
	}
	pct := func(n int) float64 { return 100 * float64(n) / float64(both) }
	fmt.Printf("compared=%d of %d  failures: jev=%d gem=%d\n", both, len(out), jFail, gFail)
	fmt.Printf("intent agreement    %.1f%%\n", pct(intentAgree))
	fmt.Printf("sentiment agreement %.1f%%\n", pct(sentAgree))
	fmt.Printf("escalate agreement  %.1f%%\n", pct(escAgree))
	fmt.Printf("final decision agreement %.1f%%  (jev-escalates-alone=%d, gemini-escalates-alone=%d)\n", pct(decAgree), jOnly, gOnly)
	fmt.Println("decision disagreements (first 12):")
	for _, s := range samples {
		fmt.Println(s)
	}
}

func turnTrigger(v gemini.TurnVerdict, hasMatch bool) bool {
	tr, _ := platform.TurnTrigger(v, hasMatch, true)
	return tr != ""
}

func truncateRunesLocal(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// runRerank A/Bs retrieval with and without the Jev reranker on the live
// production KB (DATABASE_URL must point at it, e.g. via an SSH tunnel):
// top-1 identity and top-3 overlap per query.
func runRerank(csvPath string, workers int) {
	turns, err := loadTurns(csvPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load csv:", err)
		os.Exit(1)
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL not set (tunnel the prod DB first)")
		os.Exit(2)
	}
	gemKey := os.Getenv("GEMINI_API_KEY")
	if gemKey == "" {
		fmt.Fprintln(os.Stderr, "GEMINI_API_KEY not set")
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db:", err)
		os.Exit(1)
	}
	defer pool.Close()
	gem := gemini.New(gemKey, gemini.FastModel, 2048)
	withJev := &rag.Service{DB: pool, Gemini: gem, Logger: logger, Jev: typesafe.NewFromEnv(logger)}
	without := &rag.Service{DB: pool, Gemini: gem, Logger: logger}

	seen := map[string]bool{}
	type q struct {
		uid int32
		msg string
	}
	var queries []q
	for _, t := range turns {
		if !t.hasMatch || seen[t.customerMsg] || len([]rune(t.customerMsg)) < 6 {
			continue
		}
		seen[t.customerMsg] = true
		queries = append(queries, q{uid: t.userID, msg: t.customerMsg})
		if len(queries) >= 60 {
			break
		}
	}
	fmt.Printf("queries=%d\n", len(queries))

	type res struct {
		top1Same   bool
		overlap    float64
		nJev, nGem int
	}
	out := make([]res, len(queries))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				a, errA := withJev.Search(ctx, queries[i].uid, queries[i].msg, 5)
				b, errB := without.Search(ctx, queries[i].uid, queries[i].msg, 5)
				if errA != nil || errB != nil || len(a) == 0 || len(b) == 0 {
					continue
				}
				top1 := a[0].DocID == b[0].DocID
				setA := map[int32]bool{}
				for _, s := range a[:min(3, len(a))] {
					setA[s.DocID] = true
				}
				hit := 0
				for _, s := range b[:min(3, len(b))] {
					if setA[s.DocID] {
						hit++
					}
				}
				out[i] = res{top1Same: top1, overlap: float64(hit) / float64(len(setA)), nJev: len(a), nGem: len(b)}
			}
		}()
	}
	for i := range queries {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var measured, top1 int
	var overlapSum float64
	var jevSources, gemSources int
	for _, r := range out {
		if r.nJev == 0 && r.nGem == 0 {
			continue
		}
		measured++
		if r.top1Same {
			top1++
		}
		overlapSum += r.overlap
		jevSources += r.nJev
		gemSources += r.nGem
	}
	fmt.Printf("measured=%d top1-identity=%.1f%% top3-overlap=%.1f%% avg-sources jev=%.2f gemini=%.2f\n",
		measured, 100*float64(top1)/float64(measured), 100*overlapSum/float64(measured),
		float64(jevSources)/float64(measured), float64(gemSources)/float64(measured))
}
