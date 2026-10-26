package api

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/persona"
)

// The console's /chat, /chat/stream, /ai-test and the website widget now run under
// a persona. These tests pin the two halves of that: the pure prompt/error-copy
// decisions, and — because the handlers need a database to execute — a source
// guard that each of the three generation paths still routes through them.

// funcSource returns one function's source text: from its `func` line to the next
// top-level `func`/`type` line. Used as a wiring guard for code that cannot run
// without Postgres.
func funcSource(t *testing.T, file, name string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, raw := range lines {
		line := strings.TrimSuffix(raw, "\r")
		if start < 0 {
			if strings.HasPrefix(line, "func ") && strings.Contains(line, " "+name+"(") {
				start = i
			}
			continue
		}
		if strings.HasPrefix(line, "func ") || strings.HasPrefix(line, "type ") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	if start < 0 {
		t.Fatalf("%s does not define %s", file, name)
	}
	return strings.Join(lines[start:], "\n")
}

// With no binding the request must be byte-identical to what these surfaces sent
// before personas reached them: no system prompt, the history untouched.
func TestPersonaPromptIsEmptyWithoutABinding(t *testing.T) {
	history := []gemini.HistoryItem{{Role: "user", Content: "hi"}}
	prompt, got := personaPrompt(nil, history)
	if prompt != "" {
		t.Fatalf("systemPrompt = %q, want \"\" (the tenant prompt)", prompt)
	}
	if len(got) != 1 || got[0] != history[0] {
		t.Fatalf("history = %+v, want it untouched", got)
	}
}

// A bound persona replaces the system prompt and leads the history with its opening
// turns, exactly as the inbound pipeline does (positional roles, starting on user).
func TestPersonaPromptLeadsWithTheOpeningTurns(t *testing.T) {
	per := &persona.Persona{
		SystemPrompt: "你是轮胎客服。",
		BeginDialogs: []string{"你好", "您好，请问需要什么？"},
	}
	history := []gemini.HistoryItem{{Role: "user", Content: "q"}}
	prompt, got := personaPrompt(per, history)
	if prompt != per.SystemPrompt {
		t.Fatalf("systemPrompt = %q, want the persona prompt", prompt)
	}
	want := []gemini.HistoryItem{
		{Role: "user", Content: "你好"},
		{Role: "model", Content: "您好，请问需要什么？"},
		{Role: "user", Content: "q"},
	}
	if len(got) != len(want) {
		t.Fatalf("history = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("history[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// error_reply is used only where the customer would otherwise get nothing, and
// nothing is invented when it is not configured.
func TestGenerationErrorText(t *testing.T) {
	const fallback = "生成回答失败"
	if got := generationErrorText(nil, fallback); got != fallback {
		t.Fatalf("no persona = %q, want the existing wording", got)
	}
	if got := generationErrorText(&persona.Persona{}, fallback); got != fallback {
		t.Fatalf("unconfigured error_reply = %q, want the existing wording", got)
	}
	per := &persona.Persona{ErrorReply: " សូមទោស បញ្ហាបច្ចេកទេស "}
	if got := generationErrorText(per, fallback); got != "សូមទោស បញ្ហាបច្ចេកទេស" {
		t.Fatalf("configured error_reply = %q, want the persona copy (trimmed)", got)
	}
}

// The three customer-facing generation paths each own a console/widget surface, so
// each must resolve the persona, apply its prompt and honour its error copy. A
// refactor that drops one of them would answer under the tenant prompt with no
// error anywhere — which is exactly the bug this change fixes.
func TestConsoleAndWidgetTurnsApplyThePersona(t *testing.T) {
	for _, name := range []string{"chatPlain", "chatStream"} {
		body := funcSource(t, "chat_handlers.go", name)
		// The console handlers resolve through personaForScope with the conversation
		// id resolveChatSession derived, so the turn and the id an operator copies
		// out of the inbox cannot drift apart.
		for _, want := range []string{"a.personaForScope(", "conversationID", "personaPrompt(", "generationErrorText("} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s must use %q", name, want)
			}
		}
	}
	widget := funcSource(t, "widget_handlers.go", "widgetChat")
	for _, want := range []string{"a.personaForTurn(", "personaPrompt(", "generationErrorText(", "persona.CapabilityHandoff"} {
		if !strings.Contains(widget, want) {
			t.Fatalf("widgetChat must use %q", want)
		}
	}
	// personaPrompt is the one place begin_dialogs becomes history, so all three
	// surfaces share the positional role rule instead of growing their own copy.
	if !strings.Contains(funcSource(t, "chat_handlers.go", "personaPrompt"), "platform.PersonaHistory(") {
		t.Fatal("personaPrompt must lead the history through platform.PersonaHistory")
	}
	// The streaming call is the one place the persona prompt can be dropped while
	// everything else still looks right.
	for _, name := range []string{"chatStream"} {
		body := funcSource(t, "chat_handlers.go", name)
		if !strings.Contains(body, "a.streamChatAs(") {
			t.Fatalf("%s must stream through streamChatAs so the persona prompt is sent", name)
		}
	}
	if !strings.Contains(widget, "a.streamChatAs(") {
		t.Fatal("widgetChat must stream through streamChatAs so the persona prompt is sent")
	}
	// And the helper itself must reach the provider's persona surface: with the
	// override dropped here every surface upstream still looks wired.
	streamer := funcSource(t, "chat_handlers.go", "streamChatAs")
	if !strings.Contains(streamer, "ChatStreamAs(") {
		t.Fatal("streamChatAs must call ChatStreamAs so the persona prompt reaches the provider")
	}
}

// tools is three-valued on the wire, and the read path must not collapse NULL into
// []: a console that read "no capability" for an untouched persona would write []
// back on the next save and silently disable every capability.
func TestPersonaToolsWireShapeKeepsThreeStates(t *testing.T) {
	marshal := func(p persona.Persona) string {
		t.Helper()
		b, err := json.Marshal(personaToJSON(p, nil))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	if got := marshal(persona.Persona{ID: "p_1"}); !strings.Contains(got, `"tools":null`) {
		t.Fatalf("NULL tools = %s, want \"tools\":null (every capability)", got)
	}
	if got := marshal(persona.Persona{ID: "p_1", Tools: []string{}}); !strings.Contains(got, `"tools":[]`) {
		t.Fatalf("empty tools = %s, want \"tools\":[] (no capability)", got)
	}
	if got := marshal(persona.Persona{ID: "p_1", Tools: []string{persona.CapabilityHandoff}}); !strings.Contains(got, `"tools":["handoff"]`) {
		t.Fatalf("whitelist = %s, want the list", got)
	}

	// Round trip: what the console reads back, then saves, must mean the same thing.
	for _, tc := range []struct {
		name    string
		body    string
		wantNil bool
		wantLen int
	}{
		{"explicit null is every capability", `{"name":"n","system_prompt":"p","tools":null}`, true, 0},
		{"absent is every capability too", `{"name":"n","system_prompt":"p"}`, true, 0},
		{"empty list is no capability", `{"name":"n","system_prompt":"p","tools":[]}`, false, 0},
		{"a list is that whitelist", `{"name":"n","system_prompt":"p","tools":["image"]}`, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req personaRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.body, err)
			}
			p, err := req.toPersona()
			if err != nil {
				t.Fatalf("toPersona: %v", err)
			}
			if tc.wantNil {
				if p.Tools != nil {
					t.Fatalf("tools = %#v, want nil", p.Tools)
				}
				return
			}
			if p.Tools == nil || len(p.Tools) != tc.wantLen {
				t.Fatalf("tools = %#v, want a non-nil list of %d", p.Tools, tc.wantLen)
			}
		})
	}
}

// An unknown capability is a client bug the row must not absorb: persona.Allows
// would never match it, so it would sit there looking enabled.
func TestPersonaToolsRejectUnknownCapability(t *testing.T) {
	req := personaRequest{Name: "n", SystemPrompt: "p", Tools: []string{"search", "image", "SEARCH"}}
	_, err := req.toPersona()
	var apiErr *ApiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an ApiError", err)
	}
	if apiErr.Status != 400 {
		t.Fatalf("status = %d, want 400", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "未知的工具能力：search") {
		t.Fatalf("message = %q, want the offending name (reported once)", apiErr.Message)
	}
	if !strings.Contains(apiErr.Message, "handoff、image") {
		t.Fatalf("message = %q, want the usable set", apiErr.Message)
	}
}

func TestPersonaToolsAreCanonicalised(t *testing.T) {
	req := personaRequest{Name: "n", SystemPrompt: "p", Tools: []string{" IMAGE ", "handoff", "image"}}
	p, err := req.toPersona()
	if err != nil {
		t.Fatalf("toPersona: %v", err)
	}
	if strings.Join(p.Tools, ",") != "handoff,image" {
		t.Fatalf("tools = %v, want the fixed capability order, deduped", p.Tools)
	}
}

// Both session surfaces hand the operator the exact id a conversation binding
// must use, computed from the row they already read (no extra query per row).
func TestSessionSurfacesExposeTheDerivedConversationID(t *testing.T) {
	for _, tc := range []struct{ file, fn string }{
		{"inbox_handlers.go", "listInbox"},
		{"chat_handlers.go", "getSession"},
		{"chat_handlers.go", "createSession"},
	} {
		body := funcSource(t, tc.file, tc.fn)
		if !strings.Contains(body, "persona.ConversationID(") {
			t.Fatalf("%s must expose conversation_id from persona.ConversationID", tc.fn)
		}
	}
}
