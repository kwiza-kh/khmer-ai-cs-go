package rag

import (
	"fmt"
	"strings"
	"testing"
)

func TestChunkProseExactGap(t *testing.T) {
	text := strings.Repeat("a", 500) + "." + strings.Repeat("b", 500) + "." + strings.Repeat("c", 900)
	runes := []rune(text)
	chunks := chunkProse(text)
	pos := 0
	for i, c := range chunks {
		fmt.Printf("chunk %d: runes[%d:%d] len=%d first=%q last=%q\n", i, pos, pos+len([]rune(c)), len([]rune(c)), string([]rune(c)[:3]), string([]rune(c)[len([]rune(c))-3:]))
		pos += len([]rune(c))
	}
	fmt.Println("total runes:", len(runes))
	// exact boundary reconstruction
	idx := 0
	for _, c := range chunks {
		i := strings.Index(string(runes[idx:]), c)
		if i < 0 { i = strings.Index(text, c) }
		fmt.Printf("  chunk starts at %d, ends at %d\n", idx+i, idx+i+len([]rune(c)))
		idx = idx + i + len([]rune(c))
	}
}
