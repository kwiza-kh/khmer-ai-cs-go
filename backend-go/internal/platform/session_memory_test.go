package platform

import (
	"strings"
	"testing"
)

// The memory note is incremental: the first one has no "existing note", and a
// later one must carry the previous note forward — otherwise every refresh would
// forget what earlier refreshes learned.
func TestMemoryPromptIncrementalShape(t *testing.T) {
	first := memoryPrompt("", "user: I need 5 boxes\nmodel: noted\n")
	if strings.Contains(first, "Existing note:") {
		t.Fatal("a first note must not claim an existing one")
	}
	if !strings.Contains(first, "New messages:") || !strings.Contains(first, "5 boxes") {
		t.Fatalf("prompt does not carry the new messages: %s", first)
	}
	next := memoryPrompt("- customer wants 5 boxes", "user: make it 6\n")
	if !strings.Contains(next, "Existing note:") || !strings.Contains(next, "5 boxes") {
		t.Fatalf("incremental prompt lost the existing note: %s", next)
	}
	if !strings.Contains(next, "make it 6") {
		t.Fatalf("incremental prompt lost the new messages: %s", next)
	}
}

// The note is injected ahead of the customer's text and must be labelled: the
// model has to know it is memory, not the message being answered.
func TestSessionMemoryLineIsLabelled(t *testing.T) {
	got := sessionMemoryLine("  - customer wants 5 boxes  ")
	if !strings.HasPrefix(got, "【记忆") {
		t.Fatalf("memory line is not labelled: %q", got)
	}
	if !strings.Contains(got, "5 boxes") || strings.HasSuffix(got, "  ") {
		t.Fatalf("memory line = %q", got)
	}
}
