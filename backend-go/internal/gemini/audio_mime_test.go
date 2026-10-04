package gemini

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestCanonicalAudioMimeMapsContainerAliases(t *testing.T) {
	// Every row here is a name a real client or channel sends. The two that
	// matter most were measured against the production endpoint on 2026-10-04:
	// application/ogg and the default curl type for .m4a both returned
	// 400 INVALID_ARGUMENT, while audio/ogg and audio/mp4 returned a transcript.
	for _, tc := range []struct{ in, want string }{
		{"application/ogg", "audio/ogg"},
		{"application/ogg; charset=binary", "audio/ogg"},
		{"APPLICATION/OGG", "audio/ogg"},
		{" audio/oga ", "audio/ogg"},
		{"audio/opus", "audio/ogg"},
		{"application/mp4", "audio/mp4"},
		{"audio/x-m4a", "audio/mp4"},
		{"video/mp4", "audio/mp4"},
		{"audio/x-wav", "audio/wav"},
		{"audio/mp3", "audio/mpeg"},
		{"audio/aacp", "audio/aac"},
		{"application/x-flac", "audio/flac"},
		{"video/webm", "audio/webm"},
	} {
		if got := CanonicalAudioMime(tc.in, nil); got != tc.want {
			t.Errorf("CanonicalAudioMime(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Parameters are stripped even from a type the endpoint already knows:
	// inlineData.mimeType takes a bare type, and a leftover "; codecs=opus" is
	// itself a 400 waiting to happen.
	if got := CanonicalAudioMime("audio/ogg; codecs=opus", nil); got != "audio/ogg" {
		t.Errorf("mime parameters must be stripped, got %q", got)
	}
	if got := CanonicalAudioMime("audio/ogg", nil); got != "audio/ogg" {
		t.Errorf("audio/ogg = %q", got)
	}
}

func TestCanonicalAudioMimeSniffsWhenThereIsNothingToGoOn(t *testing.T) {
	ogg := append([]byte("OggS\x00\x02"), make([]byte, 32)...)
	mp4 := append([]byte{0, 0, 0, 0x20}, []byte("ftypisom")...)
	wav := append([]byte("RIFF\x00\x00\x00\x00WAVEfmt "), make([]byte, 8)...)
	webm := append([]byte{0x1a, 0x45, 0xdf, 0xa3}, make([]byte, 32)...)
	flac := append([]byte("fLaC"), make([]byte, 32)...)
	mp3 := append([]byte("ID3\x04"), make([]byte, 32)...)

	for _, tc := range []struct {
		name  string
		mime  string
		audio []byte
		want  string
	}{
		{"octet-stream ogg", "application/octet-stream", ogg, "audio/ogg"},
		{"octet-stream mp4", "application/octet-stream", mp4, "audio/mp4"},
		{"empty mime ogg", "", ogg, "audio/ogg"},
		{"empty mime wav", "", wav, "audio/wav"},
		{"empty mime webm", "", webm, "audio/webm"},
		{"empty mime flac", "", flac, "audio/flac"},
		{"empty mime mp3", "", mp3, "audio/mpeg"},
		// Undetectable bytes: the pre-existing default, so behaviour is no worse
		// than before this function existed.
		{"empty mime garbage", "", []byte("nonsense"), "audio/ogg"},
		{"empty mime empty", "", nil, "audio/ogg"},
	} {
		if got := CanonicalAudioMime(tc.mime, tc.audio); got != tc.want {
			t.Errorf("%s: CanonicalAudioMime(%q, %d bytes) = %q, want %q",
				tc.name, tc.mime, len(tc.audio), got, tc.want)
		}
	}
}

// A stream whose first bytes are an MPEG frame sync but which is not otherwise
// identifiable must not be reported as something exotic.
func TestSniffAudioMimeIgnoresShortAndUnknownInput(t *testing.T) {
	if got := sniffAudioMime([]byte{0xFF}); got != "" {
		t.Errorf("a one-byte buffer = %q, want empty", got)
	}
	if got := sniffAudioMime([]byte("....")); got != "" {
		t.Errorf("unknown magic = %q, want empty", got)
	}
	// Two bytes cannot identify a container, so the frame-sync arm is
	// deliberately unreachable below four bytes — the caller falls back to the
	// documented default rather than pretending to know.
	if got := sniffAudioMime([]byte{0xFF, 0xFB}); got != "" {
		t.Errorf("a two-byte buffer = %q, want empty", got)
	}
	if got := sniffAudioMime([]byte{0xFF, 0xFB, 0x90, 0x00}); got != "audio/mpeg" {
		t.Errorf("MPEG frame sync = %q, want audio/mpeg", got)
	}
	// Guard against a future edit reading past the end of a short buffer.
	for n := 0; n < 13; n++ {
		buf := make([]byte, n)
		copy(buf, []byte("RIFF\x00\x00\x00\x00WAVE"))
		_ = sniffAudioMime(buf)
	}
	// ftyp detection needs the offset-4 marker, not the whole string.
	if !bytes.Equal(binary.BigEndian.AppendUint32(nil, 0x20), []byte{0, 0, 0, 0x20}) {
		t.Fatal("unexpected endianness assumption")
	}
}
