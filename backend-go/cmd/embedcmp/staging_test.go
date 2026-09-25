package main

// Hermetic end-to-end test of the thing this tool is FOR: in ONE process, path A
// talks to AI Studio with a task-conditioned query body while path B talks to
// the Vertex platform with an unconditional one — and the two rank against
// DIFFERENT document vector sets.
//
// Everything is local: two httptest servers stand in for
// generativelanguage.googleapis.com and for the platform (including its token
// endpoint, which the service account's token_uri is pointed at). No test in this
// package reaches a real API, which is the point — the measurement runs against
// production, so the machinery underneath it has to be provable for free.
//
// WHY THIS TEST MATTERS MORE THAN THE OTHERS: the gemini package resolves its
// transport ONCE per Service, from the environment, at construction. The whole
// comparison depends on that staging working (see buildServices), and on path A
// keeping its task field while path B drops it. If either regressed, this tool
// would keep printing a confident report of a comparison it was no longer
// performing — the one failure mode a measurement harness must not have.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// stubRecorder records what each stand-in endpoint was asked.
type stubRecorder struct {
	mu       sync.Mutex
	requests []stubRequest
}

type stubRequest struct {
	Path          string
	APIKeyHeader  string
	Authorization string
	Body          string
}

func (r *stubRecorder) record(req *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, stubRequest{
		Path:          req.URL.Path,
		APIKeyHeader:  req.Header.Get("x-goog-api-key"),
		Authorization: req.Header.Get("Authorization"),
		Body:          string(body),
	})
}

func (r *stubRecorder) forPath(t *testing.T, path string) stubRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, req := range r.requests {
		if req.Path == path {
			return req
		}
	}
	t.Fatalf("no request reached %s; saw %d requests", path, len(r.requests))
	return stubRequest{}
}

// unitVector returns a 768-dim vector that is 1 at position i. Two different
// positions give an orthogonal pair, so the expected ranking is unambiguous.
func unitVector(i int) []float64 {
	out := make([]float64, expectedDims)
	out[i] = 1
	return out
}

// unitVec32 is unitVector as the corpus fixture stores it ([]float32). The width
// matters: cosine refuses to compare vectors of different lengths (returning 0
// rather than a wrong number), so a short fixture would silently make every
// document tie and the test would pass for the wrong reason.
func unitVec32(i int) []float32 {
	out := make([]float32, expectedDims)
	out[i] = 1
	return out
}

