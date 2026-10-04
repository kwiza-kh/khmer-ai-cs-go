package platform

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/typesafe"
)

// jevTurnServer answers the three turn questions with fixed verdicts.
func jevTurnServer(t *testing.T, noul float64, intent string) *typesafe.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "jev-1.13.0",
			"answers": {
				"sentiment": {"type": "choice", "choice": "negative", "confidence": 0.8},
				"intent": {"type": "choice", "choice": "` + intent + `", "confidence": 0.85},
				"topic": {"type": "choice", "choice": "price", "confidence": 0.7},
				"escalate": {"type": "noul", "noul": ` + fmt.Sprintf("%v", noul) + `}
			}
		}`))
	}))
	t.Cleanup(srv.Close)
	return &typesafe.Client{Endpoint: srv.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: srv.Client(), Logger: slog.New(slog.NewJSONHandler(discardWriter{}, nil))}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// quietLogger keeps fallback warnings out of test output.
func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(discardWriter{}, nil)) }

// geminiFastServer answers the fast-model audit with a fixed JSON verdict so
// the fallback path is observable end to end.
func geminiFastServer(t *testing.T) *gemini.Service {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":` +
			`"{\"sentiment\":\"positive\",\"intent\":\"small_talk\",\"confidence\":0.7,\"escalate\":false}"` +
			`}]}}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GEMINI_API_BASE", srv.URL)
	return gemini.New("test-key", "gemini-test", 1024)
}

func TestJudgeTurnJevPrimary(t *testing.T) {
	p := &Pipeline{Jev: jevTurnServer(t, 0.9, "refund"), Logger: slog.New(slog.NewJSONHandler(discardWriter{}, nil))}
	// Gemini deliberately nil: reaching it would panic, proving Jev is primary.
	v, topic, _, _, ok := p.judgeTurn(context.Background(), "I want my money back", "Our policy says…", true)
	if !ok {
		t.Fatal("jev verdict expected")
	}
	if topic != "price" {
		t.Fatalf("topic tag expected from the jev batch: %q", topic)
	}
	if v.Intent != "refund" || v.Sentiment != "negative" || !v.Escalate {
		t.Fatalf("verdict: %+v", v)
	}
	if v.Confidence != 0.85 {
		t.Fatalf("confidence must come from the intent distribution: %v", v.Confidence)
	}
}

func TestJudgeTurnJevEscalateThresholdIsCalibratable(t *testing.T) {
	t.Setenv("JEV_TURN_ESCALATE_MIN", "0.95")
	p := &Pipeline{Jev: jevTurnServer(t, 0.9, "question"), Logger: slog.New(slog.NewJSONHandler(discardWriter{}, nil))}
	v, _, _, _, ok := p.judgeTurn(context.Background(), "how much?", "It is 5$", true)
	if !ok {
		t.Fatal("jev verdict expected")
	}
	if v.Escalate {
		t.Fatalf("noul 0.9 must stay below the 0.95 bar: %+v", v)
	}
}

func TestJudgeTurnFallsBackWhenJevFails(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	p := &Pipeline{
		Jev:    &typesafe.Client{Endpoint: bad.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: bad.Client(), Logger: slog.New(slog.NewJSONHandler(discardWriter{}, nil))},
		Gemini: geminiFastServer(t),
		Logger: slog.New(slog.NewJSONHandler(discardWriter{}, nil)),
	}
	v, _, _, _, ok := p.judgeTurn(context.Background(), "hi", "hello!", false)
	if !ok {
		t.Fatal("fallback verdict expected")
	}
	if v.Intent != "small_talk" || v.Escalate {
		t.Fatalf("fallback verdict: %+v", v)
	}
}

func TestJudgeTurnFallsBackOnOutOfVocabulary(t *testing.T) {
	p := &Pipeline{
		Jev:    jevTurnServer(t, 0.9, "banana"),
		Gemini: geminiFastServer(t),
		Logger: slog.New(slog.NewJSONHandler(discardWriter{}, nil)),
	}
	v, _, _, _, ok := p.judgeTurn(context.Background(), "hi", "hello!", false)
	if !ok || v.Intent != "small_talk" {
		t.Fatalf("out-of-vocabulary intent must fall back: %+v ok=%v", v, ok)
	}
}

func TestJudgeTurnFallsBackWhenDisabled(t *testing.T) {
	p := &Pipeline{
		Jev:    nil,
		Gemini: geminiFastServer(t),
		Logger: slog.New(slog.NewJSONHandler(discardWriter{}, nil)),
	}
	v, _, _, _, ok := p.judgeTurn(context.Background(), "hi", "hello!", false)
	if !ok || v.Intent != "small_talk" {
		t.Fatalf("disabled Jev must use the fast model: %+v ok=%v", v, ok)
	}
}

func TestTurnTriggerForConfirmsRulesWithNoul(t *testing.T) {
	v := gemini.TurnVerdict{Intent: "refund", Sentiment: "neutral", Confidence: 0.9}
	if tr, _ := TurnTriggerFor(v, 0.8, true, true, "我要退款"); tr != "negative_feedback" {
		t.Fatalf("confirmed rule intent must escalate: %q", tr)
	}
	if tr, _ := TurnTriggerFor(v, 0.3, true, true, "我要退款"); tr != "" {
		t.Fatalf("unconfirmed rule intent must not escalate: %q", tr)
	}
	// The live false positive: a price question with a hot solo noul below the bar.
	price := gemini.TurnVerdict{Intent: "price", Sentiment: "neutral", Confidence: 0.95}
	if tr, _ := TurnTriggerFor(price, 0.6, true, true, "多少钱"); tr != "" {
		t.Fatalf("price question at 0.6 must stay with the AI: %q", tr)
	}
	if tr, _ := TurnTriggerFor(price, 0.95, true, true, "多少钱"); tr != "ai_decision" {
		t.Fatalf("solo noul above the 0.90 bar must escalate: %q", tr)
	}
	// No-KB rule stays unconditional.
	nokb := gemini.TurnVerdict{Intent: "question", Sentiment: "neutral", Confidence: 0.2}
	if tr, _ := TurnTriggerFor(nokb, 0.1, false, true, "你们有这个型号吗"); tr != "no_knowledge_base" {
		t.Fatalf("ungrounded low-confidence answer must escalate: %q", tr)
	}
}
