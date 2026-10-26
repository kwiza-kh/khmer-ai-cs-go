package platform

import (
	"context"
	"os"
	"strings"
	"testing"

	"khmer-ai-cs-go/internal/persona"
)

// The four persona consumer points inside the inbound pipeline, and the one place
// the handoff decision may be blocked.
//
// Two layers here, because part of this behaviour only exists against a database:
//   - the decisions are pure and tested directly (personaAllowsHandoff, the render
//     door, the safety-net flag);
//   - the wiring is pinned by a source guard, which is the only way to assert
//     "stageResolvePersona hands the resolver a real conversation id" without
//     standing up Postgres. The guards read this package's own source, so a
//     refactor that drops the wiring fails here instead of in production.

// funcSource returns one function's source text: from its `func` line to the next
// top-level `func`/`type` line.
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

// persona tools gate ONLY the AI's own decision to hand over. The lead's rule for
// this change: a persona may change how the bot talks, never whether a customer
// can reach a human — so tools: [] must not switch off the safety net.
func TestPersonaAllowsHandoffGatesTheAIPathsOnly(t *testing.T) {
	forbidden := &persona.Persona{Tools: []string{}}
	cases := []struct {
		name   string
		per    *persona.Persona
		safety bool
		want   bool
	}{
		{"AI-claimed handoff under tools []", forbidden, false, false},
		{"verdict-based escalation under tools []", forbidden, false, false},
		{"quota exhausted under tools []", forbidden, true, true},
		{"empty model reply under tools []", forbidden, true, true},
		{"unreadable media under tools []", forbidden, true, true},
		{"reply guard / content safety under tools []", forbidden, true, true},
		{"no persona at all", nil, false, true},
		{"tools NULL (every capability)", &persona.Persona{}, false, true},
		{"whitelist of image only", &persona.Persona{Tools: []string{persona.CapabilityImage}}, false, false},
		{"whitelist naming handoff", &persona.Persona{Tools: []string{persona.CapabilityHandoff}}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := personaAllowsHandoff(tc.per, tc.safety); got != tc.want {
				t.Fatalf("personaAllowsHandoff(%v, safetyNet=%v) = %v, want %v", tc.per, tc.safety, got, tc.want)
			}
		})
	}
}

// Every caller of handOffWithoutReply is answering a customer who would otherwise
// get nothing, so the turn it marks must keep escalating even under tools: [].
func TestHandOffWithoutReplyIsMarkedAsTheSafetyNet(t *testing.T) {
	for _, label := range []string{"quota-notice", "no-reply", "empty-reply", "media-unreadable"} {
		turn := &inboundTurn{ReplyLang: "en"}
		turn.handOffWithoutReply(label, "reason for "+label)
		if !turn.HandoffSafetyNet {
			t.Fatalf("%s: handOffWithoutReply must mark the turn as a safety-net handoff", label)
		}
		if !personaAllowsHandoff(&persona.Persona{Tools: []string{}}, turn.HandoffSafetyNet) {
			t.Fatalf("%s: tools [] must not block the safety-net handoff", label)
		}
		if turn.Reply == "" || !turn.ClaimsHandoff {
			t.Fatalf("%s: the canned acknowledgement and the claim must still be set", label)
		}
	}
}

// The image capability decides whether a long reply is rendered: forbidden means
// the same answer goes out as text, and the render service must not even be
// called (which is what the injected renderer records).
func TestPersonaImageCapabilityDecidesTheRender(t *testing.T) {
	t.Setenv("T2I_ENDPOINT", "https://render.example/t2i")
	renderer := &fakeRenderer{data: []byte("PNG"), ct: "image/png"}
	p := &Pipeline{T2I: renderer, mediaUploader: &fakeUploader{}, Logger: quietLogger()}
	cfg := &configCred{UserID: 7, Platform: "zalo"}
	long := strings.Repeat("long answer ", 40)

	content, payload := p.renderableDelivery(context.Background(), cfg, long, nil, true)
	if content != "" || payload["kind"] != "media" {
		t.Fatalf("image allowed: got content %q payload %v, want the rendered media delivery", content, payload)
	}

	content, payload = p.renderableDelivery(context.Background(), cfg, long, nil, false)
	if content != long {
		t.Fatalf("image forbidden: content changed to %q, want the original text", content)
	}
	if payload != nil {
		t.Fatalf("image forbidden: payload = %v, want it untouched", payload)
	}
}

// The conversation scope was implemented and permanently unreachable because the
// only production call site passed "". That literal must never come back: the stage
// hands the resolver the event's channel identity, and the resolver derives the
// conversation id from it.
func TestStageResolvePersonaPassesTheDerivedConversationID(t *testing.T) {
	body := funcSource(t, "inbound_stages.go", "stageResolvePersona")
	if !strings.Contains(body, "p.personaForTurn(") {
		t.Fatal("stageResolvePersona must resolve through personaForTurn")
	}
	if !strings.Contains(body, "t.Event.Platform") || !strings.Contains(body, "t.Event.PlatformUserID") {
		t.Fatal("the conversation id must come from the event's channel identity")
	}
	if strings.Contains(body, `t.SessionID, "")`) {
		t.Fatal("stageResolvePersona passes an empty conversation id again")
	}
	resolver := funcSource(t, "pipeline.go", "personaForTurn")
	if !strings.Contains(resolver, "persona.ConversationID(") {
		t.Fatal("personaForTurn must derive the conversation id; a conversation binding is otherwise dead again")
	}
}

// One enqueue path, one door: the persona's image capability has to reach
// enqueueDelivery, and every delivery has to declare it.
func TestEnqueueDeliveryCarriesTheImageCapability(t *testing.T) {
	body := funcSource(t, "pipeline.go", "enqueueDelivery")
	if !strings.Contains(body, "allowImage bool") {
		t.Fatal("enqueueDelivery must take the turn's image capability")
	}
	if !strings.Contains(body, "p.renderableDelivery(") {
		t.Fatal("enqueueDelivery must route the render decision through renderableDelivery")
	}
	stage := funcSource(t, "inbound_stages.go", "stagePersistAndDeliver")
	if !strings.Contains(stage, "persona.CapabilityImage") {
		t.Fatal("persist-and-deliver must decide the image capability for the customer reply")
	}
}

// error_reply has two consumers. This one is the inbound retry-exhausted branch,
// where the customer would otherwise get silence; it must use the persona's copy
// and deliver through the normal outbox (no side channel, no invented default).
func TestExhaustedInboundEventUsesThePersonaErrorReply(t *testing.T) {
	retry := funcSource(t, "pipeline.go", "retryInbound")
	if !strings.Contains(retry, "p.sendPersonaErrorReply(") {
		t.Fatal("the retry-exhausted branch must try the persona error reply")
	}
	body := funcSource(t, "pipeline.go", "sendPersonaErrorReply")
	for _, want := range []string{"per.ErrorText()", "p.enqueueDelivery(", "'system'"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sendPersonaErrorReply must contain %q", want)
		}
	}
	if strings.Contains(body, "HandoffAcknowledgement") || strings.Contains(body, "error_reply:") {
		t.Fatal("no default error copy may be invented; an unconfigured persona sends nothing")
	}
}
