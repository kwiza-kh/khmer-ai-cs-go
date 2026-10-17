package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// searchCacheTTL bounds how long an identical question reuses its retrieval.
//
// The expensive leg is the query embedding, and it is also the leg that hits the
// provider's quota — four "Resource exhausted" warnings in three days (2026-10-16),
// each one silently dropping semantic recall for every tenant. Customer service
// repeats questions: the same customer re-asking, an agent re-sending, two people
// asking the same FAQ within a minute. A 45-second window absorbs those bursts.
//
// Invalidation is a per-tenant version counter, bumped by every knowledge-base
// write, so an edit is visible on the next question instead of after the TTL.
const searchCacheTTL = 45 * time.Second

// searchCacheVersionTTL keeps the counter alive; if it ever expires and restarts
// at 0, cache entries (which live seconds) expire long before a stale hit matters.
const searchCacheVersionTTL = 30 * 24 * time.Hour

func searchCacheVersionKey(userID int32) string {
	return fmt.Sprintf("rag:kbver:%d", userID)
}

// searchCacheKey is (tenant, KB version, query, topK). The query is hashed: a
// customer's sentence must never become a Redis key anyone can read back.
func searchCacheKey(userID int32, version int64, query string, topK int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%s|%d", userID, version, query, topK)))
	return "rag:search:" + hex.EncodeToString(sum[:16])
}

// searchCacheVersion reads the tenant's current KB version (0 when unset).
func (s *Service) searchCacheVersion(ctx context.Context, userID int32) int64 {
	if s.Redis == nil {
		return 0
	}
	raw, err := s.Redis.GetString(ctx, searchCacheVersionKey(userID))
	if err != nil || raw == "" {
		return 0
	}
	var v int64
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		return 0
	}
	return v
}

// bumpSearchCache marks one tenant's cached retrievals stale. Called from every
// knowledge-base write; a Redis failure only means a stale hit for up to the TTL.
func (s *Service) bumpSearchCache(ctx context.Context, userID int32) {
	if s.Redis == nil {
		return
	}
	_, _ = s.Redis.IncrCounter(ctx, searchCacheVersionKey(userID), searchCacheVersionTTL)
}

// bumpSearchCacheForDoc resolves the tenant of a document that just changed
// searchability and invalidates that tenant's cache. Only the two transitions
// matter — a document becoming searchable and a document disappearing — so this
// runs on the index worker and on delete, not on every query.
func (s *Service) bumpSearchCacheForDoc(ctx context.Context, docID int32) {
	if s.Redis == nil {
		return
	}
	var userID int32
	if err := s.DB.QueryRow(ctx, "SELECT uploaded_by FROM knowledge_documents WHERE doc_id = $1", docID).Scan(&userID); err != nil {
		return
	}
	s.bumpSearchCache(ctx, userID)
}

func (s *Service) cachedSearch(ctx context.Context, key string) ([]Source, bool) {
	if s.Redis == nil {
		return nil, false
	}
	raw, err := s.Redis.GetString(ctx, key)
	if err != nil || raw == "" {
		return nil, false
	}
	var out []Source
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil, false
	}
	return out, true
}

func (s *Service) storeSearch(ctx context.Context, key string, out []Source) {
	if s.Redis == nil || len(out) == 0 {
		return
	}
	blob, err := json.Marshal(out)
	if err != nil {
		return
	}
	_ = s.Redis.SetString(ctx, key, string(blob), searchCacheTTL)
}
