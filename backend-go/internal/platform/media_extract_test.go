package platform

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"khmer-ai-cs-go/internal/gemini"
)

// TestExtractMediaTextNamesTheFailedStep pins the marker the inbox shows when a
// customer's image or voice note could not be read. Without it the failure lived
// only in a log line, while the turn carried on with empty content — the one class
// of bug AGENTS.md says must be either retried or recorded.
//
// The endpoint answers 400 (a hard error, no retry budget to wait out) instead of
// being unreachable, so the test stays fast.
func TestExtractMediaTextNamesTheFailedStep(t *testing.T) {
	st := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}`))
	}))
	defer st.Close()
	t.Setenv("GEMINI_API_BASE", st.URL)

	p := &Pipeline{
		Gemini: gemini.New("test-key", "gemini-3.8-flash", 2048),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ev := &InboundEvent{EventID: 7}

	if text, failure := p.extractMediaText(context.Background(), ev, true, []byte("jpeg"), "image/jpeg"); text != "" || failure != "describe_failed" {
		t.Errorf("image = (%q, %q), want an empty text and describe_failed", text, failure)
	}
	if text, failure := p.extractMediaText(context.Background(), ev, false, []byte("ogg"), "audio/ogg"); text != "" || failure != "transcribe_failed" {
		t.Errorf("audio = (%q, %q), want an empty text and transcribe_failed", text, failure)
	}
}
