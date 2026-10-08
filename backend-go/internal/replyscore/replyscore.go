// Package replyscore is the platform's rubric for one customer-service reply,
// scored from the eval file's own facts rather than from a model's opinion.
//
// It moved out of cmd/jeveval so that every tool that grades replies grades them
// the same way: jeveval runs the judge on top of these checks, and claudeeval
// compares two providers on the same cases. Two copies of a rubric drift, and a
// drifted rubric reports a model change as a quality change.
//
// The checks are deliberately mechanical — a required fact is present or not, a
// forbidden literal is there or not, and the format invariants hold for every
// reply (the prompt forbids them and the sanitizer is supposed to make some
// impossible). Anything that needs judgment goes through the judge model, not
// through this package.
package replyscore

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// Case is one question with the facts its answer must carry.
type Case struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Language string `json:"language"`
	Question string `json:"question"`
	// MustInclude: one entry per REQUIRED fact; "|" separates accepted
	// spellings, so "$32.00|$32" is satisfied by either. Written as
	// alternatives because a correct answer may quote a price with or without
	// trailing zeros, and pinning one spelling measures the model's style
	// rather than its facts.
	MustInclude []string `json:"must_include"`
	// MustNotContain: literals that must be absent — an invented figure, a
	// citation marker, a leaked source title. Keep these to things that are
	// WRONG in any correct answer.
	MustNotContain []string `json:"must_not_contain"`
	Notes          string   `json:"notes"`
}

// RubricLevel is one line of the file's human rubric (score → meaning), printed
// beside the judge's average so the number is readable.
type RubricLevel struct {
	Score   int    `json:"score"`
	Meaning string `json:"meaning"`
}

// File is the eval corpus as stored in kb/evals/reply_eval.json.
type File struct {
	Corpus string        `json:"corpus"`
	Rubric []RubricLevel `json:"rubric"`
	Cases  []Case        `json:"cases"`
}

// Load reads an eval corpus.
func Load(path string) (File, error) {
	var file File
	raw, err := os.ReadFile(path)
	if err != nil {
		return file, err
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return file, err
	}
	if len(file.Cases) == 0 {
		return file, fmt.Errorf("eval file has no cases: %s", path)
	}
	return file, nil
}

// MissingFacts are the required facts the reply did not carry.
func MissingFacts(c Case, reply string) []string {
	var missing []string
	for _, group := range c.MustInclude {
		if !containsAny(reply, group) {
			missing = append(missing, group)
		}
	}
	return missing
}

func containsAny(reply, group string) bool {
	for _, alt := range strings.Split(group, "|") {
		alt = strings.TrimSpace(alt)
		if alt != "" && strings.Contains(reply, alt) {
			return true
		}
	}
	return false
}

// Leaked are the forbidden literals that appear in the reply.
func Leaked(c Case, reply string) []string {
	var out []string
	for _, lit := range c.MustNotContain {
		for _, alt := range strings.Split(lit, "|") {
			alt = strings.TrimSpace(alt)
			if alt != "" && strings.Contains(strings.ToLower(reply), strings.ToLower(alt)) {
				out = append(out, alt)
			}
		}
	}
	return out
}

// FormatProblems are the invariants that hold for EVERY reply, so they live in
// code rather than in every case's JSON: the prompt forbids them and the
// sanitizer is supposed to make some of them impossible.
func FormatProblems(c Case, reply string) []string {
	var out []string
	if strings.TrimSpace(reply) == "" {
		out = append(out, "empty reply")
	}
	for _, bad := range []string{"\u200b", "\u200c", "\u200d", "\ufeff", "\u00a0"} {
		if strings.Contains(reply, bad) {
			out = append(out, fmt.Sprintf("invisible character %q survived the sanitizer", bad))
		}
	}
	for _, bad := range []string{"**", "##", "```", "| ---", "[Source", "(Source"} {
		if strings.Contains(reply, bad) {
			out = append(out, "markup/citation marker "+bad)
		}
	}
	// The old prompt told every reply, whatever its language, to end a handoff
	// with the Chinese sentence. A Khmer customer reading it is a failure this
	// check is here to keep from coming back.
	if c.Language == "km" && strings.Contains(reply, "已为您转接") {
		out = append(out, "Chinese handoff sentence in a Khmer reply")
	}
	if KhmerNumeralsIn(reply) {
		out = append(out, "Khmer numerals in a reply (prices and quantities are recorded in ASCII digits)")
	}
	if c.Language == "km" {
		// Deliberately a FLOOR, not a target. An answer about the company legitimately
		// carries a Latin-heavy legal name and address ("WANFANG INSULATION
		// PACKAGING MATERIAL CO., LTD, #777, Road No. 2, …"), and the first version
		// of this check failed exactly that reply at a 50% threshold while the judge
		// scored it 12/12. Two conditions, so it still catches what it is for — a
		// reply that came back in English or Chinese: essentially no Khmer at all.
		if n := KhmerLetters(reply); n < 20 || KhmerLetterRatio(reply) < 0.25 {
			out = append(out, fmt.Sprintf("customer asked in Khmer but the reply has only %d Khmer letter(s)", n))
		}
	}
	return out
}

// KhmerLetters counts letters in the Khmer block (U+1780–U+17FF).
func KhmerLetters(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x1780 && r <= 0x17FF && unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// KhmerNumeralsIn — U+17E0–U+17E9. The reply sanitizer already maps these, so a
// hit here means either the sanitizer was bypassed or the reply came from a path
// that does not go through it; both are worth failing a run over.
func KhmerNumeralsIn(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= '០' && r <= '៩' })
}

// KhmerLetterRatio is the share of Khmer code points among all letters. It
// ignores digits, punctuation, spaces and the Latin brand names/units a correct
// Khmer reply legitimately contains.
func KhmerLetterRatio(s string) float64 {
	var khmer, letters int
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if r >= 0x1780 && r <= 0x17FF {
			khmer++
		}
	}
	if letters == 0 {
		return 0
	}
	return float64(khmer) / float64(letters)
}
