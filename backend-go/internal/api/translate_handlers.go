// Package api — agent copilot translation (multi-language, agent picks target).
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/gemini"
)

// translateTargets — languages the copilot can render a message into. Names
// are given in the language itself so agents can spot the right one at a
// glance regardless of their UI locale.
var translateTargets = map[string]string{
	"km": "Khmer (ភាសាខ្មែរ)",
	"zh": "Simplified Chinese (简体中文)",
	"en": "English",
	"th": "Thai (ไทย)",
	"vi": "Vietnamese (Tiếng Việt)",
	"lo": "Lao (ລາວ)",
	"my": "Burmese (မြန်မာ)",
	"ms": "Malay (Bahasa Melayu)",
	"id": "Indonesian (Bahasa Indonesia)",
	"ja": "Japanese (日本語)",
	"ko": "Korean (한국어)",
	"ar": "Arabic (العربية)",
	"ru": "Russian (Русский)",
	"fr": "French (Français)",
	"es": "Spanish (Español)",
	"de": "German (Deutsch)",
}

// translateCacheTTL — translations are deterministic (temperature 0), so a
// cached rendering stays valid; a week makes chatty sessions instant.
const translateCacheTTL = 7 * 24 * time.Hour

// translateCacheKey — stable key per (text, target).
func translateCacheKey(text, target string) string {
	sum := sha256.Sum256([]byte(target + "\x00" + text))
	return "translate:" + hex.EncodeToString(sum[:])
}

// resolveTranslateTarget validates the requested target, defaulting to the
// agent's working language when the source is Khmer (and Khmer otherwise).
func resolveTranslateTarget(text, target string) string {
	if _, ok := translateTargets[target]; ok {
		return target
	}
	if gemini.DetectLanguage(text) == "km" {
		return "zh"
	}
	return "km"
}

// translatePrompt builds the instruction for one text.
func translatePrompt(text, target string) string {
	return "Translate the following customer-support message into " + translateTargets[target] + ". " +
		"Reply with ONLY the translation — no quotes, no notes, keep the original tone, line breaks, numbers and prices exactly.\n\n" +
		text
}

// translateOneText translates a single string, consulting the Redis cache
// first (repeated greetings / price questions are then instant).
func (a *App) translateOneText(ctx context.Context, text, target string) (string, error) {
	key := translateCacheKey(text, target)
	if cached, err := a.Redis.GetString(ctx, key); err == nil && cached != "" {
		return cached, nil
	}
	out, ok := a.Gemini.GenerateFast(ctx, translatePrompt(text, target), 15*time.Second)
	if !ok {
		return "", errTranslateFailed
	}
	translation := strings.TrimSpace(gemini.StripSourceMarkers(out))
	if translation == "" {
		return "", errTranslateFailed
	}
	// Best-effort cache write; a Redis hiccup must not fail the request.
	_ = a.Redis.SetString(ctx, key, translation, translateCacheTTL)
	return translation, nil
}

type translateFailedError struct{}

func (translateFailedError) Error() string { return "translation failed" }

var errTranslateFailed = translateFailedError{}

// translateText — one-shot machine translation for the agent copilot: read a
// customer message in the agent's working language, or send the reply as the
// customer's language.
func (a *App) translateText(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Text   string `json:"text"`
		Target string `json:"target"` // key of translateTargets
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len([]rune(text)) > 4000 {
		return nil, ErrBadRequest("text 必填且不超过 4000 字")
	}
	target := resolveTranslateTarget(text, req.Target)
	translation, err := a.translateOneText(r.Context(), text, target)
	if err != nil {
		return nil, &ApiError{http.StatusBadGateway, "翻译失败，请稍后重试"}
	}
	return map[string]any{"translation": translation, "target": target}, nil
}

