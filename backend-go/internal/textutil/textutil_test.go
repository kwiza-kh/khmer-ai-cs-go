package textutil

import "testing"

func TestTruncateRunesNeverMarksAndNeverPanics(t *testing.T) {
	// The plain form is for values that are stored or shown as data: an appended
	// "…" would become part of the value, so the result is exactly the first n
	// runes.
	if got := TruncateRunes("abcdefgh", 5); got != "abcde" {
		t.Errorf("TruncateRunes = %q, want %q", got, "abcde")
	}
	if got := TruncateRunes("abc", 5); got != "abc" {
		t.Errorf("a short string must pass through untouched, got %q", got)
	}
	// n<=0 used to be a negative index into the rune slice — a panic, not an
	// empty string. It is reachable from a caller-supplied room budget.
	for _, n := range []int{0, -1} {
		if got := TruncateRunes("abcdef", n); got != "" {
			t.Errorf("TruncateRunes(n=%d) = %q, want empty", n, got)
		}
	}
	// Runes, not bytes: a Khmer cut must not split a code point.
	if got := []rune(TruncateRunes("តើប្រាក់ខែ", 3)); len(got) != 3 {
		t.Errorf("TruncateRunes kept %d runes, want 3 (%q)", len(got), string(got))
	}
}

func TestEllipsizeStaysInsideTheBudget(t *testing.T) {
	// The marked form must never exceed n runes INCLUDING the marker, or a
	// caller's budget is silently blown by one.
	if got := Ellipsize("abcdefgh", 5); got != "abcd…" {
		t.Errorf("Ellipsize = %q, want %q", got, "abcd…")
	}
	if got := []rune(Ellipsize("abcdefgh", 5)); len(got) != 5 {
		t.Errorf("Ellipsize returned %d runes, want 5", len(got))
	}
	if got := Ellipsize("abc", 5); got != "abc" {
		t.Errorf("a short string must not be marked, got %q", got)
	}
	if got := Ellipsize("abcdef", 1); got != "…" {
		t.Errorf("Ellipsize(n=1) = %q, want just the marker", got)
	}
	if got := Ellipsize("abcdef", 0); got != "" {
		t.Errorf("Ellipsize(n=0) = %q, want empty", got)
	}
	// n==1 and a long string is the boundary where "n-1 runes plus a marker"
	// would produce an empty body and a bare index panic.
	if got := Ellipsize("តើប្រាក់ខែ", 1); got != "…" {
		t.Errorf("Ellipsize(n=1) on Khmer = %q, want just the marker", got)
	}
}

func TestOneLine(t *testing.T) {
	// A query carrying a newline must not break a one-row-per-item table layout.
	if got := OneLine("a\nb\tc", 64); got != "a b c" {
		t.Errorf("OneLine = %q, want %q", got, "a b c")
	}
	if got := OneLine("  a   b  ", 64); got != "a b" {
		t.Errorf("OneLine must collapse runs and trim: %q", got)
	}
	// Truncation counts RUNES, not bytes: the old byte-indexed cut printed
	// mojibake for Khmer.
	khmer := "តើប្រាក់ខែ"
	got := OneLine(khmer, 4)
	if len([]rune(got)) != 4 {
		t.Errorf("OneLine truncation = %q (%d runes), want 4 runes", got, len([]rune(got)))
	}
	if got != string([]rune(khmer)[:3])+"…" {
		t.Errorf("OneLine truncated to %q, want the first 3 runes plus an ellipsis", got)
	}
	if got := OneLine("short", 0); got != "short" {
		t.Errorf("max<=0 means no cap, got %q", got)
	}
}

func TestFirstNonEmptyTestsBlanksButReturnsThemVerbatim(t *testing.T) {
	if got := FirstNonEmpty("", "  ", "third"); got != "third" {
		t.Errorf("FirstNonEmpty = %q, want %q", got, "third")
	}
	// Padding is only an emptiness signal — the value itself is handed over
	// untouched, so a padded host or secret keeps its bytes.
	if got := FirstNonEmpty("  127.0.0.1  "); got != "  127.0.0.1  " {
		t.Errorf("FirstNonEmpty altered its argument: %q", got)
	}
	if got := FirstNonEmpty(""); got != "" {
		t.Errorf("all-blank input must yield the empty string, got %q", got)
	}
}

func TestNullableStringCoercions(t *testing.T) {
	empty := ""
	full := "x"
	if got := DerefString(nil); got != "" {
		t.Errorf("DerefString(nil) = %q, want empty", got)
	}
	if got := DerefString(&empty); got != "" {
		t.Errorf("DerefString(&\"\") = %q, want empty", got)
	}
	if got := DerefString(&full); got != "x" {
		t.Errorf("DerefString(&\"x\") = %q, want %q", got, "x")
	}
	if got := NullIfEmpty(""); got != nil {
		t.Errorf("NullIfEmpty(\"\") = %v, want nil", got)
	}
	if got := NullIfEmpty("x"); got != "x" {
		t.Errorf("NullIfEmpty(\"x\") = %v, want %q", got, "x")
	}
}
