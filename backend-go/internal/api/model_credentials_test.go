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
	"net/url"
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
//
// The base carries a /v1 segment exactly as the production base does
// (VertexPlatformBase), so the catalog's version substitution is exercised
// against a realistic root rather than a version-less test-only one.
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
	t.Setenv("GEMINI_VERTEX_API_BASE", platform.URL+"/v1")
}

func studioEnv(t *testing.T, platform *modelStub) {
	t.Helper()
	t.Setenv("GEMINI_PROVIDER", "")
	t.Setenv("GEMINI_API_BASE", platform.URL)
	t.Setenv("GEMINI_VERTEX_API_BASE", "")
}

// listRows is the handler's array result, asserted to be the shape the console
// reads ([]map[string]any) rather than a typed struct: the wire keys are the
// contract, and a struct would only prove the struct.
func listRows(t *testing.T, result any) []map[string]any {
	t.Helper()
	rows, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("result has type %T, want []map[string]any", result)
	}
	return rows
}

func listNames(t *testing.T, result any) []string {
	t.Helper()
	rows := listRows(t, result)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row["name"].(string))
	}
	return out
}

// vertexModelListBody is the v1beta1 publisher-model envelope as the platform
// actually answers it: no displayName anywhere, launchStage on some entries,
// supportedActions on SOME and absent on others (measured 2026-09-25).
//
// It deliberately does NOT mention gemini-3.5-flash. That is the trap this whole
// change is about: the model serving asia-southeast1 traffic appears in neither
// that region's list nor reliably anywhere, so the catalog has to union it in.
const vertexModelListBody = `{"publisherModels":[` +
	`{"name":"publishers/google/models/gemini-2.5-flash","launchStage":"GA","supportedActions":["generateContent"],"versionId":"3"},` +
	`{"name":"publishers/google/models/text-embedding-005","launchStage":"GA"}]}`

const studioModelListBody = `{"models":[` +
	`{"name":"models/gemini-3.5-flash","displayName":"3.5 Flash"},` +
	`{"name":"models/text-embedding-005","displayName":"Embeddings"}]}`

