package gemini

import (
	"strings"
	"testing"
)

// Trimming is a front-drop, so it can leave a history that opens on a model turn
// or contains a blank turn. Both are 400s from the provider, and both are
// repaired at the point the request body is built.

func historyTexts(items []HistoryItem) []string {
	out := make([]string, 0, len(items))
	for _, h := range items {
		out = append(out, h.Role+":"+h.Content)
	}
	return out
}

func TestRepairHistoryShapeDropsBlankTurns(t *testing.T) {
	got := RepairHistoryShape([]HistoryItem{
		{Role: "user", Content: "hello"},
		{Role: "model", Content: "   "},
		{Role: "model", Content: ""},
		{Role: "agent", Content: "\n\t"},
		{Role: "model", Content: "hi there"},
	})
	want := []string{"user:hello", "model:hi there"}
	if strings.Join(historyTexts(got), "|") != strings.Join(want, "|") {
		t.Fatalf("repaired = %v, want %v", historyTexts(got), want)
	}
}

func TestRepairHistoryShapeDropsLeadingModelTurns(t *testing.T) {
	got := RepairHistoryShape([]HistoryItem{
		{Role: "model", Content: "orphaned answer"},
		{Role: "model", Content: "another"},
		{Role: "user", Content: "question"},
		{Role: "model", Content: "answer"},
	})
	if len(got) != 2 || got[0].Role != "user" || got[1].Role != "model" {
		t.Fatalf("repaired = %v, want the window to open on the user turn", historyTexts(got))
	}
}

// An "agent" turn is sent as user-role with a marker, so a customer turn
// followed by the staff reply that answered it is two consecutive user turns.
// That is intentional and must survive the repair untouched.
func TestRepairHistoryShapeKeepsConsecutiveSameRoleTurns(t *testing.T) {
	in := []HistoryItem{
		{Role: "user", Content: "where is my order"},
		{Role: "agent", Content: "it ships tomorrow"},
		{Role: "user", Content: "thanks"},
	}
	got := RepairHistoryShape(in)
	if len(got) != len(in) {
		t.Fatalf("repaired = %v, want all three turns kept", historyTexts(got))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("turn %d changed: %v -> %v", i, in[i], got[i])
		}
	}
}

func TestRepairHistoryShapeAllBlankYieldsNothing(t *testing.T) {
	got := RepairHistoryShape([]HistoryItem{{Role: "user", Content: " "}, {Role: "model", Content: ""}})
	if len(got) != 0 {
		t.Fatalf("repaired = %v, want nothing", historyTexts(got))
	}
	if got := RepairHistoryShape(nil); len(got) != 0 {
		t.Fatalf("nil history = %v", historyTexts(got))
	}
}

func TestRepairHistoryShapeIsIdempotent(t *testing.T) {
	in := []HistoryItem{
		{Role: "model", Content: "leading"},
		{Role: "user", Content: "a"},
		{Role: "model", Content: ""},
		{Role: "model", Content: "b"},
	}
	once := RepairHistoryShape(in)
	twice := RepairHistoryShape(once)
	if strings.Join(historyTexts(once), "|") != strings.Join(historyTexts(twice), "|") {
		t.Fatalf("not idempotent: %v then %v", historyTexts(once), historyTexts(twice))
	}
}

// partText reads the single text part of one built content entry.
func partText(t *testing.T, entry map[string]any) (string, string) {
	t.Helper()
	role, _ := entry["role"].(string)
	parts, ok := entry["parts"].([]map[string]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("parts = %#v", entry["parts"])
	}
	text, _ := parts[0]["text"].(string)
	return role, text
}

func TestBuildRequestBodyOpensWithAUserTurn(t *testing.T) {
	s := &Service{}
	body := s.buildRequestBody("current question", []HistoryItem{
		{Role: "model", Content: "orphaned answer"},
		{Role: "user", Content: "earlier question"},
		{Role: "model", Content: "earlier answer"},
	}, "en", "")

	contents, ok := body["contents"].([]map[string]any)
	if !ok || len(contents) != 3 {
		t.Fatalf("contents = %#v, want 3 entries", body["contents"])
	}
	if role, text := partText(t, contents[0]); role != "user" || text != "earlier question" {
		t.Fatalf("first content = %s/%q, want the user's turn", role, text)
	}
	if role, text := partText(t, contents[1]); role != "model" || text != "earlier answer" {
		t.Fatalf("second content = %s/%q", role, text)
	}
	if role, text := partText(t, contents[2]); role != "user" || text != "current question" {
		t.Fatalf("last content = %s/%q, want the current message", role, text)
	}
}

func TestBuildRequestBodyHasNoEmptyParts(t *testing.T) {
	s := &Service{}
	body := s.buildRequestBody("q", []HistoryItem{
		{Role: "user", Content: ""},
		{Role: "model", Content: "   "},
	}, "en", "")

	contents, ok := body["contents"].([]map[string]any)
	if !ok {
		t.Fatalf("contents = %#v", body["contents"])
	}
	for i, entry := range contents {
		if _, text := partText(t, entry); strings.TrimSpace(text) == "" {
			t.Fatalf("content %d carries an empty part: %#v", i, entry)
		}
	}
	if len(contents) != 1 {
		t.Fatalf("blank history must leave only the current message, got %d entries", len(contents))
	}
}

// The chain the task is about: an over-budget history is front-trimmed into a
// window that opens on a model turn, and the request body that actually goes out
// must still be well-formed — with the newest exchange intact.
func TestTruncatedHistoryStillBuildsAWellFormedRequest(t *testing.T) {
	big := strings.Repeat("x", 10000)
	history := []HistoryItem{
		{Role: "user", Content: big},
		{Role: "model", Content: big},
		{Role: "model", Content: big},
		{Role: "user", Content: "still here"},
		{Role: "model", Content: "newest"},
	}

	trimmed := TrimHistoryBudget(history)
	if len(trimmed) == 0 || trimmed[0].Role == "user" {
		t.Fatalf("fixture must trim into a leading model turn, got %v", historyTexts(trimmed))
	}

	s := &Service{}
	body := s.buildRequestBody("current question", trimmed, "en", "")
	contents, ok := body["contents"].([]map[string]any)
	if !ok || len(contents) != 3 {
		t.Fatalf("contents = %#v, want 3 entries", body["contents"])
	}
	if role, _ := partText(t, contents[0]); role != "user" {
		t.Fatalf("request opens with %q; the repair did not run after trimming", role)
	}
	if _, text := partText(t, contents[1]); text != "newest" {
		t.Fatalf("newest surviving turn lost: %q", text)
	}
	if role, text := partText(t, contents[2]); role != "user" || text != "current question" {
		t.Fatalf("last content = %s/%q", role, text)
	}
}
