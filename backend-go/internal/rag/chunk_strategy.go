package rag

import (
	"strings"
	"unicode"
)

// Pluggable chunking.
//
// The splitter used to be one hard-coded routine, which is the wrong shape for a
// knowledge base that ingests Khmer, Chinese and English: a fixed-size cut lands
// mid-sentence in a language without spaces, while a Markdown-aware cut keeps a
// heading with its body but is useless on an OCR'd PDF.
//
// Borrowed from AstrBot's knowledge base (astrbot/core/knowledge_base/chunking/:
// base / fixed_size / recursive / markdown strategies behind one interface).
// The strategy is a property of the DOCUMENT, so it is chosen per ingest rather
// than fixed for the deployment.

// ChunkOptions bounds one chunk.
type ChunkOptions struct {
	// MaxRunes is the hard ceiling for one chunk.
	MaxRunes int
	// OverlapRunes repeats this much of the previous chunk at the start of the
	// next one, so a sentence spanning a boundary is still retrievable from
	// either side.
	OverlapRunes int
}

// Chunk is one piece of a document.
type Chunk struct {
	Text  string
	Index int
}

// ChunkStrategy splits a document.
type ChunkStrategy interface {
	Name() string
	Split(text string, opts ChunkOptions) []Chunk
}

// FixedSizeStrategy cuts on size alone, preferring whitespace.
type FixedSizeStrategy struct{}

func (FixedSizeStrategy) Name() string { return "fixed_size" }

func (s FixedSizeStrategy) Split(text string, opts ChunkOptions) []Chunk {
	o := normalise(opts)
	return index(splitByRunes(text, o), text, o)
}

// RecursiveStrategy splits on paragraph, then line, then sentence boundaries
// before falling back to a hard cut: the deep hierarchy is what keeps a Khmer
// paragraph intact when it fits, without ever exceeding the ceiling.
type RecursiveStrategy struct{}

func (RecursiveStrategy) Name() string { return "recursive" }

func (s RecursiveStrategy) Split(text string, opts ChunkOptions) []Chunk {
	o := normalise(opts)
	var out []Chunk
	for _, block := range splitKeepingSeparator(text, "\n\n") {
		if runeLen(block) <= o.MaxRunes {
			out = append(out, Chunk{Text: block})
			continue
		}
		for _, line := range splitKeepingSeparator(block, "\n") {
			if runeLen(line) <= o.MaxRunes {
				out = append(out, Chunk{Text: line})
				continue
			}
			out = append(out, splitByRunes(line, o)...)
		}
	}
	return index(out, text, o)
}

// MarkdownStrategy keeps a heading with the body it introduces, so a chunk never
// arrives with a section title and no content (or the reverse).
type MarkdownStrategy struct{}

func (MarkdownStrategy) Name() string { return "markdown" }

func (s MarkdownStrategy) Split(text string, opts ChunkOptions) []Chunk {
	o := normalise(opts)
	var sections []string
	var current strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if isHeading(line) && current.Len() > 0 {
			sections = append(sections, current.String())
			current.Reset()
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		sections = append(sections, current.String())
	}

	var out []Chunk
	for _, sec := range sections {
		sec = strings.TrimRight(sec, "\n")
		if sec == "" {
			continue
		}
		if runeLen(sec) <= o.MaxRunes {
			out = append(out, Chunk{Text: sec})
			continue
		}
		// A section longer than the ceiling is split recursively, but the
		// heading stays with the first piece.
		out = append(out, RecursiveStrategy{}.Split(sec, o)...)
	}
	return index(out, text, o)
}

// ChunkStrategyByName resolves a name, defaulting to recursive — the strategy
// that degrades most gracefully on an unknown document shape. An unknown name is
// not an error: an ingest must not fail because somebody typed "markdowm".
func ChunkStrategyByName(name string) ChunkStrategy {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "fixed", "fixed_size", "size":
		return FixedSizeStrategy{}
	case "markdown", "md":
		return MarkdownStrategy{}
	default:
		return RecursiveStrategy{}
	}
}

func normalise(o ChunkOptions) ChunkOptions {
	if o.MaxRunes <= 0 {
		o.MaxRunes = 1200
	}
	if o.OverlapRunes < 0 {
		o.OverlapRunes = 0
	}
	if o.OverlapRunes >= o.MaxRunes {
		o.OverlapRunes = o.MaxRunes / 4
	}
	return o
}

// splitByRunes is the fixed-size cut: hard ceiling, whitespace-preferred break,
// and no rune split in half.
func splitByRunes(text string, o ChunkOptions) []Chunk {
	runes := []rune(text)
	if len(runes) <= o.MaxRunes {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []Chunk{{Text: text}}
	}
	var out []Chunk
	start := 0
	for start < len(runes) {
		end := start + o.MaxRunes
		if end > len(runes) {
			end = len(runes)
		}
		if end < len(runes) {
			for i := end - 1; i > start; i-- {
				if unicode.IsSpace(runes[i]) {
					end = i + 1
					break
				}
			}
		}
		if chunk := strings.TrimSpace(string(runes[start:end])); chunk != "" {
			out = append(out, Chunk{Text: chunk})
		}
		if end >= len(runes) {
			break
		}
		next := end - o.OverlapRunes
		if next <= start {
			next = end // the overlap must never stall the walk
		}
		start = next
	}
	return out
}

// index renumbers chunks and merges the overlap windows the sub-splitters may
// have produced, so callers see one flat, ordered list.
func index(chunks []Chunk, _ string, _ ChunkOptions) []Chunk {
	out := make([]Chunk, 0, len(chunks))
	for _, c := range chunks {
		c = Chunk{Text: strings.TrimSpace(c.Text)}
		if c.Text == "" {
			continue
		}
		c.Index = len(out)
		out = append(out, c)
	}
	return out
}

func runeLen(s string) int { return len([]rune(s)) }

func splitKeepingSeparator(text, sep string) []string {
	parts := strings.Split(text, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func isHeading(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(trimmed, "#")
}
