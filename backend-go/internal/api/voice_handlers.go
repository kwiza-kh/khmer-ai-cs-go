package api

import (
	"io"
	"net/http"
	"strings"
)

// chatVoice — server-side speech-to-text via Gemini (multipart audio).
// Returns the transcript so the client can feed it into the normal chat flow.
func (a *App) chatVoice(w http.ResponseWriter, r *http.Request) (any, error) {
	// Hard cap the whole request before parsing (DoS guard).
	r.Body = http.MaxBytesReader(w, r.Body, 26<<20)
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	file, _, err := r.FormFile("audio")
	if err != nil {
		return nil, ErrBadRequest("missing 'audio' file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 25*1024*1024))
	if err != nil || len(data) == 0 {
		return nil, ErrBadRequest("读取音频失败")
	}
	mime := ""
	if fh := r.MultipartForm.File["audio"]; len(fh) > 0 && fh[0].Header.Get("Content-Type") != "" {
		mime = fh[0].Header.Get("Content-Type")
	}
	if mime == "" {
		mime = r.FormValue("mime")
	}
	if mime == "" {
		mime = "audio/webm"
	}
	language := r.FormValue("language")
	if language == "" {
		language = "km"
	}
	transcript, err := a.Gemini.TranscribeAudio(r.Context(), data, normalizeMime(mime), language)
	if err != nil {
		return nil, ErrInternal("语音转写失败")
	}
	return map[string]any{"transcript": transcript, "language": language, "used_mock": !a.Gemini.IsConfigured()}, nil
}

// normalizeMime strips parameters (e.g. `audio/webm;codecs=opus`).
func normalizeMime(m string) string {
	if i := strings.Index(m, ";"); i >= 0 {
		m = m[:i]
	}
	return strings.TrimSpace(strings.ToLower(m))
}
