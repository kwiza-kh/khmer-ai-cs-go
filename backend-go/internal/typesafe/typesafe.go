// Package typesafe is a thin client for the TypeSafe System One judgment
// model (Jev). It replaces "LLM generates text, code parses it" decision
// steps with typed judgments: every call returns probabilities and
// confidences the caller thresholds in code.
//
// Contract for every call site: a nil client means "feature disabled"
// (TYPESAFE_API_KEY unset), and any error from Judge means "Jev unavailable"
// — the caller must fall back to its previous logic and must never drop the
// decision or block the customer-facing path on this service.
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

const (
	// DefaultEndpoint is the single System One evaluation endpoint.
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	// DefaultModel is the alias the API resolves to the current Jev release.
	DefaultModel = "jev-latest"
	// defaultTimeout bounds one Judge call including retries; callers that
	// sit on the reply path wrap ctx with a shorter budget.
	defaultTimeout = 10 * time.Second
	maxRetries     = 2
)

// Client talks to the System One endpoint. A nil *Client is a valid,
// disabled client.
type Client struct {
	Endpoint string
	APIKey   string
	Model    string
	HTTP     *http.Client
	Logger   *slog.Logger
	// observer is optional; installed via SetHealthObserver before serving.
	observer HealthObserver
}

// NewFromEnv builds a client from TYPESAFE_API_KEY. It returns nil when the
// key is absent so callers can keep a typed nil and skip Jev entirely.
func NewFromEnv(logger *slog.Logger) *Client {
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		return nil
	}
	return &Client{
		Endpoint: DefaultEndpoint,
		APIKey:   key,
		Model:    DefaultModel,
		HTTP:     newHTTPClient(),
		Logger:   logger,
	}
}

// newHTTPClient keeps connections warm across Judge calls.
//
// A zero-value http.Client falls back to http.DefaultTransport, whose
// MaxIdleConnsPerHost is 2. Every concurrent turn past the second therefore
// found its pooled connection already closed and paid a fresh TCP+TLS
// handshake. Measured from the production host: handshake alone 0.44-5.0s,
// versus 0.32-0.42s for a request on a reused connection — the steady-state
// model call is fast, the connection setup is what blew through the
// reply-path budgets. Cloning DefaultTransport keeps its proxy and TLS
// defaults, so only the pooling knobs change.
func newHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 64
	t.MaxIdleConnsPerHost = 32
	t.IdleConnTimeout = 90 * time.Second
	return &http.Client{Timeout: defaultTimeout, Transport: t}
}

// Enabled reports whether the client may be used.
func (c *Client) Enabled() bool { return c != nil && c.APIKey != "" }

// Question is one typed judgment. Criteria is a map[string]string for Choice
// and a []string of ordered level descriptions for Score; Noul ignores it.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Noul asks a yes/no question; the answer is the probability of yes.
func Noul(instructions string) Question {
	return Question{Type: "noul", Instructions: instructions}
}

// Choice asks the model to pick one option from a labelled set.
func Choice(instructions string, criteria map[string]string) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: criteria}
}

// Score asks the model to place the state on an ordered scale.
func Score(instructions string, levels []string) Question {
	return Question{Type: "score", Instructions: instructions, Criteria: levels}
}

type judgeRequest struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Answer is one typed judgment result.
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// Response is the batched result of one Judge call.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// NoulValue returns the yes-probability of a noul answer.
func (r *Response) NoulValue(name string) (float64, bool) {
	if r == nil {
		return 0, false
	}
	a, ok := r.Answers[name]
	if !ok || a.Type != "noul" {
		return 0, false
	}
	return a.Noul, true
}

// ChoiceValue returns the picked option and its confidence.
func (r *Response) ChoiceValue(name string) (string, float64, bool) {
	if r == nil {
		return "", 0, false
	}
	a, ok := r.Answers[name]
	if !ok || a.Type != "choice" {
		return "", 0, false
	}
	return a.Choice, a.Confidence, true
}

// ScoreValue returns the probability-weighted position on the scale.
func (r *Response) ScoreValue(name string) (float64, bool) {
	if r == nil {
		return 0, false
	}
	a, ok := r.Answers[name]
	if !ok || a.Type != "score" {
		return 0, false
	}
	return a.Score, true
}

// Judge sends state plus every question in one parallel batch. state may be
// a string or a JSON-serialisable object; question IDs are for code only and
// never reach the model, so the full meaning must live in each Question.
func (c *Client) Judge(ctx context.Context, state any, questions map[string]Question) (*Response, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("typesafe: client disabled")
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("typesafe: no questions")
	}
	started := time.Now()
	body, err := json.Marshal(judgeRequest{State: state, Model: c.Model, Questions: questions})
	if err != nil {
		return nil, fmt.Errorf("typesafe: marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 429/529 back off; anything else is final.
			select {
			case <-ctx.Done():
				// Only a caller-imposed DEADLINE is a health signal (Jev was too
				// slow for the reply-path budget). A cancellation means shutdown
				// or a hung-up visitor, which says nothing about Jev.
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					c.notifyFailed(ctx.Err())
				}
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		resp, retryable, err := c.doJudge(ctx, body)
		if err == nil {
			c.notifySucceeded(time.Since(started))
			return resp, nil
		}
		lastErr = err
		if !retryable {
			break
		}
	}
	// A CANCELLED caller says nothing about Jev's health: that is server
	// shutdown, or a visitor who hung up mid-turn. Reporting it would page
	// operators on every hangup, and an alert nobody trusts is worse than none.
	// A DEADLINE is the opposite — the call was too slow for the budget its
	// caller set, which is exactly the silent degradation this hook exists to
	// surface. `lastErr` carries the wrapped url.Error, so errors.Is reaches
	// the context error through doJudge's %w.
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(lastErr, context.Canceled) {
		return nil, lastErr
	}
	c.notifyFailed(lastErr)
	return nil, lastErr
}

func (c *Client) doJudge(ctx context.Context, body []byte) (*Response, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("typesafe: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	httpResp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("typesafe: call: %w", err)
	}
	defer httpResp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil {
		return nil, false, fmt.Errorf("typesafe: read body: %w", err)
	}
	switch httpResp.StatusCode {
	case http.StatusOK:
		var out Response
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, false, fmt.Errorf("typesafe: decode response: %w", err)
		}
		return &out, false, nil
	case http.StatusTooManyRequests, 529:
		return nil, true, fmt.Errorf("typesafe: rate limited (%d)", httpResp.StatusCode)
	default:
		return nil, false, fmt.Errorf("typesafe: http %d: %s", httpResp.StatusCode, truncate(string(raw), 200))
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