// translateBatch — translate several messages in a SINGLE model call.
//
// Auto-translate used to fire one request per message: a 50-message chat meant
// 50 round trips (and while the client serialized them, up to a couple of
// minutes before anything appeared). One numbered-list call returns them all,
// and anything already cached is filled in without touching the model.
func (a *App) translateBatch(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Texts  []string `json:"texts"`
		Target string   `json:"target"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if len(req.Texts) == 0 || len(req.Texts) > 50 {
		return nil, ErrBadRequest("texts 需为 1-50 条")
	}
	ctx := r.Context()

	// Resolve the target once (from the first non-empty text when unset).
	target := req.Target
	if _, ok := translateTargets[target]; !ok {
		target = resolveTranslateTarget(firstNonEmptyText(req.Texts), "")
	}

	out := make([]string, len(req.Texts))
	missIdx := make([]int, 0, len(req.Texts))
	missTexts := make([]string, 0, len(req.Texts))
	seen := map[string]int{}    // text → first index awaiting translation
	dupIdx := map[string][]int{} // text → other indices to backfill

	for i, raw := range req.Texts {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		if cached, err := a.Redis.GetString(ctx, translateCacheKey(text, target)); err == nil && cached != "" {
			out[i] = cached
			continue
		}
		if _, dup := seen[text]; dup {
			dupIdx[text] = append(dupIdx[text], i)
			continue
		}
		seen[text] = i
		missIdx = append(missIdx, i)
		missTexts = append(missTexts, text)
	}

	if len(missTexts) > 0 {
		var b strings.Builder
		b.WriteString("Translate each numbered customer-support message below into ")
		b.WriteString(translateTargets[target])
		b.WriteString(".\nReply with ONLY a JSON array of strings, one translation per input, in the same order — ")
		b.WriteString("no notes, no numbering, keep tones, line breaks, numbers and prices exactly.\n\n")
		for i, t := range missTexts {
			b.WriteString("[")
			b.WriteString(strconv.Itoa(i + 1))
			b.WriteString("] ")
			b.WriteString(t)
			b.WriteString("\n")
		}
		reply, ok := a.Gemini.GenerateFast(ctx, b.String(), 25*time.Second)
		if !ok {
			return nil, &ApiError{http.StatusBadGateway, "翻译失败，请稍后重试"}
		}
		translated := parseTranslationArray(reply, len(missTexts))
		for n, idx := range missIdx {
			text := missTexts[n]
			val := ""
			if n < len(translated) {
				val = strings.TrimSpace(translated[n])
			}
			if val == "" {
				// The model dropped or merged an entry — fall back to a single
				// call so the agent still gets every line translated.
				if single, err := a.translateOneText(ctx, text, target); err == nil {
					val = single
				}
			}
			out[idx] = val
			if val != "" {
				_ = a.Redis.SetString(ctx, translateCacheKey(text, target), val, translateCacheTTL)
			}
		}
	}

	// Duplicates reuse the first occurrence's rendering.
	for text, firstIdx := range seen {
		if out[firstIdx] == "" {
			continue
		}
		for _, i := range dupIdx[text] {
			out[i] = out[firstIdx]
		}
	}
	return map[string]any{"translations": out, "target": target}, nil
}

// parseTranslationArray extracts the string array from a model reply, tolerating
// markdown fences and stray prose.
func parseTranslationArray(reply string, want int) []string {
	trimmed := strings.TrimSpace(reply)
	if i := strings.Index(trimmed, "["); i >= 0 {
		if j := strings.LastIndex(trimmed, "]"); j > i {
			var arr []string
			if json.Unmarshal([]byte(trimmed[i:j+1]), &arr) == nil {
				return arr
			}
			// Some models emit non-string scalars; retry as generic values.
			var raw []any
			if json.Unmarshal([]byte(trimmed[i:j+1]), &raw) == nil {
				out := make([]string, 0, len(raw))
				for _, v := range raw {
					s, _ := v.(string)
					out = append(out, s)
				}
				return out
			}
		}
	}
	// No JSON: fall back to one-per-line when the count matches.
	lines := []string{}
	for _, l := range strings.Split(trimmed, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == want {
		return lines
	}
	return nil
}

func firstNonEmptyText(items []string) string {
	for _, s := range items {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
