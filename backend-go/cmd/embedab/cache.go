package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// vectorCache persists one vector per (variant, model, prefix-mode, exact text
// bytes) so that a rerun — or a fourth variant that shares a model with an
// earlier one — costs no API calls for texts already embedded.
//
// The key includes the model AND the prefix mode because the whole point of the
// tool is comparing those; keying by text alone would make the second variant
// silently reuse the first variant's vectors and report "identical quality".
//
// The file is a plain JSON object so an operator can inspect it, and it is
// written atomically (temp file + rename) so a crash mid-run cannot leave a
// truncated cache that then poisons the next measurement with half a corpus.
type vectorCache struct {
	mu      sync.Mutex
	path    string
	entries map[string]cacheEntry
	dirty   bool
}

type cacheEntry struct {
	Vec []float32 `json:"v"`
	MS  int64     `json:"ms,omitempty"` // network latency of the originating call
}

func loadCache(path string) (*vectorCache, error) {
	c := &vectorCache{path: path, entries: map[string]cacheEntry{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, fmt.Errorf("read cache: %w", err)
	}
	if len(raw) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c.entries); err != nil {
		// A corrupt cache must not fail the run — it only means the API calls
		// will be repeated. Say so rather than silently discarding it.
		fmt.Fprintf(os.Stderr, "embedab: cache %s is unreadable (%v); starting empty\n", path, err)
		c.entries = map[string]cacheEntry{}
	}
	return c, nil
}

func cacheKey(variant, model string, prefixes bool, text string) string {
	mode := "p0"
	if prefixes {
		mode = "p1"
	}
	sum := sha256.Sum256([]byte(text))
	return variant + "|" + model + "|" + mode + "|" + hex.EncodeToString(sum[:])
}

func (c *vectorCache) has(variant, model string, prefixes bool, text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[cacheKey(variant, model, prefixes, text)]
	return ok
}

func (c *vectorCache) get(variant, model string, prefixes bool, text string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[cacheKey(variant, model, prefixes, text)]
	return e.Vec, ok
}

func (c *vectorCache) put(variant, model string, prefixes bool, text string, vec []float32, ms int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[cacheKey(variant, model, prefixes, text)] = cacheEntry{Vec: vec, MS: ms}
	c.dirty = true
}

// save writes the cache atomically. Called after each variant so an aborted run
// keeps everything it paid for.
func (c *vectorCache) save(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	raw, err := json.Marshal(c.entries)
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename cache: %w", err)
	}
	c.dirty = false
	return nil
}
