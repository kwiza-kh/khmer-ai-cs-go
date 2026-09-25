package api

// The dual-credential tests for the admin model routes.
//
// These routes predate Vertex and were written around one assumption: "the
// credential is model_configs.api_key". Under GEMINI_PROVIDER=vertex that
// assumption is false — the credential is the service-account file, the column
// is normally empty — and every branch that acted on it did the wrong thing
// (refused to list models, cancelled the hot-reload). So each test below pins
// ONE of the two transports and asserts what actually leaves the process, with
// the literals spelled out rather than re-derived from the code under test.
//
// The two package-level seams in admin_handlers.go (studioAPIKeyFromDB,
// defaultModelConfigFromDB) are the routes' only database reads. Replacing them
// is what lets these run with no database AND report which credential the
// handler thought it needed: a vertex test that swaps one for a function which
// fails the test proves the column is not consulted at all, which a live
// database could never show.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/security"
)

// testSealerKey is the same 32-byte key internal/security's own tests use; a
// real Sealer (not a stub) is used so the studio tests exercise the actual
// seal/open path the deployment runs.
const testSealerKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func newTestSealer(t *testing.T) *security.Sealer {
	t.Helper()
	sealer, err := security.NewSealer(testSealerKey)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	return sealer
}

// stubRequest is what one request to the Gemini-through-stub looked like. URI
// keeps the query string: the studio model list is defined by `?pageSize=200`,
// and a path-only assertion would not notice it going missing.
type stubRequest struct {
	URI    string
	Path   string
	Auth   string
	APIKey string
}

// modelStub is a stand-in for the endpoint the active transport talks to: the
// AI Studio API root in studio mode, the Vertex location root in vertex mode.
type modelStub struct {
	*httptest.Server
	mu       sync.Mutex
	requests []stubRequest
}

func (s *modelStub) all() []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubRequest(nil), s.requests...)
}

func newModelStub(t *testing.T, reply func(r *http.Request) (int, string)) *modelStub {
	t.Helper()
	stub := &modelStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		uri := r.URL.Path
		if r.URL.RawQuery != "" {
			uri += "?" + r.URL.RawQuery
		}
		stub.requests = append(stub.requests, stubRequest{
			URI:    uri,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			APIKey: r.Header.Get("x-goog-api-key"),
		})
		stub.mu.Unlock()
		status, body := reply(r)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(stub.Close)
	return stub
}

// writeTestServiceAccount writes a throwaway service-account key (a real RSA
// key in Google's PKCS#8 PEM form) whose token_uri points at the stub token
// endpoint, and returns its path. Nothing here is a fixture string: a broken
// PEM/JSON path has to fail the same way it would in production.
func writeTestServiceAccount(t *testing.T, tokenURI, projectID string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"type":         "service_account",
		"project_id":   projectID,
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "vertex-test@example.iam.gserviceaccount.com",
		"token_uri":    tokenURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// vertexEnv wires a working vertex deployment: a token stub the key's own
// token_uri points at, an SA file, and the platform base pointed at platform.
func vertexEnv(t *testing.T, platform *modelStub) {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok-vertex","expires_in":3600}`))
	}))
	t.Cleanup(tokenSrv.Close)
	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", writeTestServiceAccount(t, tokenSrv.URL, "proj-1"))
	t.Setenv("GEMINI_VERTEX_PROJECT", "proj-1")
	t.Setenv("GEMINI_VERTEX_REGION", "asia-southeast1")
	t.Setenv("GEMINI_VERTEX_API_BASE", platform.URL)
}

func studioEnv(t *testing.T, platform *modelStub) {
	t.Helper()
	t.Setenv("GEMINI_PROVIDER", "")
	t.Setenv("GEMINI_API_BASE", platform.URL)
	t.Setenv("GEMINI_VERTEX_API_BASE", "")
}

func listNames(t *testing.T, result any) []string {
	t.Helper()
	rows, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("result has type %T, want []map[string]any", result)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row["name"].(string))
	}
	return out
}

const vertexModelListBody = `{"publisherModels":[` +
	`{"name":"publishers/google/models/gemini-3.5-flash","displayName":"3.5 Flash"},` +
	`{"name":"publishers/google/models/text-embedding-005","displayName":"Embeddings"}]}`

