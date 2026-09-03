package platform

import (
	"strings"
	"testing"
)

func TestSplitPlatformTextShort(t *testing.T) {
	got := SplitPlatformText("hello", 100)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("short text must stay whole: %v", got)
	}
}

func TestSplitPlatformTextLong(t *testing.T) {
	text := strings.Repeat("word ", 1000) // ~5000 runes
	got := SplitPlatformText(text, 4096)
	if len(got) < 2 {
		t.Fatalf("long text must split, got %d chunk(s)", len(got))
	}
	joined := strings.Join(got, "")
	// No runes lost.
	if len([]rune(joined)) != len([]rune(text)) {
		t.Fatalf("split lost runes: %d vs %d", len([]rune(joined)), len([]rune(text)))
	}
}

func TestPlatformTextLimit(t *testing.T) {
	if PlatformTextLimit("telegram") != 4096 {
		t.Error("telegram limit wrong")
	}
	if PlatformTextLimit("whatsapp") != 1024 {
		t.Error("whatsapp limit wrong")
	}
}

func TestHandoffAcknowledgementLanguages(t *testing.T) {
	if handoffAcknowledgement("en") == "" || handoffAcknowledgement("zh") == "" || handoffAcknowledgement("km") == "" {
		t.Fatal("all language acks must be non-empty")
	}
	if handoffAcknowledgement("xx") != handoffAcknowledgement("km") {
		t.Fatal("unknown language must fall back to Khmer")
	}
}

func TestSafeFilename(t *testing.T) {
	if got := safeFilename("a/b\\c\x00d.txt", "audio", 1); strings.ContainsAny(got, "/\\\x00") {
		t.Fatalf("unsafe chars survived: %q", got)
	}
	if got := safeFilename("", "voice", 7); got != "voice-7.bin" {
		t.Fatalf("empty filename must derive: %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdefgh", 5); got != "abcde" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("abc", 5); got != "abc" {
		t.Fatalf("short string must pass through")
	}
}
