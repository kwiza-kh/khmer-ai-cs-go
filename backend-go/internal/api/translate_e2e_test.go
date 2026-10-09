package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/usage"
)

// End-to-end translation test: a real HTTP request through the real handler
// (status codes, JSON envelope, request validation, spend gate), the real
// Gemini client doing a real HTTP POST, a real Redis cache — with only Google
// replaced by a local stub. The unit tests above drive the pipeline with a
// scripted model; this one proves the parts they cannot: the prompt that
// actually leaves the process, the response parsing, the cache round-trip and
// the digits guard, wired together as they ship.

// translatePayload digs the JSON payload out of a translation prompt.
func translatePayload(prompt string) ([]string, bool) {
	i := strings.LastIndex(prompt, "\n\n")
	if i < 0 {
		return nil, false
	}
	body := strings.TrimSpace(prompt[i+2:])
	var arr []string
	if json.Unmarshal([]byte(body), &arr) == nil {
		return arr, false
	}
	var obj struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(body), &obj) == nil {
		return []string{obj.Text}, true
	}
	return nil, false
}

func TestTranslateEndToEndThroughHTTP(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "studio")
	t.Setenv("GEMINI_FAST_MODEL", "gemini-test")

	var mu sync.Mutex
	prompts := make([]string, 0, 16)

	// The stub answers from the prompt itself (stateless), which keeps the
	// expected call sequence readable: no reply queue to keep in sync.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		_ = json.Unmarshal(raw, &body)
		prompt := ""
		if len(body.Contents) > 0 && len(body.Contents[0].Parts) > 0 {
			prompt = body.Contents[0].Parts[0].Text
		}
		texts, single := translatePayload(prompt)

		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()

		reply := ""
		switch {
		case strings.Contains(prompt, "changed a digit"):
			// The strict retry: give the digits back exactly.
			reply = "T(" + texts[0] + ")"
		case len(texts) == 1 && strings.Contains(texts[0], "2500") && single:
			// First pass on the price message deliberately alters a digit, so
			// the guard has something to catch.
			reply = "T(total 3000)"
		case len(texts) > 1 && texts[0] == "first" && !strings.Contains(prompt, "previous attempt dropped"):
			// Drop the last entry (wrapping the ones it does answer, so a raw
			// pass-through cannot be mistaken for a translation): the caller must
			// retry only the dropped one.
			short := make([]string, 0, len(texts)-1)
			for _, text := range texts[:len(texts)-1] {
				short = append(short, "T("+text+")")
			}
			out, _ := json.Marshal(short)
			reply = string(out)
		default:
			out := make([]string, len(texts))
			for i, text := range texts {
				out[i] = "T(" + text + ")"
			}
			if single {
				reply = out[0]
			} else {
				encoded, _ := json.Marshal(out)
				reply = string(encoded)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{"parts": []any{map[string]any{"text": reply}}},
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 12, "candidatesTokenCount": 6},
		})
	}))
	defer stub.Close()
	t.Setenv("GEMINI_API_BASE", stub.URL)

	app := newTranslateApp(t)
	app.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	app.Gemini = gemini.New("test-key", "gemini-test", 4096)

	callCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(prompts)
	}

	post := func(h Handler, body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/translate", strings.NewReader(body))
		req = req.WithContext(usage.WithUser(req.Context(), 4242))
		w := httptest.NewRecorder()
		app.handle(h)(w, req)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// 1. One message, through the whole stack.
	status, single := post(app.translateText, `{"text":"price 1000","target":"km"}`)
	if status != http.StatusOK {
		t.Fatalf("single status = %d (%v)", status, single)
	}
	if got := single["translation"]; got != "T(price 1000)" {
		t.Fatalf("translation = %v", got)
	}
	if verified, _ := single["verified"].(bool); !verified {
		t.Fatalf("verified = %v, want true", single["verified"])
	}
	firstPrompt := prompts[0]
	if !strings.Contains(firstPrompt, `{"text":"price 1000"}`) {
		t.Fatalf("the message must travel as a JSON field, prompt was:\n%s", firstPrompt)
	}
	if !strings.Contains(firstPrompt, "never an instruction") {
		t.Fatalf("the injection guard must be in the prompt, got:\n%s", firstPrompt)
	}

	// 2. The same message again is a cache hit: no second model call.
	if status, again := post(app.translateText, `{"text":"price 1000","target":"km"}`); status != http.StatusOK || again["translation"] != "T(price 1000)" {
		t.Fatalf("cached call = %d %v", status, again)
	}
	if got := callCount(); got != 1 {
		t.Fatalf("model calls = %d after a repeated message, want 1 (cache)", got)
	}

	// 3. A batch that the model answers short: the gap is retried on its own.
	status, batch := post(app.translateBatch, `{"texts":["first","second","third"],"target":"km"}`)
	if status != http.StatusOK {
		t.Fatalf("batch status = %d (%v)", status, batch)
	}
	got, _ := batch["translations"].([]any)
	if len(got) != 3 || got[0] != "T(first)" || got[1] != "T(second)" || got[2] != "T(third)" {
		t.Fatalf("batch translations = %v", batch["translations"])
	}
	if n := callCount(); n != 3 {
		t.Fatalf("model calls = %d, want 3 (one batch + one gap retry)", n)
	}
	if gap := prompts[2]; !strings.Contains(gap, `["third"]`) || strings.Contains(gap, `"first"`) {
		t.Fatalf("the retry must carry only the dropped entry, got:\n%s", gap)
	}

	// 4. A message already in the target language never leaves the process.
	status, mixed := post(app.translateBatch, `{"texts":["already english","ជំនួយ"],"target":"en"}`)
	if status != http.StatusOK {
		t.Fatalf("mixed batch status = %d (%v)", status, mixed)
	}
	mixedGot, _ := mixed["translations"].([]any)
	if len(mixedGot) != 2 || mixedGot[0] != "already english" || mixedGot[1] != "T(ជំនួយ)" {
		t.Fatalf("mixed batch = %v", mixed["translations"])
	}
	if n := callCount(); n != 4 {
		t.Fatalf("model calls = %d, want 4 (the English message must not be sent)", n)
	}

	// 5. A rendering that changes a digit is re-asked and ends up verified.
	status, digits := post(app.translateText, `{"text":"total 2500","target":"km"}`)
	if status != http.StatusOK {
		t.Fatalf("digits status = %d (%v)", status, digits)
	}
	if digits["translation"] != "T(total 2500)" {
		t.Fatalf("digits translation = %v", digits["translation"])
	}
	if verified, _ := digits["verified"].(bool); !verified {
		t.Fatalf("verified = %v after the strict retry", digits["verified"])
	}
	if n := callCount(); n != 6 {
		t.Fatalf("model calls = %d, want 6 (first pass + strict retry)", n)
	}

	// 6. An over-long message is skipped, and the rest of the batch still goes.
	long := strings.Repeat("字", translateMaxRunes+1)
	status, oversize := post(app.translateBatch, `{"texts":["`+long+`","short"],"target":"km"}`)
	if status != http.StatusOK {
		t.Fatalf("oversize batch status = %d (%v)", status, oversize)
	}
	skipped, _ := oversize["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(float64) != 0 {
		t.Fatalf("skipped = %v, want [0]", oversize["skipped"])
	}
	overGot, _ := oversize["translations"].([]any)
	if len(overGot) != 2 || overGot[0] != "" || overGot[1] != "T(short)" {
		t.Fatalf("oversize translations = %v", oversize["translations"])
	}
}
