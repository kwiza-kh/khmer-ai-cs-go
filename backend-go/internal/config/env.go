package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Shared environment readers, for every package outside this one.
//
// config.Load keeps its own internal env()/envInt()/envBool() helpers, and that
// is deliberate: Load feeds credential parsing (JWT_SECRET, R2_SECRET_KEY,
// *_CLIENT_SECRET), so it returns values VERBATIM — silently trimming padding off
// a secret would invalidate every token signed with it. The readers below trim,
// which is what the rest of this tree wants and what the five near-identical
// copies they replace (db, replycache, rag, platform, gemini) already did, except
// for platform's envFloat/envMillis: those skipped the trim, so a knob written as
// " 0.70" fell back to the default instead of being honoured. Trimming fixes that.
//
// The rule of thumb: bytes that authenticate or identify use Load's helpers;
// numbers, durations and feature toggles use these.

// EnvText returns a trimmed, non-empty string override, or fallback.
func EnvText(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// EnvInt returns the parsed override, or fallback when unset or malformed.
func EnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

// EnvPositiveInt is EnvInt for knobs where zero or a negative value would turn
// the feature off rather than tighten it: unset, malformed and non-positive all
// keep the default, so a typo cannot silently disable what the knob bounds.
func EnvPositiveInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// EnvFloat returns the parsed override, or fallback when unset or malformed.
func EnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return fallback
}

// EnvMillis reads a millisecond count. Unset, unparseable and non-positive
// values keep the default — a zero budget would silently disable the feature it
// bounds rather than tighten it.
func EnvMillis(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return fallback
}
