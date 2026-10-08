package platform

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The capability table replaced a `switch platform` in four places. These tests
// pin the data itself, so a future channel cannot be half-declared and the
// values the old switch returned cannot silently drift.

func TestCapabilitiesTableIsSelfConsistent(t *testing.T) {
	for key, caps := range capabilitiesTable {
		if caps.Platform != key {
			t.Errorf("%s: Platform field is %q", key, caps.Platform)
		}
		if caps.DisplayName == "" {
			t.Errorf("%s: DisplayName must be set (policy messages use it)", key)
		}
		if !caps.Known {
			t.Errorf("%s: a table row must be Known", key)
		}
		if caps.TextLimit <= 0 {
			t.Errorf("%s: TextLimit must be positive", key)
		}
		if caps.Media&MediaText == 0 {
			t.Errorf("%s: every channel must carry text", key)
		}
		if caps.TextLimitUnit != UnitRunes && caps.TextLimitUnit != UnitBytes {
			t.Errorf("%s: unknown TextLimitUnit %v", key, caps.TextLimitUnit)
		}
		// Connectable channels need at least one credential the admin form can
		// render; the website widget is not connectable through /platforms.
		if key != "web" && len(caps.Credentials) == 0 {
			t.Errorf("%s: no credential fields declared", key)
		}
		for _, f := range caps.Credentials {
			if f.Key == "" || f.Label == "" {
				t.Errorf("%s: credential field missing key/label: %+v", key, f)
			}
		}
	}
}

// TestCapabilitiesCoverPlatformTypeEnum keeps the table in step with the
// platform_type enum in the database: a value that exists in the DB but not here
// would make policy refuse a live channel.
func TestCapabilitiesCoverPlatformTypeEnum(t *testing.T) {
	// Every platform_type value the database can hold must be a KNOWN capability
	// row: an enum member missing here would make policy refuse a live channel.
	// (This used to enumerate through a KnownPlatforms() helper that nothing else
	// called; the assertion is the same, asked of the table itself.)
	want := []string{"instagram", "line", "meta", "telegram", "web", "whatsapp", "zalo"}
	for _, name := range want {
		if c := CapabilitiesFor(name); !c.Known {
			t.Errorf("CapabilitiesFor(%q).Known = false, want the DB enum member to be known", name)
		}
	}
	if c := CapabilitiesFor("nope"); c.Known {
		t.Error("an unknown platform must not be Known")
	}
}

// TestPlatformTextLimitPreservesLegacyValues is the regression net for the
// switch this table replaced.
func TestPlatformTextLimitPreservesLegacyValues(t *testing.T) {
	// The regression net for the switch this table replaced: the caps must not move.
	cases := map[string]int{
		"telegram":  4096,
		"line":      5000,
		"whatsapp":  1024,
		"instagram": 1000,
		"meta":      2000,
		"zalo":      2000,
		"web":       2000,
		// The old switch's default branch.
		"nope": 2000,
	}
	for platform, want := range cases {
		if got := CapabilitiesFor(platform).TextLimit; got != want {
			t.Errorf("TextLimit(%q) = %d, want %d", platform, got, want)
		}
	}
}

func TestCapabilitiesReplyWindowShape(t *testing.T) {
	for _, p := range []string{"telegram", "line", "zalo"} {
		c := CapabilitiesFor(p)
		if !c.Windowless {
			t.Errorf("%s must be Windowless", p)
		}
		if c.ReplyWindow != 0 {
			t.Errorf("%s must not declare a reply window, got %s", p, c.ReplyWindow)
		}
		if c.HumanExtension != 0 {
			t.Errorf("%s must not declare a human extension", p)
		}
	}
	for _, p := range []string{"whatsapp", "meta", "instagram"} {
		c := CapabilitiesFor(p)
		if c.Windowless {
			t.Errorf("%s must NOT be Windowless", p)
		}
		if c.ReplyWindow != CustomerCareWindowHours*time.Hour {
			t.Errorf("%s ReplyWindow = %s, want 24h", p, c.ReplyWindow)
		}
	}
	if !CapabilitiesFor("whatsapp").TemplateExempt {
		t.Error("whatsapp must be TemplateExempt")
	}
	for _, p := range []string{"meta", "instagram"} {
		if got := CapabilitiesFor(p).HumanExtension; got != HumanAgentWindowDays*24*time.Hour {
			t.Errorf("%s HumanExtension = %s, want 7d", p, got)
		}
	}
	// The website widget is a known channel with no window; it must not become
	// windowless, because that is what keeps the reply-window policy refusing it
	// exactly as the old switch did.
	web := CapabilitiesFor("web")
	if !web.Known || web.Windowless || web.ReplyWindow != 0 {
		t.Errorf("web caps = %+v, want Known + non-windowless + no window", web)
	}
}

func TestCapabilitiesUnknownPlatformIsNotKnown(t *testing.T) {
	c := CapabilitiesFor("myspace")
	if c.Known {
		t.Fatal("unknown platform must not be Known")
	}
	if c.Platform != "myspace" || c.DisplayName != "myspace" {
		t.Fatalf("unknown platform identity = %q/%q", c.Platform, c.DisplayName)
	}
}

func TestSplitChannelTextRuneChannelMatchesLegacySplitter(t *testing.T) {
	text := strings.Repeat("word ", 1000) // ~5000 runes
	caps := CapabilitiesFor("telegram")

	got := SplitChannelText(text, caps)
	want := SplitPlatformText(text, CapabilitiesFor("telegram").TextLimit)

	if len(got) != len(want) {
		t.Fatalf("chunk count = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSplitChannelTextByteCapNeverExceedsLimit is the WeChat/WeCom case: the cap
// is 2048 bytes and a Khmer or CJK rune costs three, so a rune-based splitter
// would overshoot by 3x.
func TestSplitChannelTextByteCapNeverExceedsLimit(t *testing.T) {
	caps := Capabilities{Platform: "bytecapped", TextLimit: 2048, TextLimitUnit: UnitBytes}
	text := strings.Repeat("សូមស្វាគមន៍", 400) // 3 bytes per rune
	if len(text) <= 2048 {
		t.Fatalf("fixture too small: %d bytes", len(text))
	}

	chunks := SplitChannelText(text, caps)
	if len(chunks) < 2 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 2048 {
			t.Fatalf("chunk %d is %d bytes, over the 2048 cap", i, len(c))
		}
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %d split a rune in half", i)
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatal("byte splitting lost or reordered content")
	}
}

// A single rune bigger than the whole budget must still make progress rather
// than loop forever.
func TestSplitChannelTextByteCapProgressOnOversizedRune(t *testing.T) {
	caps := Capabilities{TextLimit: 1, TextLimitUnit: UnitBytes}
	chunks := SplitChannelText("中中中", caps)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d (%q), want 3", len(chunks), chunks)
	}
	for _, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("invalid rune in %q", c)
		}
	}
}

func TestSplitChannelTextUnlimitedPassesThrough(t *testing.T) {
	if got := SplitChannelText("hello", Capabilities{}); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("zero cap must pass text through, got %q", got)
	}
}

func TestMediaKindsSupports(t *testing.T) {
	if !(MediaText | MediaImage).Supports(MediaText) {
		t.Error("text must be supported")
	}
	if (MediaText | MediaImage).Supports(MediaAudio) {
		t.Error("audio must not be reported as supported")
	}
	if !MediaAll.Supports(MediaFile) {
		t.Error("MediaAll must support file")
	}
}
