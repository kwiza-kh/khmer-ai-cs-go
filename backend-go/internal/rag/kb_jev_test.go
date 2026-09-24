package rag

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/typesafe"
)

// jevNoulServer answers every noul question with per-index values.
func jevNoulServer(t *testing.T, calls *atomic.Int32, values []float64) *typesafe.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		answers := ""
		for i, v := range values {
			if i > 0 {
				answers += ","
			}
			answers += fmt.Sprintf(`"q%d":{"type":"noul","noul":%s}`, i, strconv.FormatFloat(v, 'f', -1, 64))
			answers += fmt.Sprintf(`,"c%d":{"type":"noul","noul":%s}`, i, strconv.FormatFloat(v, 'f', -1, 64))
			answers += fmt.Sprintf(`,"e%d":{"type":"noul","noul":%s}`, i, strconv.FormatFloat(v, 'f', -1, 64))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{` + answers + `}}`))
	}))
	t.Cleanup(srv.Close)
	return &typesafe.Client{Endpoint: srv.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: srv.Client(), Logger: quietLogger()}
}

func TestConfirmContradictionsDropsUnconfirmed(t *testing.T) {
	var calls atomic.Int32
	s := &Service{Jev: jevNoulServer(t, &calls, []float64{0.9, 0.1}), Logger: quietLogger()}
	items := []compileContradiction{
		{NewClaim: "price 250$", OldClaim: "price 200$", OldDocTitle: "Price list", Severity: "high"},
		{NewClaim: "open 8am", OldClaim: "open 8am daily", OldDocTitle: "Hours", Severity: "low"},
	}
	kept := s.confirmContradictions(context.Background(), "new doc", items)
	if len(kept) != 1 || kept[0].OldDocTitle != "Price list" {
		t.Fatalf("low-noul item must be dropped: %+v", kept)
	}
}

func TestConfirmContradictionsFailOpen(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	s := &Service{Jev: &typesafe.Client{Endpoint: bad.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: bad.Client(), Logger: quietLogger()}, Logger: quietLogger()}
	items := []compileContradiction{{NewClaim: "a", OldClaim: "b", OldDocTitle: "d", Severity: "high"}}
	if kept := s.confirmContradictions(context.Background(), "t", items); len(kept) != 1 {
		t.Fatal("jev failure must keep every item (noisy queue beats silent loss)")
	}
}

func TestSweepContradictionsFlagsNewConflicts(t *testing.T) {
	var calls atomic.Int32
	s := &Service{Jev: jevNoulServer(t, &calls, []float64{0.85, 0.2}), Logger: quietLogger()}
	excerpts := []kbExcerpt{{Title: "Warranty", Content: "1 year"}, {Title: "Delivery", Content: "2 days"}}
	found := s.sweepContradictions(context.Background(), "new doc", excerpts, nil)
	if len(found) != 1 || found[0].OldDocTitle != "Warranty" {
		t.Fatalf("only the flagged excerpt must surface: %+v", found)
	}
	// Already-flagged titles must be skipped entirely: Warranty never
	// re-surfaces, only the remaining excerpt can.
	none := s.sweepContradictions(context.Background(), "new doc", excerpts, []compileContradiction{{OldDocTitle: "Warranty"}})
	for _, f := range none {
		if f.OldDocTitle == "Warranty" {
			t.Fatal("already-flagged title must not be re-flagged")
		}
	}
}

// jevChoiceServer answers the "pick" question with a fixed option id.
func jevChoiceServer(t *testing.T, calls *atomic.Int32, pick string) *typesafe.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"pick":{"type":"choice","choice":"` +
			pick + `","confidence":0.9}}}`))
	}))
	t.Cleanup(srv.Close)
	return &typesafe.Client{Endpoint: srv.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: srv.Client(), Logger: quietLogger()}
}

func TestRewriteQueryJevPicksCarryOverCandidate(t *testing.T) {
	var calls atomic.Int32
	s := &Service{Jev: jevChoiceServer(t, &calls, "q1"), Logger: quietLogger()}
	history := []gemini.HistoryItem{
		{Role: "user", Content: "KWF-RO-75 有现货吗"},
		{Role: "model", Content: "有货"},
	}
	q, decided := s.rewriteQueryJev(context.Background(), "多少钱？", history)
	if !decided || q == nil || *q == "多少钱？" {
		t.Fatalf("a different carry-over candidate expected, got %v decided=%v", q, decided)
	}
}

func TestRewriteQueryJevOriginalBestAndFallbacks(t *testing.T) {
	var calls atomic.Int32
	// Jev picks q0 → (nil, true): original query, no rewrite.
	s := &Service{Jev: jevChoiceServer(t, &calls, "q0"), Logger: quietLogger()}
	history := []gemini.HistoryItem{{Role: "user", Content: "KWF-RO-75 有货吗"}}
	if q, decided := s.rewriteQueryJev(context.Background(), "多少钱", history); !decided || q != nil {
		t.Fatalf("original pick must return (nil,true): %v %v", q, decided)
	}
	// Disabled Jev → (nil, false) so the caller uses the Gemini rewrite.
	s2 := &Service{Jev: nil, Logger: quietLogger()}
	if q, decided := s2.rewriteQueryJev(context.Background(), "多少钱", history); decided || q != nil {
		t.Fatalf("disabled Jev must report undecided: %v %v", q, decided)
	}
}
