// Package api — agent copilot translation (Khmer ↔ 中文 ↔ English).
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/gemini"
)

// translateText — one-shot machine translation for the agent copilot: read a
// Khmer customer message in 中文, or send your 中文 reply as Khmer.
func (a *App) translateText(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Text   string `json:"text"`
		Target string `json:"target"` // km | zh | en
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len([]rune(text)) > 4000 {
		return nil, ErrBadRequest("text 必填且不超过 4000 字")
	}
	target := req.Target
	if target != "km" && target != "zh" && target != "en" {
		// Default: translate into the operator's own working language when
		// the source is Khmer, and into Khmer otherwise.
		target = map[string]string{"km": "zh"}[gemini.DetectLanguage(text)]
		if target == "" {
			target = "km"
		}
	}
	languageName := map[string]string{"km": "Khmer (ភាសាខ្មែរ)", "zh": "Simplified Chinese (简体中文)", "en": "English"}[target]
	prompt := "Translate the following customer-support message into " + languageName + ". " +
		"Reply with ONLY the translation — no quotes, no notes, keep the original tone, line breaks, numbers and prices exactly.\n\n" +
		text
	out, ok := a.Gemini.GenerateFast(r.Context(), prompt, 15*time.Second)
	if !ok {
		return nil, &ApiError{http.StatusBadGateway, "翻译失败，请稍后重试"}
	}
	translation := strings.TrimSpace(gemini.StripSourceMarkers(out))
	if translation == "" {
		return nil, &ApiError{http.StatusBadGateway, "翻译失败，请稍后重试"}
	}
	return map[string]any{"translation": translation, "target": target}, nil
}
