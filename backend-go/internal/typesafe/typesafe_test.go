package typesafe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func judgeResponse(t *testing.T) string {
	t.Helper()
	return `{
		"model": "jev-1.13.0",
		"answers": {
			"escalate": {"type": "noul", "noul": 0.87},
			"intent": {"type": "choice", "choice": "refund", "confidence": 0.91,
				"probabilities": {"refund": 0.91, "question": 0.05, "other": 0.04}},
			"relevance": {"type": "score", "score": 3.4, "confidence": 0.8,
				"probabilities": {"0": 0.0, "1": 0.0, "2": 0.1, "3": 0.5, "4": 0.4}}
		},
		"usage": {"input_tokens": 396, "output_tokens": 66}
	}`
}

func newServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := &Client{Endpoint: srv.URL, APIKey: "test-key", Model: DefaultModel, HTTP: srv.Client()}
	return c, srv
}

func TestJudgeParsesAllThreePrimitives(t *testing.T) {
	var gotAuth, gotCT string
	var body map[string]any
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(judgeResponse(t)))
	})

	resp, err := c.Judge(context.Background(), "customer: I want my money back", map[string]Question{
		"escalate":  Noul("Should a human take over?"),
		"intent":    Choice("What is the intent?", map[string]string{"refund": "money back", "question": "product question"}),
		"relevance": Score("How relevant?", []string{"no", "low", "mid", "high", "exact"}),
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if gotAuth != "Bearer test-key" || gotCT != "application/json" {
		t.Fatalf("headers: auth=%q ct=%q", gotAuth, gotCT)
	}
	if body["model"] != DefaultModel {
		t.Fatalf("model not sent: %v", body["model"])
	}
	questions, _ := body["questions"].(map[string]any)
	if questions == nil || len(questions) != 3 {
		t.Fatalf("questions not batched: %v", body["questions"])
	}

	if v, ok := resp.NoulValue("escalate"); !ok || v != 0.87 {
		t.Fatalf("noul: %v %v", v, ok)
	}
	if ch, conf, ok := resp.ChoiceValue("intent"); !ok || ch != "refund" || conf != 0.91 {
		t.Fatalf("choice: %q %v %v", ch, conf, ok)
	}
	if sc, ok := resp.ScoreValue("relevance"); !ok || sc != 3.4 {
		t.Fatalf("score: %v %v", sc, ok)
	}
	if resp.Usage.InputTokens != 396 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestJudgeRetriesOn429ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(judgeResponse(t)))
	})
	if _, err := c.Judge(context.Background(), "x", map[string]Question{"q": Noul("y?")}); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2", calls.Load())
	}
}

func TestJudgeDoesNotRetryOn401(t *testing.T) {
	var calls atomic.Int32
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	})
	if _, err := c.Judge(context.Background(), "x", map[string]Question{"q": Noul("y?")}); err == nil {
		t.Fatal("expected error on 401")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1 (no retry on auth failure)", calls.Load())
	}
}

func TestJudgeHonoursContextDeadline(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(judgeResponse(t)))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Judge(ctx, "x", map[string]Question{"q": Noul("y?")}); err == nil {
		t.Fatal("expected deadline error")
	}
}

func TestDisabledClientIsSafeEverywhere(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Fatal("nil client must be disabled")
	}
	if _, err := c.Judge(context.Background(), "x", map[string]Question{"q": Noul("y?")}); err == nil {
		t.Fatal("disabled client must error, callers fall back")
	}
	// Accessors on a nil response must not panic — fallback paths rely on this.
	var resp *Response
	if _, ok := resp.NoulValue("q"); ok {
		t.Fatal("nil response must report not-ok")
	}
	if _, _, ok := resp.ChoiceValue("q"); ok {
		t.Fatal("nil response must report not-ok")
	}
	if _, ok := resp.ScoreValue("q"); ok {
		t.Fatal("nil response must report not-ok")
	}
}

func TestJudgeRejectsEmptyQuestions(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server must not be called")
	})
	if _, err := c.Judge(context.Background(), "x", nil); err == nil {
		t.Fatal("expected error for empty questions")
	}
}

func TestQuestionWireShape(t *testing.T) {
	raw, err := json.Marshal(map[string]Question{
		"a": Noul("yes?"),
		"b": Choice("pick", map[string]string{"x": "why x"}),
		"c": Score("rate", []string{"low", "high"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["a"]["type"] != "noul" || m["b"]["type"] != "choice" || m["c"]["type"] != "score" {
		t.Fatalf("types: %v", m)
	}
	if _, has := m["a"]["criteria"]; has {
		t.Fatal("noul must omit criteria")
	}
	if crit, ok := m["c"]["criteria"].([]any); !ok || len(crit) != 2 {
		t.Fatalf("score criteria: %v", m["c"]["criteria"])
	}
}
