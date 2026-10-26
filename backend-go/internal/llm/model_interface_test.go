package llm

import (
	"context"
	"strings"
	"testing"

	"khmer-ai-cs-go/internal/anthropic"
	"khmer-ai-cs-go/internal/deepseek"
	"khmer-ai-cs-go/internal/gemini"
)

// ChatStreamAs is part of Model, not an optional capability that call sites assert on.
//
// The reason is a silent-degradation bug that this package can make impossible: the
// console's /chat, /chat/stream and /ai-test and the website widget all STREAM their
// turns, so a provider without the streaming override answers exactly the screens an
// operator uses to check a persona — with the tenant prompt, while the same persona
// applies on every customer channel. When the method was optional (a type assertion at
// the call site) a new client could ship without it and nothing failed.
//
// These assertions are compile-time on purpose: the build breaks the moment a client
// stops satisfying Model, instead of a turn quietly losing its persona at runtime.
var (
	_ Model = (*gemini.Service)(nil)
	_ Model = (*anthropic.Service)(nil)
	_ Model = (*deepseek.Service)(nil)
	_ Model = unavailable{}
)

// A provider whose configuration never loaded must fail the persona surface loudly,
// like every other call: an empty ChatResult would be read as "the model said
// nothing" and the turn would hand off with no trace of the real fault.
func TestUnavailableModelFailsTheStreamingPersonaSurface(t *testing.T) {
	var m Model = unavailable{provider: ProviderDeepSeek}
	var streamed []string
	if _, err := m.ChatStreamAs(context.Background(), "hi", nil, "en", "persona prompt", func(tok string) {
		streamed = append(streamed, tok)
	}); err == nil {
		t.Fatal("an unbuilt provider must not look like a successful streaming turn")
	} else if !strings.Contains(err.Error(), ProviderDeepSeek) {
		t.Fatalf("err = %v, want it to name the provider that is missing", err)
	}
	if len(streamed) != 0 {
		t.Fatalf("streamed %v; a call that never ran must not emit tokens", streamed)
	}
}
