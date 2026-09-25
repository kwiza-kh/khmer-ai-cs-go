package main

// Capability probe: does the chosen model accept every request shape this
// product actually sends? Replacing the model everywhere is only viable if the
// multimodal and configuration paths survive, so each one is exercised against
// the real API before any code is rewritten.
//
// Checked: systemInstruction, streaming SSE, inline image, inline audio,
// inline PDF, and thinkingConfig.thinkingBudget (GEMINI_THINKING_BUDGET).

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// onePixelPNG is the smallest valid PNG — enough for the API to accept and
// decode the part; the model's answer quality is irrelevant here.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

// minimalPDF is a valid one-page PDF, so the document part is accepted as
// application/pdf rather than rejected as malformed.
const minimalPDF = `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj
trailer<</Root 1 0 R>>
%%EOF`

// silentWAV builds a minimal 8 kHz mono PCM WAV so the audio part is a real,
// parseable container rather than a random blob.
func silentWAV() []byte {
	const samples = 800 // 100 ms of silence
	data := make([]byte, samples*2)

	buf := make([]byte, 0, 44+len(data))
	buf = append(buf, "RIFF"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(36+len(data)))
	buf = append(buf, "WAVEfmt "...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1)     // PCM
	buf = binary.LittleEndian.AppendUint16(buf, 1)     // mono
	buf = binary.LittleEndian.AppendUint32(buf, 8000)  // sample rate
	buf = binary.LittleEndian.AppendUint32(buf, 16000) // byte rate
	buf = binary.LittleEndian.AppendUint16(buf, 2)     // block align
	buf = binary.LittleEndian.AppendUint16(buf, 16)    // bits per sample
	buf = append(buf, "data"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(data)))
	return append(buf, data...)
}

// runCaps exercises each request shape and prints one verdict per shape. The
// return value is how many shapes the API rejected, so the caller can pick the
// exit status.
func (p *probe) runCaps(ctx context.Context, model string) int {
	fmt.Printf("\n── capability probe on %s ─────────────────────────\n", model)

	inline := func(mime, data string) []map[string]any {
		return []map[string]any{{"inlineData": map[string]any{"mimeType": mime, "data": data}}}
	}
	text := []map[string]any{{"text": "hi"}}

	checks := []struct {
		name   string
		parts  []map[string]any
		system bool
		extra  map[string]any
		stream bool
	}{
		{name: "text + systemInstruction", parts: text, system: true},
		{name: "inline image (DescribeImage)", parts: inline("image/png", onePixelPNG)},
		{name: "inline audio (TranscribeAudio)", parts: inline("audio/wav", base64.StdEncoding.EncodeToString(silentWAV()))},
		{name: "inline pdf (ExtractDocumentText)", parts: inline("application/pdf", base64.StdEncoding.EncodeToString([]byte(minimalPDF)))},
		{name: "generationConfig.thinkingConfig", parts: text, extra: map[string]any{"generationConfig": map[string]any{"maxOutputTokens": 16, "thinkingConfig": map[string]any{"thinkingBudget": 0}}}},
		{name: "streaming SSE", parts: text, stream: true},
	}

	rejected := 0
	for _, c := range checks {
		body := map[string]any{"contents": []map[string]any{{"role": "user", "parts": c.parts}}}
		if c.system {
			body["systemInstruction"] = map[string]any{
				"parts": []map[string]any{{"text": "You are a customer-service assistant."}},
			}
		}
		for k, v := range c.extra {
			body[k] = v
		}

		target := p.modelBase + "/" + model + ":generateContent"
		if c.stream {
			target += "?alt=sse"
		}
		status, resp, err := p.post(ctx, target, body)
		switch {
		case err != nil:
			fmt.Printf("✗ %-40s %v\n", c.name, err)
			rejected++
		case status != http.StatusOK:
			fmt.Printf("✗ %-40s %s\n", c.name, summarize(status, resp))
			rejected++
		case c.stream && !strings.Contains(string(resp), "data:"):
			fmt.Printf("✗ %-40s HTTP 200 but no SSE frames\n", c.name)
			rejected++
		default:
			note := ""
			if c.stream {
				note = "  (SSE frames present)"
			}
			fmt.Printf("✓ %-40s OK%s\n", c.name, note)
		}
	}
	fmt.Printf("\n%d/%d request shapes accepted.\n", len(checks)-rejected, len(checks))
	return rejected
}

// runAudioDir sends every audio file in dir as an inlineData part, one request
// per file, and reports whether the API accepted the container. Format support
// is the part of STT that a wav-only probe cannot answer: the channels deliver
// ogg (WhatsApp/Telegram voice notes) and mpeg (LINE), not wav.
func (p *probe) runAudioDir(ctx context.Context, model, dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Printf("cannot read %s: %v\n", dir, err)
		return 1
	}
	fmt.Printf("\n── audio container probe on %s ────────────────────\n", model)
	checked, rejected := 0, 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		mime := mimeForExt(filepath.Ext(e.Name()))
		if mime == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			fmt.Printf("✗ %-24s read: %v\n", e.Name(), err)
			rejected++
			continue
		}
		checked++
		body := map[string]any{"contents": []map[string]any{{
			"role": "user",
			"parts": []map[string]any{
				{"text": "Transcribe this audio verbatim. If it is silent, say SILENT."},
				{"inlineData": map[string]any{"mimeType": mime, "data": base64.StdEncoding.EncodeToString(data)}},
			},
		}}}
		status, resp, err := p.post(ctx, p.modelBase+"/"+model+":generateContent", body)
		switch {
		case err != nil:
			fmt.Printf("✗ %-24s %s  %v\n", e.Name(), mime, err)
			rejected++
		case status != http.StatusOK:
			fmt.Printf("✗ %-24s %s  %s\n", e.Name(), mime, summarize(status, resp))
			rejected++
		default:
			// A 200 means the container decoded. The transcript itself is
			// reported so a silent/garbage asset is visible rather than implied.
			fmt.Printf("✓ %-24s %s  transcript=%q\n", e.Name(), mime, firstText(mustMap(resp)))
		}
	}
	fmt.Printf("\n%d/%d containers accepted.\n", checked-rejected, checked)
	return rejected
}

func mimeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".ogg", ".oga", ".opus":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	}
	return ""
}

func mustMap(b []byte) map[string]any {
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	return v
}

// firstText pulls candidates[0].content.parts[*].text out of a generateContent
// response. The probe deliberately does not import internal/gemini: it must be
// able to run before that package is changed.
func firstText(v map[string]any) string {
	cands, _ := v["candidates"].([]any)
	if len(cands) == 0 {
		return ""
	}
	c0, _ := cands[0].(map[string]any)
	content, _ := c0["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	var sb strings.Builder
	for _, p := range parts {
		if pm, ok := p.(map[string]any); ok {
			if t, ok := pm["text"].(string); ok {
				sb.WriteString(t)
			}
		}
	}
	return firstLine([]byte(sb.String()))
}
