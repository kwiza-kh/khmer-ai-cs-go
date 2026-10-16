package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"khmer-ai-cs-go/internal/rag"
)

// Keeping the merchant's own picture beside the extracted text.
//
// A product photo is the thing a customer asks about ("do you have this?"), so
// the answer is only complete when it carries the photo back. The row stores the
// R2 object key (knowledge_documents.media_key, migration 080) and every reader
// resolves it through storager2 — keys survive a public-base-URL change, and the
// storefront-through-WhatsApp path needs a URL the provider itself can fetch.

// kbMediaURLTTL — validity requested for a resolved picture URL. It only matters
// when no public base URL is configured (PublicOrPresigned ignores it then); the
// list re-resolves on every page load and the send path resolves right before
// delivery, so one page view or one outbound send is all it has to outlive.
const kbMediaURLTTL = 6 * time.Hour

// kbImageMime returns the mime a KB picture is stored under, decided by the
// bytes and never by the client-supplied filename: a ".jpg" that is really a PDF
// must not reach the bucket as an image, and the vision read already used the
// extension's mime. "" means "not a picture we keep".
func kbImageMime(data []byte) string {
	switch sniffImageType(data) {
	case "image/jpeg", "image/png", "image/webp":
		return sniffImageType(data)
	default:
		return ""
	}
}

// kbAssetKey content-addresses one picture inside the tenant's own prefix:
// re-uploading the same photo overwrites one object instead of accumulating
// copies, and the tenant id in the path keeps the bucket readable per tenant.
func kbAssetKey(tenantID int32, data []byte, mime string) string {
	sum := sha256.Sum256(data)
	ext := ".jpg"
	switch mime {
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	}
	return fmt.Sprintf("kb/%d/%s%s", tenantID, hex.EncodeToString(sum[:8]), ext)
}

// storeKnowledgeImage keeps one uploaded picture and returns its object key.
func (a *App) storeKnowledgeImage(ctx context.Context, tenantID int32, filename string, data []byte) (string, error) {
	if a.Media == nil || !a.Media.Enabled() {
		return "", fmt.Errorf("r2 not configured")
	}
	mime := kbImageMime(data)
	if mime == "" {
		return "", fmt.Errorf("unrecognised image bytes for %s", filepath.Base(filename))
	}
	key := kbAssetKey(tenantID, data, mime)
	putCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := a.Media.PutObject(putCtx, key, data, mime); err != nil {
		return "", err
	}
	return key, nil
}

// mediaURLFor resolves a stored object key into a URL a browser or a channel
// provider can fetch. "" means the picture is unreachable right now (R2 off,
// signing failed): callers degrade to text instead of shipping an empty
// media_url, which some providers answer with a 400 for the whole message.
func (a *App) mediaURLFor(key string) string {
	if a.Media == nil || key == "" || !a.Media.Enabled() {
		return ""
	}
	return a.Media.PublicOrPresigned(key, kbMediaURLTTL)
}

// attachKnowledgeMediaURL adds the resolved media_url to one document JSON the
// UI consumes (the stored key stays in the payload for support/debugging).
//
// The scanner hands media_key over as *string (a nullable column), while the
// upload path has a plain string: both must resolve, or the library would show
// the picture only for the upload that just happened and never again — which is
// exactly what the first production smoke of this feature caught (2026-10-16:
// doc list/detail returned media_key but no media_url).
func (a *App) attachKnowledgeMediaURL(doc map[string]any) {
	if doc == nil {
		return
	}
	var key string
	switch v := doc["media_key"].(type) {
	case string:
		key = v
	case *string:
		if v != nil {
			key = *v
		}
	}
	if key == "" {
		return
	}
	if u := a.mediaURLFor(key); u != "" {
		doc["media_url"] = u
	}
}

// dropKnowledgeImage deletes one stored picture. Best-effort on purpose: the
// document row is already gone by the time this runs, so a bucket failure can
// only leave an unreachable object behind — worth a log line, never an error
// the operator sees after a successful delete.
func (a *App) dropKnowledgeImage(ctx context.Context, key string) {
	if key == "" || a.Media == nil || !a.Media.Enabled() {
		return
	}
	if err := a.Media.DeleteObject(ctx, key); err != nil && a.Logger != nil {
		a.Logger.Warn("kb image delete failed; object left in the bucket", "key", key, "error", err.Error())
	}
}

// isKnowledgeImageFile reports whether this upload keeps a picture at all — the
// same question rag.IsImage answers for extraction, asked here for storage.
func isKnowledgeImageFile(filename string) bool { return rag.IsImage(filename) }
