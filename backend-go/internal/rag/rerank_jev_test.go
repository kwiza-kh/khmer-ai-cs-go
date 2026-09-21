package rag

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/typesafe"
)

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(discardWriter{}, nil)) }

// jevRerankServer scores every passage question from the supplied 0-4 values.
func jevRerankServer(t *testing.T, calls *atomic.Int32, scores []float64) *typesafe.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		answers := ""
		for i, sc := range scores {
			if i > 0 {
				answers += ","
			}
			answers += fmt.Sprintf(`"c%d":{"type":"score","score":%v,"confidence":0.9}`, i, sc)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{` + answers + `}}`))
	}))
	t.Cleanup(srv.Close)
	return &typesafe.Client{Endpoint: srv.URL, APIKey: "k", Model: "jev-latest", HTTP: srv.Client(), Logger: quietLogger()}
}

func geminiRerankStub(t *testing.T) *gemini.Service {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"[{\"i\":1,\"score\":7},{\"i\":2,\"score\":2},{\"i\":3,\"score\":1}]"}` +
			`]}}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GEMINI_API_BASE", srv.URL)
	return gemini.New("test-key", "gemini-test", 1024)
}

func TestRerankScoresJevMapsOntoGeminiScale(t *testing.T) {
	var calls atomic.Int32
	s := &Service{Jev: jevRerankServer(t, &calls, []float64{4, 1, 0}), Logger: quietLogger()}
	// Gemini deliberately nil: the Jev path must not touch it.
	scores, ok := s.rerankScoresJev(context.Background(), "price of 5cm boards?", []string{"a", "b", "c"})
	if !ok {
		t.Fatal("jev scores expected")
	}
	want := []float32{10, 2.5, 0}
	for i := range want {
		if scores[i] != want[i] {
			t.Fatalf("scores[%d]=%v want %v (0-4 level ×2.5)", i, scores[i], want[i])
		}
	}
}

func TestRerankScoresFallsBackWhenJevFails(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	s := &Service{
		Jev:    &typesafe.Client{Endpoint: bad.URL, APIKey: "k", Model: "jev-latest", HTTP: bad.Client(), Logger: quietLogger()},
		Gemini: geminiRerankStub(t),
		Logger: quietLogger(),
	}
	scores := s.rerankScores(context.Background(), "q", []string{"a", "b", "c"})
	if len(scores) != 3 || scores[0] != 7 {
		t.Fatalf("fallback scores: %v", scores)
	}
}

func TestRerankScoresFallsBackOnMissingAnswer(t *testing.T) {
	var calls atomic.Int32
	s := &Service{
		Jev:    jevRerankServer(t, &calls, []float64{4, 1}), // three passages, two answers
		Gemini: geminiRerankStub(t),
		Logger: quietLogger(),
	}
	scores := s.rerankScores(context.Background(), "q", []string{"a", "b", "c"})
	if len(scores) != 3 || scores[0] != 7 {
		t.Fatalf("partial jev response must fall back: %v", scores)
	}
}

func TestRerankScoresDisabledJevUsesGemini(t *testing.T) {
	s := &Service{Jev: nil, Gemini: geminiRerankStub(t), Logger: quietLogger()}
	scores := s.rerankScores(context.Background(), "q", []string{"a", "b", "c"})
	if len(scores) != 3 || scores[0] != 7 {
		t.Fatalf("disabled jev must use gemini: %v", scores)
	}
}

func TestRerankCandidatesUsesJevOnceAndCaches(t *testing.T) {
	var calls atomic.Int32
	s := &Service{Jev: jevRerankServer(t, &calls, []float64{4, 3, 2, 1, 0, 0}), Logger: quietLogger()}
	chunks := make([]SearchChunk, 6)
	for i := range chunks {
		chunks[i] = SearchChunk{ChunkID: int32(i + 1), DocID: int32(i + 1), Content: fmt.Sprintf("passage %d about boards", i), Title: "doc"}
	}
	got := s.rerankCandidates(context.Background(), "unique-jev-cache-query", chunks, 2)
	if len(got) != 2 {
		t.Fatalf("topK must cap rerank output: %d", len(got))
	}
	if got[0].ChunkID != 1 || got[1].ChunkID != 2 {
		t.Fatalf("order must follow jev scores: %v %v", got[0].ChunkID, got[1].ChunkID)
	}
	_ = s.rerankCandidates(context.Background(), "unique-jev-cache-query", chunks, 2)
	if calls.Load() != 1 {
		t.Fatalf("second identical rerank must hit the cache, calls=%d", calls.Load())
	}
}
