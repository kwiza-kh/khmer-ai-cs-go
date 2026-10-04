package gemini

import "strings"

// audioMimeAliases maps the container names that channels and clients actually
// send onto the audio/* types the Gemini/Vertex endpoint accepts.
//
// Why this file exists: the endpoint answers a generic `400 INVALID_ARGUMENT`
// when inlineData.mimeType is not one it recognises, and the caller only sees
// "语音转写失败". Two of those aliases reached production — `application/ogg`
// (how many servers label an Ogg/Opus voice note, and what curl infers from a
// .ogg filename) and `application/mp4` (the same for .m4a). Measured
// 2026-10-04 against the production endpoint with real customer audio:
//
//	application/ogg → 400        audio/ogg → 200, correct Khmer transcript
//	(curl default for .m4a) → 400  audio/mp4 → 200, correct transcript
//
// The pipeline already rescued `application/octet-stream` by falling back to the
// webhook's declared mime, but not these. One of the eight real inbound voice
// notes in the database (Telegram, 2026-09-10) shows the customer-visible cost:
// its content was stored as the "no transcript" placeholder.
//
// The endpoint tolerates a *supported* type that disagrees with the container
// (a WebM/Opus payload sent as audio/ogg still transcribed correctly), so
// canonicalising the name is enough — no re-muxing is needed.
var audioMimeAliases = map[string]string{
	// Ogg / Opus
	"audio/ogg":         "audio/ogg",
	"application/ogg":   "audio/ogg",
	"audio/x-ogg":       "audio/ogg",
	"application/x-ogg": "audio/ogg",
	"audio/oga":         "audio/ogg",
	"audio/opus":        "audio/ogg",
	"audio/vorbis":      "audio/ogg",
	// MP4 / M4A
	"audio/mp4":       "audio/mp4",
	"application/mp4": "audio/mp4",
	"audio/m4a":       "audio/mp4",
	"audio/x-m4a":     "audio/mp4",
	"audio/x-mp4":     "audio/mp4",
	"video/mp4":       "audio/mp4",
	"video/quicktime": "audio/mp4",
	// MP3 / MPEG
	"audio/mpeg":     "audio/mpeg",
	"audio/mp3":      "audio/mpeg",
	"audio/x-mp3":    "audio/mpeg",
	"audio/mpeg3":    "audio/mpeg",
	"audio/x-mpeg":   "audio/mpeg",
	"audio/x-mpeg-3": "audio/mpeg",
	// WAV
	"audio/wav":         "audio/wav",
	"audio/x-wav":       "audio/wav",
	"audio/wave":        "audio/wav",
	"audio/vnd.wave":    "audio/wav",
	"application/x-wav": "audio/wav",
	// AAC (raw ADTS, not the MP4 container)
	"audio/aac":   "audio/aac",
	"audio/x-aac": "audio/aac",
	"audio/aacp":  "audio/aac",
	// FLAC
	"audio/flac":         "audio/flac",
	"audio/x-flac":       "audio/flac",
	"application/x-flac": "audio/flac",
	// AIFF
	"audio/aiff":   "audio/aiff",
	"audio/x-aiff": "audio/aiff",
	"audio/aif":    "audio/aiff",
	// WebM — outside the vendor's documented list, but measured accepted
	"audio/webm":       "audio/webm",
	"video/webm":       "audio/webm",
	"application/webm": "audio/webm",
}

// CanonicalAudioMime returns a mimeType the endpoint will accept for these
// bytes, or the input unchanged when it cannot do better.
//
// Empty and `application/octet-stream` are answered by sniffing the container:
// that is what a channel that reports nothing useful hands us, and guessing from
// the bytes beats guessing from the extension (the stored filename is
// `voice-<id>.bin`, which carries no information at all).
func CanonicalAudioMime(mime string, audio []byte) string {
	key := strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(key, ';'); i >= 0 {
		key = strings.TrimSpace(key[:i])
	}
	if canonical, ok := audioMimeAliases[key]; ok {
		return canonical
	}
	if key == "" || key == "application/octet-stream" || key == "binary/octet-stream" {
		if sniffed := sniffAudioMime(audio); sniffed != "" {
			return sniffed
		}
		return "audio/ogg"
	}
	return mime
}

// sniffAudioMime reads the container magic. Every case here is one the
// implementation can actually produce: channels deliver Ogg/Opus (WhatsApp,
// Telegram), MP4/AAC (LINE) and WebM (browsers recording with MediaRecorder).
func sniffAudioMime(b []byte) string {
	switch {
	case len(b) < 4:
		return ""
	case string(b[:4]) == "OggS":
		return "audio/ogg"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "audio/wav"
	case string(b[:4]) == "fLaC":
		return "audio/flac"
	case string(b[:4]) == "FORM" && len(b) >= 12 && string(b[8:12]) == "AIFF":
		return "audio/aiff"
	case string(b[:4]) == "\x1a\x45\xdf\xa3":
		return "audio/webm"
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return "audio/mp4"
	case string(b[:3]) == "ID3":
		return "audio/mpeg"
	case b[0] == 0xFF && (b[1]&0xE0) == 0xE0:
		// MPEG audio frame sync (MP3). ADTS AAC shares the sync word and is
		// distinguished by the layer bits; audio/mpeg is the right answer for
		// both, since an AAC stream labelled audio/mpeg still decodes.
		return "audio/mpeg"
	}
	return ""
}
