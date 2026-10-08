package replycache

import (
	"context"
	"testing"
)

// TestReplyCacheServesOnlyTheModelInForce pins the provider switch: an answer the previous
// model wrote is not this deployment's answer after the switch, so it must not be served.
// Same discipline as the other reply-cache tests: DB-gated, throwaway user, cleaned up.
func TestReplyCacheServesOnlyTheModelInForce(t *testing.T) {
	pool := cachePool(t)
	userID := cacheUser(t, pool, "model")
	const dim = 768
	const question = "is the product waterproof?"
	embeds := stubEmbed{question: nearlyOrthogonal(dim, 5)}

	serving := "gemini-old"
	s := &Service{DB: pool, Embed: embeds.embed, Serving: func() string { return serving }}
	ctx := context.Background()

	s.Store(ctx, userID, question, "en", "answer from the old model", "gemini-old")
	if got, hit := s.Lookup(ctx, userID, question, "en"); !hit || got != "answer from the old model" {
		t.Fatalf("same-model lookup = %q hit=%v, want the stored answer", got, hit)
	}

	serving = "claude-haiku-5-5"
	if got, hit := s.Lookup(ctx, userID, question, "en"); hit {
		t.Fatalf("after the switch the previous model's answer was served: %q", got)
	}

	s.Store(ctx, userID, question, "en", "answer from claude", "claude-haiku-5-5")
	if got, hit := s.Lookup(ctx, userID, question, "en"); !hit || got != "answer from claude" {
		t.Fatalf("new-model lookup = %q hit=%v, want its own answer", got, hit)
	}

	// With no model in force the cache must not answer at all: nothing says which model
	// the answer would be attributed to.
	serving = ""
	if _, hit := s.Lookup(ctx, userID, question, "en"); hit {
		t.Fatal("with no serving model the cache answered")
	}
}
