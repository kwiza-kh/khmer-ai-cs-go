package gemini

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Query-embedding keep-warm.
//
// WHY THIS EXISTS
// The retrieval leg of every grounded turn starts with one query embedding. On
// the multi-region Vertex endpoint that call costs ~11.2s on a cold connection
// and ~1.2s on a warm one (measured 2026-09-26 from the production host, 10
// probes on one connection: 11.23 then 1.15 1.19 0.36 0.31 0.50 1.17 0.31 0.33
// 1.18). The embedding budget is 5s (GEMINI_EMBED_BUDGET_MS), so a cold
// connection means the call is abandoned and Search drops its dense leg,
// answering from lexical+trigram only — a silent quality loss, logged as
// "vector knowledge search failed; retaining lexical results".
//
// Regional endpoints do not have this problem (asia-southeast1 measures 0.3s
// cold), but gemini-3.5-flash-lite is served ONLY from `global` (measured
// 2026-09-26: 15 regions probed, lite present in global alone), so choosing the
// lite model means living on the endpoint that needs a warm pool.
//
// The probe is deliberately NOT GenerateQueryEmbedding: that method caches by
// query text for 5 minutes, so a repeating probe string would be answered from
// the cache without touching the network — warming nothing. embed() is the
// un-cached call the real path makes once its cache misses.

const embedKeepWarmDefaultSec = 45

// EmbedKeepWarmInterval resolves GEMINI_EMBED_KEEPWARM_SEC. 0 disables the
// warmer; unparseable or negative keeps the default. 45s matches the Jev
// warmer and sits well under Go's http.Transport IdleConnTimeout (90s), which
// is what makes the pooled connection survive between probes.
func EmbedKeepWarmInterval() time.Duration {
	v := os.Getenv("GEMINI_EMBED_KEEPWARM_SEC")
	if v == "" {
		return embedKeepWarmDefaultSec * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return embedKeepWarmDefaultSec * time.Second
	}
	return time.Duration(n) * time.Second
}

// StartEmbedKeepWarm holds a pooled TLS connection to the model host open so
// that a customer's first grounding query does not pay the handshake — and,
// on the multi-region endpoint, does not blow the 5s embedding budget before
// it reaches the network.
//
// Safe to call once at startup; returns immediately. A no-op when the service
// is unconfigured (mock mode makes no network call, so there is nothing to
// keep warm) or the interval is non-positive.
func (s *Service) StartEmbedKeepWarm(ctx context.Context, interval time.Duration) {
	if s == nil || !s.IsConfigured() || interval <= 0 {
		return
	}
	go func() {
		probe := func(first bool) {
			t0 := time.Now()
			// The task type is irrelevant to warming; RETRIEVAL_QUERY matches
			// what the reply path sends, so the warmed session is the same one.
			_, err := s.embed(ctx, "keepwarm", "RETRIEVAL_QUERY")
			took := time.Since(t0)
			if ctx.Err() != nil {
				return // shutting down; not a Gemini problem
			}
			switch {
			case err != nil:
				slog.Default().Warn("embed keep-warm probe failed; next grounded turn may be cold",
					"error", err.Error(), "took", took.Round(time.Millisecond).String())
			case first:
				slog.Default().Info("embed keep-warm: connection established",
					"took", took.Round(time.Millisecond).String(),
					"interval", interval.String())
			default:
				slog.Default().Debug("embed keep-warm probe ok",
					"took", took.Round(time.Millisecond).String())
			}
		}

		// Warm before the ticker so the first grounded turn after a restart is
		// already on a hot connection.
		probe(true)

		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				probe(false)
			}
		}
	}()
}