const studioModelListBody = `{"models":[` +
	`{"name":"models/gemini-3.5-flash","displayName":"3.5 Flash"},` +
	`{"name":"models/text-embedding-005","displayName":"Embeddings"}]}`

// TestListAvailableModelsVertexUsesTheServiceAccount — the model picker must
// load on a vertex deployment whose api_key column is EMPTY (the normal state,
// since the private key deliberately never enters the database). Before this,
// the handler answered 400 "未设置 API Key" and never called the platform.
func TestListAvailableModelsVertexUsesTheServiceAccount(t *testing.T) {
	platform := newModelStub(t, func(r *http.Request) (int, string) { return http.StatusOK, vertexModelListBody })
	vertexEnv(t, platform)

	original := studioAPIKeyFromDB
	studioAPIKeyFromDB = func(ctx context.Context, db *pgxpool.Pool, configID int32) string {
		t.Error("vertex mode read model_configs.api_key — the DB key is not the credential there")
		return ""
	}
	t.Cleanup(func() { studioAPIKeyFromDB = original })

	// App with no DB and no Sealer: if the handler reached either, the test
	// would fail (panic) rather than quietly pass.
	app := &App{}
	result, err := app.listAvailableModels(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/7/available", nil), 7)
	if err != nil {
		t.Fatalf("listAvailableModels: %v", err)
	}
	if got := listNames(t, result); len(got) != 1 || got[0] != "gemini-3.5-flash" {
		t.Fatalf("names = %v, want [gemini-3.5-flash]", got)
	}

	reqs := platform.all()
	if len(reqs) != 1 {
		t.Fatalf("%d request(s) reached the platform, want 1", len(reqs))
	}
	if want := "/projects/proj-1/locations/asia-southeast1/publishers/google/models"; reqs[0].Path != want {
		t.Errorf("path = %q, want %q", reqs[0].Path, want)
	}
	if reqs[0].Auth != "Bearer tok-vertex" {
		t.Errorf("Authorization = %q, want the minted service-account token", reqs[0].Auth)
	}
	// The red line of the migration: an AI Studio key must never travel to a
	// Vertex endpoint. Here there is no key at all — but the header must also be
	// absent rather than empty-but-set-as-a-credential.
	if reqs[0].APIKey != "" {
		t.Errorf("x-goog-api-key = %q, want no API-key header on the vertex path", reqs[0].APIKey)
	}
}

// TestListAvailableModelsVertexWithoutServiceAccountFailsLoudly — a vertex
// deployment missing its key file must say so. The point is the opposite of an
// empty list: the operator gets the variable name to fix, not a healthy-looking
// "no models available" console.
func TestListAvailableModelsVertexWithoutServiceAccountFailsLoudly(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", "")

	original := studioAPIKeyFromDB
	studioAPIKeyFromDB = func(ctx context.Context, db *pgxpool.Pool, configID int32) string {
		t.Error("vertex mode read model_configs.api_key — the DB key is not the credential there")
		return ""
	}
	t.Cleanup(func() { studioAPIKeyFromDB = original })

	_, err := (&App{}).listAvailableModels(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), 7)
	var apiErr *ApiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *ApiError", err, err)
	}
	if apiErr.Status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "GEMINI_VERTEX_SA_FILE") {
		t.Errorf("message = %q, want it to name the missing configuration", apiErr.Message)
	}
}

