package typesafe

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// keepWarmDefaultSec is the probe interval. It must stay well under Go's
// http.Transport IdleConnTimeout (90s) or the pooled connection dies between
// probes and the warming accomplishes nothing.
const keepWarmDefaultSec = 45

func (c *Client) log() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// KeepWarmInterval resolves JEV_KEEPWARM_SEC. 0 disables the warmer; an
// unparseable or negative value keeps the default.
func KeepWarmInterval() time.Duration {
	v := os.Getenv("JEV_KEEPWARM_SEC")
	if v == "" {
		return keepWarmDefaultSec * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return keepWarmDefaultSec * time.Second
	}
	return time.Duration(n) * time.Second
}

// StartKeepWarm holds a pooled TLS connection to System One open so that real
// turns do not pay the handshake.
//
// Measured on the production host: a request on a reused connection costs
// 0.22-0.42s, while a fresh connection costs 0.64-4.24s — the handshake alone
// ranged 0.44-3.62s. Jev is ~9x faster than the fast model for the same
// judgment (354ms vs 3234ms), but that advantage is inverted whenever the pool
// is cold, because the reply-path budgets (route 4s, guard 3s) then expire and
// the caller falls back to the slower model it was meant to replace.
//
// Production traffic is a handful of messages a day, so every turn would
// otherwise be a cold one. A probe every interval keeps the connection, and
// the TLS session ticket, reusable.
//
// Safe to call once at startup; returns immediately. A no-op on a disabled
// client or a non-positive interval.
func (c *Client) StartKeepWarm(ctx context.Context, interval time.Duration) {
	if !c.Enabled() || interval <= 0 {
		return
	}
	go func() {
		probe := func(first bool) {
			t0 := time.Now()
			_, err := c.Judge(ctx, "hello", map[string]Question{
				"probe": Noul("Is `hello` a greeting?"),
			})
			took := time.Since(t0)
			if ctx.Err() != nil {
				return // shutting down; not a Jev problem
			}
			switch {
			case err != nil:
				c.log().Warn("jev keep-warm probe failed; next turn may be cold",
					"error", err.Error(), "took", took.Round(time.Millisecond).String())
			case first:
				c.log().Info("jev keep-warm: connection established",
					"took", took.Round(time.Millisecond).String(),
					"interval", interval.String())
			default:
				c.log().Debug("jev keep-warm probe ok",
					"took", took.Round(time.Millisecond).String())
			}
		}

		// Warm before the ticker so the very first customer turn after a
		// restart is already on a hot connection.
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