// queryBody is the decoded shape the stub cares about.
type queryBody struct {
	TaskType   string `json:"taskType"`
	Instances  []any  `json:"instances"`
	Parameters struct {
		OutputDimensionality int `json:"outputDimensionality"`
	} `json:"parameters"`
	Content struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"content"`
}

func TestBothTransportsCoexistWithOppositeEmbeddingConventions(t *testing.T) {
	rec := &stubRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.record(req)
		switch {
		case strings.HasSuffix(req.URL.Path, "/token"):
			_, _ = w.Write([]byte(`{"access_token":"stub-token","expires_in":3600,"token_type":"Bearer"}`))
		case strings.HasSuffix(req.URL.Path, ":embedContent"):
			// AI Studio: {"embedding":{"values":[…]}}
			writeJSON(t, w, map[string]any{"embedding": map[string]any{"values": unitVector(0)}})
		case strings.HasSuffix(req.URL.Path, ":predict"):
			// Vertex: {"predictions":[{"embeddings":{"values":[…]}}]}
			writeJSON(t, w, map[string]any{"predictions": []any{
				map[string]any{"embeddings": map[string]any{"values": unitVector(1)}},
			}})
		default:
			http.Error(w, "unexpected path "+req.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// The service account's token_uri points at the stub, so minting a bearer
	// token is local too. The key itself is a real RSA key generated for this
	// test — newTokenSource parses it and never sends it anywhere.
	saPath := filepath.Join(t.TempDir(), "sa.json")
	sa := map[string]any{
		"type":         "service_account",
		"project_id":   "stub-project",
		"private_key":  testRSAKeyPEM(t),
		"client_email": "stub@stub-project.iam.gserviceaccount.com",
		"token_uri":    srv.URL + "/token",
	}
	raw, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	if err := os.WriteFile(saPath, raw, 0o600); err != nil {
		t.Fatalf("write service account: %v", err)
	}

	// Both API bases point at the stub; the studio one is read per call, the
	// vertex one at construction, which is exactly the asymmetry buildServices
	// has to handle.
	t.Setenv("GEMINI_API_BASE", srv.URL+"/v1beta")
	t.Setenv("GEMINI_VERTEX_API_BASE", srv.URL+"/v1")
	t.Setenv("GEMINI_PROVIDER", "vertex") // the host is already migrated: path A must override this
	t.Setenv("GEMINI_VERTEX_SA_FILE", saPath)
	t.Setenv("GEMINI_VERTEX_PROJECT", "stub-project")
	t.Setenv("GEMINI_VERTEX_REGION", "asia-southeast1")

	studio, vertexSvc, err := buildServices("AIza-studio-key", saPath, "stub-project", "asia-southeast1")
	if err != nil {
		t.Fatalf("buildServices: %v", err)
	}

	// The staging must be undone: a leftover GEMINI_PROVIDER would silently
	// reroute anything that reads it later in the process.
	if got := os.Getenv("GEMINI_PROVIDER"); got != "vertex" {
		t.Errorf("GEMINI_PROVIDER = %q after buildServices, want the caller's original \"vertex\"", got)
	}

	// Both services must answer a health probe — no mock vectors anywhere.
	if err := probeServices(context.Background(), studio, vertexSvc); err != nil {
		t.Fatalf("probeServices: %v", err)
	}

	studioReq := rec.forPath(t, "/v1beta/models/gemini-embedding-001:embedContent")
	vertexReq := rec.forPath(t, "/v1/projects/stub-project/locations/asia-southeast1/publishers/google/models/gemini-embedding-001:predict")

	// Path A: AI Studio, task-CONDITIONED, API key in the header.
	if studioReq.APIKeyHeader != "AIza-studio-key" {
		t.Errorf("studio request carried x-goog-api-key %q, want the studio key", studioReq.APIKeyHeader)
	}
	var studioBody queryBody
	if err := json.Unmarshal([]byte(studioReq.Body), &studioBody); err != nil {
		t.Fatalf("studio body is not JSON: %v (%s)", err, studioReq.Body)
	}
	if studioBody.TaskType == "" {
		t.Errorf("studio body has no taskType — path A would stop being production retrieval: %s", studioReq.Body)
	}

	// Path B: the platform, UNCONDITIONAL, bearer token, and the width pinned in
	// `parameters` (the model's own default is 3072 while the column is 768).
	if vertexReq.Authorization != "Bearer stub-token" {
		t.Errorf("vertex request carried Authorization %q, want the minted bearer token", vertexReq.Authorization)
	}
	if vertexReq.APIKeyHeader != "" {
		t.Errorf("vertex request leaked an API key header: %q", vertexReq.APIKeyHeader)
	}
	if strings.Contains(vertexReq.Body, "task") {
		t.Errorf("vertex body carries a task field — the premise is that :predict ignores it, and sending it "+
			"anyway would make path B something other than what the migration ships: %s", vertexReq.Body)
	}
	var vertexBody queryBody
	if err := json.Unmarshal([]byte(vertexReq.Body), &vertexBody); err != nil {
		t.Fatalf("vertex body is not JSON: %v (%s)", err, vertexReq.Body)
	}
	if len(vertexBody.Instances) != 1 || vertexBody.Parameters.OutputDimensionality != expectedDims {
		t.Errorf("vertex body = %s, want one instance and parameters.outputDimensionality=%d",
			vertexReq.Body, expectedDims)
	}
}

// TestMeasureRanksEachPathAgainstItsOwnVectors pins the property the whole
// design rests on: path A scores the STORED vectors, path B scores the vectors
// computed for the migration, and the two query vectors come from two different
// transports. A tool that accidentally ranked both paths against one vector set
// would report a difference of exactly zero — a plausible-looking, completely
// false "no quality loss".
func TestMeasureRanksEachPathAgainstItsOwnVectors(t *testing.T) {
	rec := &stubRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.record(req)
		switch {
		case strings.HasSuffix(req.URL.Path, "/token"):
			_, _ = w.Write([]byte(`{"access_token":"stub-token","expires_in":3600}`))
		case strings.HasSuffix(req.URL.Path, ":embedContent"):
			writeJSON(t, w, map[string]any{"embedding": map[string]any{"values": unitVector(0)}})
		case strings.HasSuffix(req.URL.Path, ":predict"):
			// Both transports answer the QUERY with the same vector on purpose:
			// this test isolates the DOCUMENT side, so the query side must be
			// held constant while the document vectors differ (the query-side
			// difference is pinned by the test above).
			writeJSON(t, w, map[string]any{"predictions": []any{
				map[string]any{"embeddings": map[string]any{"values": unitVector(0)}},
			}})
		default:
			http.Error(w, "unexpected "+req.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	saPath := filepath.Join(t.TempDir(), "sa.json")
	raw, _ := json.Marshal(map[string]any{
		"project_id": "stub-project", "private_key": testRSAKeyPEM(t),
		"client_email": "stub@stub-project.iam.gserviceaccount.com", "token_uri": srv.URL + "/token",
	})
	if err := os.WriteFile(saPath, raw, 0o600); err != nil {
		t.Fatalf("write service account: %v", err)
	}
	t.Setenv("GEMINI_API_BASE", srv.URL+"/v1beta")
	t.Setenv("GEMINI_VERTEX_API_BASE", srv.URL+"/v1")
	t.Setenv("GEMINI_VERTEX_REGION", "asia-southeast1")

	studio, vertexSvc, err := buildServices("AIza-studio-key", saPath, "stub-project", "asia-southeast1")
	if err != nil {
		t.Fatalf("buildServices: %v", err)
	}

	// Both paths' query vectors are unitVector(0) out of the stub, and the
	// document vectors are 768-dim too (a width mismatch scores 0 by design, so a
	// short fixture would make this test pass for the wrong reason). The two
	// rankings therefore differ ONLY because the document vector sets differ:
	//   doc 10 → stored e0 (path A: rank 1), vertex e1 (path B: rank 2)
	//   doc 20 → stored e1 (path A: rank 2), vertex e0 (path B: rank 1)
	corpus := []corpusChunk{
		{chunkID: 1, docID: 10, content: "a", stored: unitVec32(0), vertex: unitVec32(1)},
		{chunkID: 2, docID: 20, content: "b", stored: unitVec32(1), vertex: unitVec32(0)},
	}
	studioVec, err := studio.GenerateQueryEmbedding(context.Background(), "q")
	if err != nil {
		t.Fatalf("studio query embedding: %v", err)
	}
	vertexVec, err := vertexSvc.GenerateQueryEmbedding(context.Background(), "q")
	if err != nil {
		t.Fatalf("vertex query embedding: %v", err)
	}
	if cosine(studioVec, vertexVec) < 0.9999 {
		t.Fatalf("the stub answered differently per transport; this test needs both query vectors identical "+
			"so that only the DOCUMENT vectors differ (cosine %.4f)", cosine(studioVec, vertexVec))
	}

	// measure() embeds the query again through both services (cached now), so the
	// ranking it produces is the one this test checks.
	results, err := measure(context.Background(), studio, vertexSvc, corpus, []evalCase{
		{Query: "q", Expect: []int32{10}},
	})
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("measure returned %d results, want 1", len(results))
	}
	got := results[0]
	if got.RankA != 1 {
		t.Errorf("path A ranked the expected document at %d, want 1 (it must use the STORED vectors)", got.RankA)
	}
	if got.RankB != 2 {
		t.Errorf("path B ranked the expected document at %d, want 2 (it must use the VERTEX vectors)", got.RankB)
	}
	if got.TopA != 10 || got.TopB != 20 {
		t.Errorf("top documents = A:%d B:%d, want A:10 B:20 — the two paths are not scoring different vector sets",
			got.TopA, got.TopB)
	}
	// And that difference must reach the diff table as a regression, not be
	// averaged away.
	diffs := diffCases(results, 5)
	if len(diffs) != 1 || diffs[0].Verdict != verdictDropped {
		t.Errorf("diffs = %+v, want a single \"dropped\" row", diffs)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("stub write: %v", err)
	}
}

// testKeyOnce caches one generated key for the whole test binary: RSA key
// generation costs ~100ms, and the tests that need a service account only need
// ANY parseable key — nothing here is ever verified by a real token endpoint.
var (
	testKeyOnce sync.Once
	testKeyPEM  string
	testKeyErr  error
)

// testRSAKeyPEM returns a throwaway PKCS#1 RSA private key in PEM form. The
// vertex token source parses it (that IS the code under test); the key never
// leaves the process and is never presented to anything that could verify it.
func testRSAKeyPEM(t *testing.T) string {
	t.Helper()
	testKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			testKeyErr = err
			return
		}
		testKeyPEM = string(pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		}))
	})
	if testKeyErr != nil {
		t.Fatalf("generate test RSA key: %v", testKeyErr)
	}
	return testKeyPEM
}
