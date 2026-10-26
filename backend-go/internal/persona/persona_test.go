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

// The conversation scope is derived from the sessions row (there is no
// conversation column), so these three rules ARE the feature: a binding an
// operator saves today has to match the id this function computes tomorrow.
func TestConversationIDRules(t *testing.T) {
	cases := []struct {
		name                     string
		platform, platformUserID string
		sessionID                string
		want                     string
	}{
		{"channel customer", "telegram", "123456789", "sess-1", "telegram:123456789"},
		{"whitespace trimmed", " messenger ", " 42 ", "sess-1", "messenger:42"},
		{"no platform id degrades to the session", "web", "", "sess-1", "web:session:sess-1"},
		{"console session (platform NULL)", "", "", "sess-1", "console:sess-1"},
		{"console ignores a stray platform id", "", "42", "sess-1", "console:sess-1"},
		{"nothing addressable", "telegram", "", "", ""},
		{"nothing addressable at all", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConversationID(tc.platform, tc.platformUserID, tc.sessionID); got != tc.want {
				t.Fatalf("ConversationID(%q, %q, %q) = %q, want %q",
					tc.platform, tc.platformUserID, tc.sessionID, got, tc.want)
			}
		})
	}
}

// The whole point of the middle scope: the same customer keeps their persona
// across threads, while a console conversation is its session and nothing more.
func TestConversationIDIsStableAcrossSessions(t *testing.T) {
	first := ConversationID("telegram", "123456789", "session-one")
	second := ConversationID("telegram", "123456789", "session-two")
	if first != second {
		t.Fatalf("same customer gave %q and %q; a conversation must survive a new session", first, second)
	}
	if ConsoleConversation("s1") == ConsoleConversation("s2") {
		t.Fatal("two console sessions must not share a conversation")
	}
	if ConsoleConversation("s1") != "console:s1" {
		t.Fatalf("ConsoleConversation = %q", ConsoleConversation("s1"))
	}
}

// Conversation bound → beats global; session bound too → session still wins. The
// last case is the integration that used to be dead: the id the resolver is
// handed is the one ConversationID produces.
func TestResolveConversationBeatsGlobalButLosesToSession(t *testing.T) {
	bindings := []Binding{
		{PersonaID: "global-1", Scope: "global"},
		{PersonaID: "conv-1", Scope: "conversation", Target: "telegram:42"},
	}
	if b, ok := Resolve(bindings, "sess-A", "telegram:42"); !ok || b.PersonaID != "conv-1" {
		t.Fatalf("conversation binding must beat the tenant default: %+v ok=%v", b, ok)
	}
	withSession := append(append([]Binding(nil), bindings...),
		Binding{PersonaID: "sess-1", Scope: "session", Target: "sess-A"})
	if b, ok := Resolve(withSession, "sess-A", "telegram:42"); !ok || b.PersonaID != "sess-1" {
		t.Fatalf("session binding must still beat the conversation: %+v ok=%v", b, ok)
	}
	if b, ok := Resolve(bindings, "sess-B", "telegram:99"); !ok || b.PersonaID != "global-1" {
		t.Fatalf("another customer's turn must fall to the tenant default: %+v ok=%v", b, ok)
	}
	// Deriving the id on a brand-new session must match the operator's binding.
	if b, ok := Resolve(bindings, "sess-new", ConversationID("telegram", "42", "sess-new")); !ok || b.PersonaID != "conv-1" {
		t.Fatalf("a derived conversation id must match its binding: %+v ok=%v", b, ok)
	}
}

// tools is three-valued and read through Allows: nil (SQL NULL) allows
// everything, [] forbids everything, a whitelist allows exactly its names.
func TestPersonaAllowsTheThreeValuedTools(t *testing.T) {
	cases := []struct {
		name string
		per  *Persona
		cap  string
		want bool
	}{
		{"no persona at all", nil, CapabilityHandoff, true},
		{"tools NULL allows everything", &Persona{}, CapabilityImage, true},
		{"tools [] forbids handoff", &Persona{Tools: []string{}}, CapabilityHandoff, false},
		{"tools [] forbids image", &Persona{Tools: []string{}}, CapabilityImage, false},
		{"whitelist allows what it names", &Persona{Tools: []string{CapabilityHandoff}}, CapabilityHandoff, true},
		{"whitelist forbids the rest", &Persona{Tools: []string{CapabilityHandoff}}, CapabilityImage, false},
		{"case and spaces do not matter", &Persona{Tools: []string{"Handoff", " IMAGE "}}, CapabilityImage, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.per.Allows(tc.cap); got != tc.want {
				t.Fatalf("Allows(%q) = %v, want %v", tc.cap, got, tc.want)
			}
		})
	}
}

// NormalizeTools is what the API stores, so it must not change the meaning of any
// of the three states while canonicalising the names the API accepts.
func TestNormalizeTools(t *testing.T) {
	if got, unknown := NormalizeTools(nil); got != nil || unknown != nil {
		t.Fatalf("nil = (%#v, %#v), want nil/nil (every capability)", got, unknown)
	}
	got, unknown := NormalizeTools([]string{})
	if got == nil || len(got) != 0 || unknown != nil {
		t.Fatalf("[] = (%#v, %#v), want an empty non-nil slice (no capability)", got, unknown)
	}
	got, unknown = NormalizeTools([]string{" IMAGE ", "handoff", "image"})
	if strings.Join(got, ",") != "handoff,image" || unknown != nil {
		t.Fatalf("normalised = %v unknown = %v, want the fixed order, deduped", got, unknown)
	}
	got, unknown = NormalizeTools([]string{"search", "handoff", "SEARCH", "browser"})
	if strings.Join(got, ",") != "handoff" {
		t.Fatalf("normalised = %v, want only the implemented capability", got)
	}
	if strings.Join(unknown, ",") != "search,browser" {
		t.Fatalf("unknown = %v, want the unimplemented names once each, in order", unknown)
	}
}

// error_reply is only a customer-facing line when somebody wrote one.
func TestErrorTextIsEmptyUnlessConfigured(t *testing.T) {
	var noPersona *Persona
	if got := noPersona.ErrorText(); got != "" {
		t.Fatalf("nil persona ErrorText = %q, want \"\"", got)
	}
	if got := (&Persona{}).ErrorText(); got != "" {
		t.Fatalf("unconfigured ErrorText = %q, want \"\"", got)
	}
	if got := (&Persona{ErrorReply: "  សូមទោស  "}).ErrorText(); got != "សូមទោស" {
		t.Fatalf("ErrorText = %q, want it trimmed", got)
	}
}
