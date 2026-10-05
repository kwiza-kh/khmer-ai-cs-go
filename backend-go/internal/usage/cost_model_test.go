package usage

import "testing"

// Embedding spend used to be invisible: the auxiliary observer is fed by the
// generative calls only, so a document ingest and every query hop were free as
// far as the spend gate could tell. The fix routes embedding rows through
// EstimateCostFor, which must price them at the embedding rate — and input only,
// because a vector response has no completion tokens. Billing them at the chat
// input rate would overstate an ingest by an order of magnitude and shed turns
// for no reason.
func TestEstimateCostForPricesEmbeddingsSeparately(t *testing.T) {
	if got, want := EstimateCostFor("gemini-embedding-001", 1_000_000, 0, 0), embeddingPer1M; got != want {
		t.Errorf("embedding cost = %v, want %v", got, want)
	}
	if got := EstimateCostFor("gemini-embedding-001", 0, 1_000_000, 0); got != 0 {
		t.Errorf("embedding billed completion tokens: %v, want 0", got)
	}
	if got, want := EstimateCostFor("models/GEMINI-EMBEDDING-001", 1_000_000, 0, 0), embeddingPer1M; got != want {
		t.Errorf("case-insensitive embedding match failed: got %v want %v", got, want)
	}
	if got, want := EstimateCostFor("gemini-3.8-flash", 1_000_000, 1_000_000, 0), inputPer1M+outputPer1M; got != want {
		t.Errorf("chat cost = %v, want %v", got, want)
	}
	// A negative input count is clamped rather than credited back to the tenant.
	if got := EstimateCostFor("gemini-embedding-001", -5, 0, 0); got < 0 {
		t.Errorf("negative embedding cost = %v", got)
	}
}
