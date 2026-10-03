package persona

import (
	"strings"
	"testing"
)

// Two things matter here and neither needs a database: which binding wins, and
// the difference between "no tools" and "every tool".

func TestResolvePrefersTheMostSpecificBinding(t *testing.T) {
	bindings := []Binding{
		{PersonaID: "global-1", Scope: "global"},
		{PersonaID: "conv-1", Scope: "conversation", Target: "conv-A"},
		{PersonaID: "sess-1", Scope: "session", Target: "sess-X"},
		{PersonaID: "sess-other", Scope: "session", Target: "sess-Y"},
	}

	if b, ok := Resolve(bindings, "sess-X", "conv-A"); !ok || b.PersonaID != "sess-1" {
		t.Fatalf("session binding must win: %+v ok=%v", b, ok)
	}
	// A session with no binding of its own falls to the conversation.
	if b, ok := Resolve(bindings, "sess-Z", "conv-A"); !ok || b.PersonaID != "conv-1" {
		t.Fatalf("conversation binding must apply: %+v ok=%v", b, ok)
	}
	// Neither, so the tenant default.
	if b, ok := Resolve(bindings, "sess-Z", "conv-B"); !ok || b.PersonaID != "global-1" {
		t.Fatalf("global default must apply: %+v ok=%v", b, ok)
	}
	// No bindings at all: the caller keeps the existing system prompt.
	if _, ok := Resolve(nil, "s", "c"); ok {
		t.Fatal("no bindings must resolve to nothing")
	}
	// Empty ids must not match a ''-targeted binding by accident.
	if _, ok := Resolve([]Binding{{PersonaID: "x", Scope: "session", Target: ""}}, "", ""); ok {
		t.Fatal("an empty session id must not resolve")
	}
}

func TestResolveIgnoresUnknownScopes(t *testing.T) {
	bindings := []Binding{{PersonaID: "weird", Scope: "tenant", Target: "t"}}
	if _, ok := Resolve(bindings, "s", "c"); ok {
		t.Fatal("an unrecognised scope must never win")
	}
}

func TestDecodeStringsDistinguishesNoneFromAll(t *testing.T) {
	if got := decodeStrings(nil); got != nil {
		t.Fatalf("NULL tools must decode to nil (every tool), got %v", got)
	}
	if got := decodeStrings([]byte(`["search","handoff"]`)); strings.Join(got, ",") != "search,handoff" {
		t.Fatalf("tools = %v", got)
	}
	if got := decodeStrings([]byte(`[]`)); got == nil || len(got) != 0 {
		t.Fatalf("[] must decode to a non-nil empty slice (no tools), got %#v", got)
	}
	if got := decodeStrings([]byte(`not json`)); got != nil {
		t.Fatalf("garbage must fall back to nil, got %v", got)
	}
}

func TestScopesAreOrderedMostSpecificFirst(t *testing.T) {
	if strings.Join(Scopes, ",") != "session,conversation,global" {
		t.Fatalf("Scopes = %v; the slice order IS the precedence", Scopes)
	}
}

func TestStoreWithoutDatabaseIsSafe(t *testing.T) {
	s := NewStore(nil)
	if _, ok, err := s.Get(nil, "x"); err != nil || ok {
		t.Fatalf("Get = %v/%v", ok, err)
	}
	if b, err := s.Bindings(nil, 1); err != nil || b != nil {
		t.Fatalf("Bindings = %v/%v", b, err)
	}
}