// TestListAvailableModelsVertexUsesTheServiceAccount — the model picker must
// load on a vertex deployment whose api_key column is EMPTY (the normal state,
// since the private key deliberately never enters the database). Before this,
// the handler answered 400 "未设置 API Key" and never called the platform.
//
// It also pins the two fixes that are the point of this endpoint: the request
// goes to the v1beta1 publisher route (the v1 one 404s, which is what produced
// the 502 here), and the configured model is unioned in — the stub's list does
// NOT contain it, exactly as asia-southeast1 does not list gemini-3.5-flash.
func TestListAvailableModelsVertexUsesTheServiceAccount(t *testing.T) {
	platform := newModelStub(t, func(r *http.Request) (int, string) { return http.StatusOK, vertexModelListBody })
	vertexEnv(t, platform)

	original := studioAPIKeyFromDB
	studioAPIKeyFromDB = func(ctx context.Context, db *pgxpool.Pool, configID int32) string {
		t.Error("vertex mode read model_configs.api_key — the DB key is not the credential there")
		return ""
	}
	t.Cleanup(func() { studioAPIKeyFromDB = original })

	// No DB and no Sealer: if the handler reached either, the test would fail
	// (panic) rather than quietly pass. The Gemini service is only here to say
	// which model this deployment is serving.
	app := &App{Gemini: gemini.New("", "gemini-3.5-flash", 128)}
	result, err := app.listAvailableModels(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/7/available", nil), 7)
	if err != nil {
		t.Fatalf("listAvailableModels: %v", err)
	}
	// The serving model first, then the region's own list, in order and without
	// duplicates — a duplicate would show the same model twice in the dropdown.
	got := listNames(t, result)
	want := []string{"gemini-3.5-flash", "gemini-2.5-flash", "text-embedding-005"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("duplicate model %q in %v", n, got)
		}
		seen[n] = true
	}

	// The wire shape: every field the console's AvailableModel declares.
	rows := listRows(t, result)
	if rows[0]["available"] != true || rows[0]["launch_stage"] != "" || rows[0]["capability"] != "chat" {
		t.Errorf("unioned row = %v, want available=true, launch_stage=\"\" (unknown) and capability=chat", rows[0])
	}
	if rows[0]["display_name"] != "Gemini 3.5 Flash" {
		t.Errorf("display_name = %v, want the derived label", rows[0]["display_name"])
	}
	if rows[1]["launch_stage"] != "GA" || rows[1]["capability"] != "chat" || rows[1]["available"] != true {
		t.Errorf("listed row = %v, want launch_stage=GA, capability=chat, available=true", rows[1])
	}
	// The second entry carried no supportedActions at all — the regional shape
	// that must not make an entry unusable.
	if rows[2]["capability"] != "embedding" || rows[2]["available"] != true {
		t.Errorf("embedding row = %v, want capability=embedding and available=true", rows[2])
	}

	reqs := platform.all()
	if len(reqs) != 1 {
		t.Fatalf("%d request(s) reached the platform, want 1", len(reqs))
	}
	// The catalog route is /v1beta1/publishers/google/models. The old assertion
	// here was .../locations/{l}/models — the project-scoped route that lists the
	// project's OWN models, whose names every URL this client builds would then
	// mis-address.
	if want := "/v1beta1/publishers/google/models"; reqs[0].Path != want {
		t.Errorf("path = %q, want %q — the v1 form of this route answers 404", reqs[0].Path, want)
	}
	if want := "pageSize=100"; reqs[0].URI != reqs[0].Path+"?"+want {
		t.Errorf("URI = %q, want the catalog page size", reqs[0].URI)
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
	// Both entries the relay returned are offered, in the relay's order, with no
	// duplicates. The embedding is here deliberately: the pre-catalog studio list
	// dropped every non-Gemini name, so an embedding model could never be picked
	// even though the capability vocabulary has a word for it.
	got := listNames(t, result)
	want := []string{"gemini-3.5-flash", "text-embedding-005"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("duplicate model %q in %v", n, got)
		}
		seen[n] = true
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
	defaultModelConfigFromDB = func(ctx context.Context, db *pgxpool.Pool) (string, string, string, int, string, bool) {
		return "", "gemini-3.5-flash", "", 512, "", true
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
	defaultModelConfigFromDB = func(ctx context.Context, db *pgxpool.Pool) (string, string, string, int, string, bool) {
		return "", "gemini-3.5-flash", "", 512, "", true
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
	defaultModelConfigFromDB = func(ctx context.Context, db *pgxpool.Pool) (string, string, string, int, string, bool) {
		return sealed, "gemini-3.5-flash", "", 512, "", true
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

// ============================================
// Region-parameterised listing (the admin region selector)
// ============================================

// TestListAvailableModelsOmittedRegionUsesTheConfiguredRegion — omitting ?region=
// is not the same as asking about some default: it must mean the region the
// server is configured with, which is what keeps the console's pre-selector
// request behaving exactly as it did.
//
// With GEMINI_VERTEX_API_BASE pointing at the stub, the region cannot appear in
// the request HOST (that mapping is pinned against the real hosts in the gemini
// package), so it is read back out of the warning the catalog attaches to a
// failed listing — the same string the operator sees.
//
// Every entry stays available in both calls. `available` is true unless there is
// positive evidence against a model, and a listing that failed is evidence about
// the listing: the measured production case is gemini-3.5-flash answering 200 in
// asia-southeast1 while appearing in none of its nine listed entries.
func TestListAvailableModelsOmittedRegionUsesTheConfiguredRegion(t *testing.T) {
	platform := newModelStub(t, func(r *http.Request) (int, string) {
		return http.StatusNotFound, `{"error":{"message":"Requested entity was not found."}}`
	})
	vertexEnv(t, platform)

	app := &App{Gemini: gemini.New("", "gemini-3.5-flash", 128)}
	rec := httptest.NewRecorder()
	result, err := app.listAvailableModels(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/7/available", nil), 7)
	if err != nil {
		t.Fatalf("listAvailableModels: %v", err)
	}
	warning := rec.Header().Get(modelListWarningHeader)
	if !strings.Contains(warning, "asia-southeast1") || !strings.Contains(warning, "404") {
		t.Errorf("warning = %q, want the CONFIGURED region and the cause", warning)
	}
	rows := listRows(t, result)
	if rows[0]["name"] != "gemini-3.5-flash" {
		t.Fatalf("first row = %v, want the serving model first even though nothing listed it", rows[0])
	}
	if rows[0]["available"] != true {
		t.Error("the serving model must stay available in its own region — the platform's silence is not evidence")
	}
	if len(rows) < 2 {
		t.Errorf("rows = %v, want the last-resort names beside the serving model", rows)
	}
	for _, row := range rows {
		if row["available"] != true {
			t.Errorf("row = %v, want available=true — a failed listing is not evidence about a model", row)
		}
	}

	// The same failure asked about ANOTHER region: still every entry available,
	// and the warning names the region that was actually asked about.
	rec = httptest.NewRecorder()
	result, err = app.listAvailableModels(rec,
		httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/7/available?region=us-central1", nil), 7)
	if err != nil {
		t.Fatalf("a foreign region must not be a hard failure either: %v", err)
	}
	if got := rec.Header().Get(modelListWarningHeader); !strings.Contains(got, "us-central1") {
		t.Errorf("warning = %q, want the requested region", got)
	}
	rows = listRows(t, result)
	if rows[0]["name"] != "gemini-3.5-flash" || rows[0]["available"] != true {
		t.Errorf("first row = %v, want the serving model offered and still available", rows[0])
	}
	// Nothing about this is a 500 — that is the contract — and the body is still
	// the bare array the console already parses.
	if len(rows) < 2 {
		t.Errorf("rows = %v, want the last-resort names beside the serving model", rows)
	}

	// Asserted through the real handler wrapper, so "not a hard failure" is an
	// HTTP status and an encodable body, not merely a nil error.
	wrapped := app.handle(func(w http.ResponseWriter, r *http.Request) (any, error) {
		return app.listAvailableModels(w, r, 7)
	})
	rec = httptest.NewRecorder()
	wrapped(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/7/available?region=us-central1", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — a region whose listing fails is a degraded picker, not an error", rec.Code)
	}
	if rec.Header().Get(modelListWarningHeader) == "" {
		t.Error("the degraded listing lost its warning on the way out")
	}
	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body = %s (err %v), want the bare array", rec.Body.String(), err)
	}
	if len(body) == 0 || body[0]["name"] != "gemini-3.5-flash" {
		t.Errorf("body = %s, want the serving model in the array", rec.Body.String())
	}
}

// TestListAvailableModelsRejectsARegionThatCouldSteerTheHost — the region is
// interpolated into the request host, and that request carries a
// service-account bearer token, so a rejected value must be rejected BEFORE any
// request is built. The stub's zero request count is the assertion that matters:
// a 400 with the request already sent would still have leaked a credential.
func TestListAvailableModelsRejectsARegionThatCouldSteerTheHost(t *testing.T) {
	platform := newModelStub(t, func(r *http.Request) (int, string) {
		return http.StatusOK, vertexModelListBody
	})
	vertexEnv(t, platform)

	app := &App{Gemini: gemini.New("", "gemini-3.5-flash", 128)}
	for _, bad := range []string{"evil.com", "asia/southeast1", "../v1", "asia_southeast1", "asia southeast1", "-asia", "asia--southeast1"} {
		rec := httptest.NewRecorder()
		_, err := app.listAvailableModels(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/admin/models/7/available?region="+url.QueryEscape(bad), nil), 7)
		var apiErr *ApiError
		if !errors.As(err, &apiErr) {
			t.Errorf("region %q: err = %v (%T), want *ApiError", bad, err, err)
			continue
		}
		if apiErr.Status != http.StatusBadRequest || !strings.Contains(apiErr.Message, "region") {
			t.Errorf("region %q: got %d %q, want 400 naming the region", bad, apiErr.Status, apiErr.Message)
		}
	}
	if n := len(platform.all()); n != 0 {
		t.Fatalf("%d request(s) left the process for an invalid region, want 0 — the token must never be aimed at a caller-chosen host", n)
	}
}

// TestVertexRegionsEndpoint — the selector's payload: the deployment's own
// region first and reported as current, every candidate labelled, no duplicates.
func TestVertexRegionsEndpoint(t *testing.T) {
	platform := newModelStub(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"candidates":[]}`
	})
	vertexEnv(t, platform)
	// A real serving service, not a bare App: `current` is the region the SERVICE
	// is on (the environment's value at boot, then the console's last switch), so
	// an App with no service has no region to report at all.
	app := &App{Gemini: gemini.New("", "gemini-3.5-flash", 0)}
	result, err := app.vertexRegions(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/vertex-regions", nil))
	if err != nil {
		t.Fatalf("vertexRegions: %v", err)
	}
	out, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result has type %T, want map[string]any", result)
	}
	if out["current"] != "asia-southeast1" {
		t.Errorf("current = %v, want the configured region", out["current"])
	}
	regions, ok := out["regions"].([]map[string]any)
	if !ok {
		t.Fatalf("regions has type %T, want []map[string]any", out["regions"])
	}
	if len(regions) < 2 {
		t.Fatalf("regions = %v, want a candidate list", regions)
	}
	if regions[0]["id"] != "asia-southeast1" || regions[0]["label"] != "asia-southeast1 (Singapore)" {
		t.Errorf("first region = %v, want the configured region first, labelled", regions[0])
	}
	seen := map[any]int{}
	for _, r := range regions {
		if r["id"] == nil || r["label"] == nil || r["id"] == "" || r["label"] == "" {
			t.Errorf("region %v is missing an id or a label", r)
		}
		seen[r["id"]]++
	}
	if seen["asia-southeast1"] != 1 {
		t.Errorf("the configured region appears %d times, want exactly 1", seen["asia-southeast1"])
	}
	if _, ok := seen["global"]; !ok {
		t.Error("the global (multi-region) endpoint is missing from the candidates")
	}

	// A configured region outside the static list must still be expressible —
	// otherwise the selector cannot show the operator where their service runs.
	// Switched, rather than re-read from the environment: this is the value the
	// request path will use, so the selector has to follow it instead of
	// GEMINI_VERTEX_REGION, which is only the boot default.
	if err := app.Gemini.SetVertexRegion("me-west1"); err != nil {
		t.Fatal(err)
	}
	result, err = app.vertexRegions(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/vertex-regions", nil))
	if err != nil {
		t.Fatal(err)
	}
	out = result.(map[string]any)
	if out["current"] != "me-west1" {
		t.Errorf("current = %v, want me-west1", out["current"])
	}
	if first := out["regions"].([]map[string]any)[0]; first["id"] != "me-west1" {
		t.Errorf("first region = %v, want the unlisted configured region first", first)
	}
}

// TestVertexRegionsRouteIsNotAnId — the new route is a literal segment at the
// depth where {id} patterns live, which is the shape Go's ServeMux resolves by
// specificity. Mirrors the real table's four shapes (see router_test.go for the
// same property pinned for default-prompt); the real table itself is built by
// TestRouterRegistersWithoutPanic, which fails if two patterns conflict.
func TestVertexRegionsRouteIsNotAnId(t *testing.T) {
	mux := http.NewServeMux()
	var served string
	mark := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { served = name }
	}
	mux.HandleFunc("GET /api/v1/admin/models", mark("list"))
	mux.HandleFunc("GET /api/v1/admin/models/default-prompt", mark("default-prompt"))
	mux.HandleFunc("GET /api/v1/admin/models/vertex-regions", mark("vertex-regions"))
	mux.HandleFunc("GET /api/v1/admin/models/{id}/available", mark("available"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/vertex-regions", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if served != "vertex-regions" {
		t.Fatalf("literal route lost: served=%q", served)
	}
	served = ""
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/7/available", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if served != "available" {
		t.Fatalf("{id} route broken: served=%q", served)
	}
}
