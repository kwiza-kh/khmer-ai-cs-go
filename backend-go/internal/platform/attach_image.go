package platform

import (
	"context"
	"time"

	"khmer-ai-cs-go/internal/rag"
)

// attach-doc-image — send the matched document's picture back with the answer.
//
// The customer's question is often a photo and four words ("do you have this?"),
// and a text answer without the merchant's own product photo is half an answer.
// The picture is the one stored beside the knowledge document
// (knowledge_documents.media_key, migration 080); the answer already grounded on
// that document, so this stage only decides whether shipping the picture is
// safe.
//
// Deterministic on purpose. A model-written media URL is the class of injection
// the inbox guard exists for (unsafeReplyMediaURL), and a model that picks the
// wrong product photo is worse than no photo at all: the merchant's catalogue is
// the only source, selected here by retrieval evidence.
//
// A miss costs nothing — the turn delivers exactly the text reply it would have.

// docImageAmbiguityRatio — how close a competing document must be for the
// picture to be withheld. Two products at 0.81 and 0.80 mean the customer's
// photo could be either; the honest answer is a question, not a photo of the
// wrong item. Relative (not absolute) because the fused scores are not one
// scale: lexical, trigram and cosine all land in the same list.
const docImageAmbiguityRatio = 0.9

// docImageURLTTL — the outbound URL's requested validity. It only matters when
// no public base URL is configured; the provider fetches the picture within
// seconds of the send, and providers that cache it themselves keep working.
const docImageURLTTL = 6 * time.Hour

// chooseDocImage decides which stored picture (if any) rides along with one
// reply. Pure so every gate is testable without a database or a channel.
func chooseDocImage(sources []rag.Source, hasMatch, canned, claimsHandoff bool, caps Capabilities) (string, bool) {
	if canned || claimsHandoff || !hasMatch || len(sources) == 0 {
		return "", false
	}
	if !caps.Media.Supports(MediaImage) {
		return "", false
	}
	top := sources[0]
	if top.MediaKey == "" {
		return "", false
	}
	for _, s := range sources[1:] {
		if s.MediaKey == "" || s.MediaKey == top.MediaKey {
			continue
		}
		if s.Score >= top.Score*docImageAmbiguityRatio {
			return "", false
		}
	}
	return top.MediaKey, true
}

// stageAttachDocImage records the decision on the turn. persist-and-deliver
// resolves the key into the outbound payload, so a turn that never reaches
// delivery never signs a URL.
func (p *Pipeline) stageAttachDocImage(_ context.Context, t *inboundTurn) (bool, error) {
	if t.Config == nil {
		return true, nil
	}
	ch, err := NewChannel(p, t.Config)
	if err != nil {
		// Unknown/unsupported channel: the text reply still goes out.
		return true, nil
	}
	if key, ok := chooseDocImage(t.GroundCtx.Sources, t.GroundCtx.HasMatch, t.Canned, t.ClaimsHandoff, ch.Caps()); ok {
		t.AttachMediaKey = key
	}
	return true, nil
}

// docImageURL resolves a stored key for the outbound provider. "" means the
// picture is unreachable right now, and the caller degrades to text only — an
// empty media_url makes some providers reject the whole message.
func (p *Pipeline) docImageURL(key string) string {
	if p.Media == nil || key == "" {
		return ""
	}
	return p.Media.PublicOrPresigned(key, docImageURLTTL)
}