// TestListAvailableModelsStudioUsesTheSealedDBKey — the studio path is
// production today, so it is pinned literally: the config's stored key (sealed
// in the column, exactly as backfill_secrets writes it) is decrypted and sent
// as x-goog-api-key to the AI Studio URL, and nothing else is.
func TestListAvailableModelsStudioUsesTheSealedDBKey(t *testing.T) {
	platform := newModelStub(t, func(r *http.Request) (int, string) { return http.StatusOK, studioModelListBody })
	studioEnv(t, platform)
	sealer := newTestSealer(t)
	sealed, err := sealer.Encrypt("AIza-studio-key-from-db")
	if err != nil {
		t.Fatal(err)
	}

	original := studioAPIKeyFromDB
	var askedConfigID int32
	studioAPIKeyFromDB = func(ctx context.Context, db *pgxpool.Pool, configID int32) string {
		askedConfigID = configID
		return sealed
	}
	t.Cleanup(func() { studioAPIKeyFromDB = original })

	result, err := (&App{Sealer: sealer}).listAvailableModels(
		httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), 42)
	if err != nil {
		t.Fatalf("listAvailableModels: %v", err)
	}
	if askedConfigID != 42 {
		t.Errorf("looked up config %d, want the requested config 42", askedConfigID)
	}
	if got := listNames(t, result); len(got) != 1 || got[0] != "gemini-3.5-flash" {
		t.Fatalf("names = %v, want [gemini-3.5-flash]", got)
	}
	reqs := platform.all()
	if len(reqs) != 1 {
		t.Fatalf("%d request(s) reached the platform, want 1", len(reqs))
	}
	if want := "/models?pageSize=200"; reqs[0].URI != want {
		t.Errorf("URI = %q, want %q", reqs[0].URI, want)
	}
	if reqs[0].APIKey != "AIza-studio-key-from-db" {
		t.Errorf("x-goog-api-key = %q, want the DECRYPTED stored key", reqs[0].APIKey)
	}
	if reqs[0].Auth != "" {
		t.Errorf("Authorization = %q, want none on the studio path", reqs[0].Auth)
	}
}

// TestListAvailableModelsStudioWithoutAKeyStillRefuses — the studio behaviour
// that must NOT change: no stored key is a user error ("未设置 API Key"), not a
// silent empty list, and no request is attempted.
func TestListAvailableModelsStudioWithoutAKeyStillRefuses(t *testing.T) {
	platform := newModelStub(t, func(r *http.Request) (int, string) { return http.StatusOK, studioModelListBody })
	studioEnv(t, platform)

	original := studioAPIKeyFromDB
	studioAPIKeyFromDB = func(ctx context.Context, db *pgxpool.Pool, configID int32) string { return "" }
	t.Cleanup(func() { studioAPIKeyFromDB = original })

	_, err := (&App{}).listAvailableModels(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), 7)
	var apiErr *ApiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *ApiError", err, err)
	}
	if apiErr.Status != http.StatusBadRequest || apiErr.Message != "未设置 API Key" {
		t.Errorf("got %d %q, want 400 %q", apiErr.Status, apiErr.Message, "未设置 API Key")
	}
	if n := len(platform.all()); n != 0 {
		t.Errorf("%d request(s) left the process without a key, want 0", n)
	}
}

// TestUpdateModelConfigVertexRefusesAKeyWrite — under vertex the api_key column
// is not the credential, so accepting a key here would let an operator believe
// they had rotated one. The refusal must also come BEFORE the first write: the
// body below carries a model_name as well, so a guard placed after the other
// fields would touch the (nil) pool here and fail this test.
func TestUpdateModelConfigVertexRefusesAKeyWrite(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", "")

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/models/7",
		strings.NewReader(`{"model_name":"gemini-3.5-flash","api_key":"AIza-pasted-key"}`))
	// The route is platform-admin only, and that gate runs first — so the
	// request has to carry a real caller for the credential branch to be the
	// thing under test.
	req = req.WithContext(context.WithValue(req.Context(), userKey,
		&CurrentUser{UserID: 1, Role: "platform_admin"}))
	_, err := (&App{}).updateModelConfig(httptest.NewRecorder(), req, 7)
	var apiErr *ApiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *ApiError", err, err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "服务账号") {
		t.Errorf("message = %q, want it to say the credential is the service account", apiErr.Message)
	}
}

