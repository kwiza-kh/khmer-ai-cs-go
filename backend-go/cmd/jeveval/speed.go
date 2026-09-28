package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/typesafe"
)

// runSpeed is the head-to-head the `live` mode cannot give: the same turn
// judgment on the same input through Jev and through the fast model (the
// fallback it is meant to beat), timed sequentially so the number is a real
// single-decision latency and not queueing. Prints both distributions, the
// ratio, and decision agreement — one run answers "is it faster" and "does it
// decide the same thing".
//
// Run it once with GEMINI_FAST_MODEL left at the deployed value, then again
// with the model you are weighing it against; nothing is hardcoded here.
func runSpeed(_ int) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
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
	fmt.Printf("fast model = %s\n", gemini.FastModel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var cases []liveCase
	for _, c := range liveCases() {
		if c.kind == "turn" {
			cases = append(cases, c)
		}
	}

	// Warm both paths first so a cold TLS handshake is not charged to case #1
	// (the whole point of the keep-warm probe is that steady-state is warm).
	_, _, _, _ = pipe.JudgeTurnJev(ctx, "សួស្តី", "", false)
	_, _ = gem.JudgeTurn(ctx, "សួស្តី", "", false)

	type row struct {
		name       string
		jevLat     time.Duration
		gemLat     time.Duration
		jok, gok   bool
		agreesDec  bool
		agreesInt  bool
		jEsc, gEsc bool
	}
	rows := make([]row, 0, len(cases))
	for _, c := range cases {
		t0 := time.Now()
		jv, _, _, jok := pipe.JudgeTurnJev(ctx, c.msg, c.reply, c.hasMatch)
		jl := time.Since(t0)
		t1 := time.Now()
		gv, gok := gem.JudgeTurn(ctx, c.msg, c.reply, c.hasMatch)
		gl := time.Since(t1)

		jDec := jok && (jv.Escalate || turnTrigger(jv, c.hasMatch))
		gDec := gok && (gv.Escalate || turnTrigger(gv, c.hasMatch))
		rows = append(rows, row{
			name: c.name, jevLat: jl, gemLat: gl, jok: jok, gok: gok,
			agreesDec: jDec == gDec, agreesInt: jv.Intent == gv.Intent,
			jEsc: jDec, gEsc: gDec,
		})
		fmt.Printf("%-24s jev=%-6dms gem=%-7dms  jevDec=%-5t gemDec=%-5t intent=%s/%s\n",
			c.name, jl.Milliseconds(), gl.Milliseconds(), jDec, gDec, jv.Intent, gv.Intent)
	}

	pct := func(lats []time.Duration, p float64) time.Duration {
		s := append([]time.Duration(nil), lats...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		idx := int(float64(len(s)-1) * p)
		return s[idx]
	}
	var jl, gGl []time.Duration
	var agreeDec, agreeInt, ok int
	for _, r := range rows {
		if !r.jok || !r.gok {
			continue
		}
		ok++
		jl = append(jl, r.jevLat)
		gGl = append(gGl, r.gemLat)
		if r.agreesDec {
			agreeDec++
		}
		if r.agreesInt {
			agreeInt++
		}
	}
	jP50, jP95 := pct(jl, 0.50), pct(jl, 0.95)
	gP50, gP95 := pct(gGl, 0.50), pct(gGl, 0.95)
	fmt.Printf("\ncompared=%d\n", ok)
	fmt.Printf("jev   p50=%dms p95=%dms\n", jP50.Milliseconds(), jP95.Milliseconds())
	fmt.Printf("gem   p50=%dms p95=%dms\n", gP50.Milliseconds(), gP95.Milliseconds())
	if jP50 > 0 {
		fmt.Printf("speedup (gem p50 / jev p50) = %.2fx\n", float64(gP50)/float64(jP50))
	}
	fmt.Printf("final decision agreement %.1f%%   intent agreement %.1f%%\n",
		100*float64(agreeDec)/float64(ok), 100*float64(agreeInt)/float64(ok))
}
