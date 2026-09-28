package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/typesafe"
)

// Minimal-pair probe: does Jev actually READ Khmer? If it only pattern-matched
// surface shapes, flipping a single word (ចង់ / មិនចង់, ខូច / ល្អ) could not
// flip the judgment direction. Gemini answers the same questions as an
// independent reader; only agreement of two independent models counts as
// evidence.
type khmerPair struct {
	label    string
	a, b     string // a should answer YES, b should answer NO
	question string
}

var khmerPairs = []khmerPair{
	{"want-vs-not", "ខ្ញុំចង់ទិញ", "ខ្ញុំមិនចង់ទិញ", "Does the customer want to buy something?"},
	{"broken-vs-fine", "ម៉ាស៊ីនខូចហើយ", "ម៉ាស៊ីនដំណើរការល្អ", "Is the machine broken?"},
	{"refund-vs-buy", "ខ្ញុំចង់បានប្រាក់សងវិញ", "ខ្ញុំចង់ទិញមួយ", "Is the customer asking for their money back?"},
	{"late-vs-ontime", "ការដឹកជញ្ជូនយឺតពេក", "ការដឹកជញ្ជូនទាន់ពេល", "Is the delivery late?"},
}

// single-word vocabulary probes (English gloss must match the Khmer word)
var khmerWords = []struct {
	word string
	glos string
}{
	{"តម្លៃ", "price"},
	{"ដឹកជញ្ជូន", "delivery or shipping"},
	{"ខូច", "broken or damaged"},
	{"ធានា", "warranty or guarantee"},
}

// controls that should NOT read as "wants to buy"
var khmerControls = []struct {
	label string
	text  string
}{
	{"gibberish", "ស្កកស្កាកស្កុស្កេ"},
	{"greeting-only", "សួស្តី"},
}

func runKhmer(workers int) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	jev := typesafe.NewFromEnv(logger)
	if !jev.Enabled() {
		fmt.Fprintln(os.Stderr, "TYPESAFE_API_KEY not set")
		os.Exit(2)
	}
	gemKey := os.Getenv("GEMINI_API_KEY")
	var gem *gemini.Service
	if gemKey != "" {
		gem = gemini.New(gemKey, gemini.FastModel, 512)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	type row struct {
		label string
		jevA  float64
		jevB  float64
		gemA  string
		gemB  string
	}
	pairRows := make([]row, len(khmerPairs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i, p := range khmerPairs {
		wg.Add(1)
		go func(i int, p khmerPair) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := row{label: p.label}
			var wg2 sync.WaitGroup
			wg2.Add(2)
			go func() { defer wg2.Done(); r.jevA = jevNoul(ctx, jev, p.a, p.question) }()
			go func() { defer wg2.Done(); r.jevB = jevNoul(ctx, jev, p.b, p.question) }()
			if gem != nil {
				wg2.Add(2)
				go func() { defer wg2.Done(); r.gemA = gemYesNo(ctx, gem, p.a, p.question) }()
				go func() { defer wg2.Done(); r.gemB = gemYesNo(ctx, gem, p.b, p.question) }()
			}
			wg2.Wait()
			pairRows[i] = r
		}(i, p)
	}
	wg.Wait()

	fmt.Println("== minimal pairs (a=YES text, b=NO text) ==")
	pairPass, gemPairPass := 0, 0
	for i, p := range khmerPairs {
		r := pairRows[i]
		jevOK := r.jevA >= r.jevB+0.20
		if jevOK {
			pairPass++
		}
		line := fmt.Sprintf("[Jev %s] %-16s a=%.2f b=%.2f  gap=%.2f", yn(jevOK), p.label, r.jevA, r.jevB, r.jevA-r.jevB)
		if gem != nil {
			gemOK := strings.HasPrefix(r.gemA, "1") && strings.HasPrefix(r.gemB, "0")
			if gemOK {
				gemPairPass++
			}
			line += fmt.Sprintf("   [Gemini %s] a=%s b=%s", yn(gemOK), strings.TrimSpace(r.gemA), strings.TrimSpace(r.gemB))
		}
		fmt.Println(line)
	}
	fmt.Printf("\n== vocabulary probes (word→gloss, noul of 'does this word mean X?') ==\n")
	wordPass := 0
	for _, w := range khmerWords {
		q := fmt.Sprintf("Does this Khmer word mean '%s'?", w.glos)
		v := jevNoul(ctx, jev, w.word, q)
		if v >= 0.60 {
			wordPass++
		}
		fmt.Printf("[Jev %s] %-12s %-24s noul=%.2f\n", yn(v >= 0.60), w.word, w.glos, v)
	}
	fmt.Printf("\n== controls (should NOT answer yes to 'wants to buy') ==\n")
	ctrlPass := 0
	for _, c := range khmerControls {
		v := jevNoul(ctx, jev, c.text, "Does the customer want to buy something?")
		if v < 0.30 {
			ctrlPass++
		}
		fmt.Printf("[Jev %s] %-16s noul=%.2f\n", yn(v < 0.30), c.label, v)
	}
	total := len(khmerPairs) + len(khmerWords) + len(khmerControls)
	fmt.Printf("\nJev summary: %d/%d (pairs %d/%d, vocab %d/%d, controls %d/%d)\n",
		pairPass+wordPass+ctrlPass, total, pairPass, len(khmerPairs), wordPass, len(khmerWords), ctrlPass, len(khmerControls))
	if gem != nil {
		fmt.Printf("Gemini cross-check: pairs agreed %d/%d\n", gemPairPass, len(khmerPairs))
	}
}

func yn(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func jevNoul(ctx context.Context, jev *typesafe.Client, state, question string) float64 {
	resp, err := jev.Judge(ctx, map[string]any{"text": state}, map[string]typesafe.Question{
		"q": typesafe.Noul(question),
	})
	if err != nil {
		return -1
	}
	v, _ := resp.NoulValue("q")
	return v
}

func gemYesNo(ctx context.Context, gem *gemini.Service, state, question string) string {
	prompt := "Answer with ONLY the single digit 1 for yes or 0 for no. Question: " + question + "\nText: " + state
	out, ok := gem.GenerateFast(ctx, prompt, 10*time.Second)
	if !ok {
		return "err"
	}
	return strings.TrimSpace(out)
}