// TestReloadGeminiFromDBVertexAppliesEditsWithoutAKey — the silent half of the
// bug: the reload used to bail out when api_key was empty, so on a vertex
// deployment an edited model name was stored, answered "已更新", and never took
// effect until the next restart. A nil Sealer here is deliberate: the vertex
// path must not decrypt anything.
func TestReloadGeminiFromDBVertexAppliesEditsWithoutAKey(t *testing.T) {
	platform := newModelStub(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`
	})
	vertexEnv(t, platform)

	serving := gemini.New("", "gemini-2.0-flash", 111)
	if !serving.IsConfigured() {
		t.Fatal("a vertex service built with an empty API key must be live, not mock")
	}

	original := defaultModelConfigFromDB
	defaultModelConfigFromDB = func(ctx context.Context, db *pgxpool.Pool) (string, string, string, int, bool) {
		return "", "gemini-3.5-flash", "", 512, true
	}
	t.Cleanup(func() { defaultModelConfigFromDB = original })

	(&App{Gemini: serving}).reloadGeminiFromDB(context.Background())
	if got := serving.ModelName(); got != "gemini-3.5-flash" {
		t.Fatalf("model after reload = %q, want gemini-3.5-flash — the empty DB key must not cancel the reload", got)
	}
}

// TestReloadGeminiFromDBStudioIsUnchanged — studio keeps both halves of the old
// contract: an empty stored key pushes nothing (the boot credential stays in
// place), and a stored key is decrypted into the serving client. The second
// half is asserted end-to-end, by looking at the header of the next request.
func TestReloadGeminiFromDBStudioIsUnchanged(t *testing.T) {
	platform := newModelStub(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`
	})
	studioEnv(t, platform)
	sealer := newTestSealer(t)

	serving := gemini.New("AIza-key-from-env", "gemini-2.0-flash", 111)
	if !serving.IsConfigured() {
		t.Fatal("studio service with a key must be configured")
	}
	app := &App{Gemini: serving, Sealer: sealer}

	original := defaultModelConfigFromDB
	t.Cleanup(func() { defaultModelConfigFromDB = original })

	// (a) empty stored key → nothing is pushed.
	defaultModelConfigFromDB = func(ctx context.Context, db *pgxpool.Pool) (string, string, string, int, bool) {
		return "", "gemini-3.5-flash", "", 512, true
	}
	app.reloadGeminiFromDB(context.Background())
	if got := serving.ModelName(); got != "gemini-2.0-flash" {
		t.Fatalf("model after an empty-key reload = %q, want the boot model gemini-2.0-flash", got)
	}

	// (b) stored key → decrypted, applied, and used by the next request.
	sealed, err := sealer.Encrypt("AIza-key-from-db")
	if err != nil {
		t.Fatal(err)
	}
	defaultModelConfigFromDB = func(ctx context.Context, db *pgxpool.Pool) (string, string, string, int, bool) {
		return sealed, "gemini-3.5-flash", "", 512, true
	}
	app.reloadGeminiFromDB(context.Background())
	if got := serving.ModelName(); got != "gemini-3.5-flash" {
		t.Fatalf("model after reload = %q, want gemini-3.5-flash", got)
	}
	if _, err := serving.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("chat after reload: %v", err)
	}
	reqs := platform.all()
	if len(reqs) != 1 {
		t.Fatalf("%d request(s) reached the platform, want 1", len(reqs))
	}
	if reqs[0].APIKey != "AIza-key-from-db" {
		t.Errorf("x-goog-api-key = %q, want the reloaded DECRYPTED key", reqs[0].APIKey)
	}
	if want := "/models/gemini-3.5-flash:generateContent"; reqs[0].Path != want {
		t.Errorf("path = %q, want %q", reqs[0].Path, want)
	}
}

// TestCredentialSourceWireValues — the admin console switches on these exact
// strings (ModelItem.credential_source), and the default must stay studio: an
// unset, empty or misspelled GEMINI_PROVIDER can never report a service account
// that does not exist.
func TestCredentialSourceWireValues(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "vertex")
	if got := string(gemini.CredentialSourceOf()); got != "service_account" {
		t.Errorf("vertex credential_source = %q, want \"service_account\"", got)
	}
	if gemini.CredentialSourceOf().IsAPIKey() {
		t.Error("vertex must not report an API-key credential")
	}
	for _, v := range []string{"", "studio", "STUDIO", "vertext", "vertex-ai"} {
		t.Setenv("GEMINI_PROVIDER", v)
		if got := string(gemini.CredentialSourceOf()); got != "api_key" {
			t.Errorf("GEMINI_PROVIDER=%q credential_source = %q, want \"api_key\"", v, got)
		}
		if !gemini.CredentialSourceOf().IsAPIKey() {
			t.Errorf("GEMINI_PROVIDER=%q must keep the API-key credential", v)
		}
	}
}
