package gemini

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The tests in this file are the migration's red line: they pin the STUDIO
// endpoint strings and the studio credential header to the exact expressions
// the code used before the transport split (apiBase() + "/models/{m}:…",
// x-goog-api-key). If a refactor ever reroutes the default path, these fail
// without a server, a key or an environment being involved.

func TestProviderKindDefaultsToStudio(t *testing.T) {
	for _, v := range []string{"", "   ", "studio", "STUDIO", "ai-studio", "vertext", "vertex-ai", "1"} {
		t.Setenv("GEMINI_PROVIDER", v)
		if got := providerKindFromEnv(); got != providerStudio {
			t.Errorf("GEMINI_PROVIDER=%q resolved to %q, want studio — anything but an explicit vertex must keep today's path", v, got)
		}
	}
	t.Setenv("GEMINI_PROVIDER", "VeRtEx")
	if got := providerKindFromEnv(); got != providerVertex {
		t.Errorf("GEMINI_PROVIDER=VeRtEx should be accepted case-insensitively, got %q", got)
	}
}

// TestStudioEndpointsUnchanged — the six endpoints, spelled out as literals
// rather than re-derived from the code under test.
func TestStudioEndpointsUnchanged(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "")
	t.Setenv("GEMINI_API_BASE", "https://relay.example/v1beta")
	s := New("k", "gemini-flash-lite-latest", 128)

	cases := []struct{ name, got, want string }{
		{"generateContent", s.generateURLFor("gemini-flash-lite-latest"),
			"https://relay.example/v1beta/models/gemini-flash-lite-latest:generateContent"},
		{"generateContent strips models/", s.generateURLFor("models/gemini-flash-lite-latest"),
			"https://relay.example/v1beta/models/gemini-flash-lite-latest:generateContent"},
		{"streamGenerateContent", s.provider.streamURL("m"),
			"https://relay.example/v1beta/models/m:streamGenerateContent?alt=sse"},
		{"embedContent", s.provider.embedURL(EmbeddingModelGE1),
			"https://relay.example/v1beta/models/gemini-embedding-001:embedContent"},
		{"embedContent GE2", s.provider.embedURL(EmbeddingModelGE2),
			"https://relay.example/v1beta/models/gemini-embedding-2:embedContent"},
		{"batchEmbedContents", s.provider.batchEmbedURL(EmbeddingModelGE1),
			"https://relay.example/v1beta/models/gemini-embedding-001:batchEmbedContents"},
		{"cachedContents", s.provider.cachedContentsURL(),
			"https://relay.example/v1beta/cachedContents"},
		{"cachedContent model field", s.provider.cachedContentModel("models/m"),
			"models/m"},
		{"models list", s.provider.listModelsURL(),
			"https://relay.example/v1beta/models?pageSize=200"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	// The base is read per call, not captured at construction: a deploy that
	// points GEMINI_API_BASE elsewhere (a relay in a supported region) must take
	// effect without rebuilding, and the tests above rely on it too.
	t.Setenv("GEMINI_API_BASE", "https://other.example/v1beta")
	if got := s.generateURLFor("m"); got != "https://other.example/v1beta/models/m:generateContent" {
		t.Errorf("GEMINI_API_BASE must be honoured per call, got %q", got)
	}
}

// TestStudioAuthorizeSetsOnlyTheApiKeyHeader — studio must send the key and
// nothing else; a stray Authorization header would be sent to a host that has
// no business seeing it.
func TestStudioAuthorizeSetsOnlyTheApiKeyHeader(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "")
	s := New("studio-key", "m", 128)
	req, err := http.NewRequest(http.MethodPost, "https://example.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.provider.authorize(context.Background(), req, "studio-key"); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-goog-api-key"); got != "studio-key" {
		t.Errorf("x-goog-api-key = %q, want the api key", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("studio must not send Authorization, got %q", got)
	}
}

// TestZeroProviderIsStudio — a Service assembled without New (which is what
// most of this package's tests do) must keep the pre-vertex behaviour.
func TestZeroProviderIsStudio(t *testing.T) {
	t.Setenv("GEMINI_API_BASE", "https://relay.example/v1beta")
	s := &Service{}
	if got := s.generateURLFor("m"); got != "https://relay.example/v1beta/models/m:generateContent" {
		t.Errorf("zero provider generateURL = %q", got)
	}
	if got := s.provider.cachedContentsURL(); got != "https://relay.example/v1beta/cachedContents" {
		t.Errorf("zero provider cachedContents = %q", got)
	}
}

// TestVertexEndpointShapes — the platform's paths, pinned against the values
// measured with cmd/vertexprobe. The path is where the platform is least
// forgiving: a wrong one 404s, and a model passed as a URL 400s "malformed".
func TestVertexEndpointShapes(t *testing.T) {
	// The catalog URL is derived from the region (and the override), not from
	// the provider's own base, so a stray override in the environment would
	// silently rewrite the expectation below.
	t.Setenv("GEMINI_VERTEX_API_BASE", "")
	r := vertexResource{
		base:    "https://asia-southeast1-aiplatform.googleapis.com/v1",
		project: "gen-lang-client-0354228918",
		region:  "asia-southeast1",
	}
	p := provider{kind: providerVertex, vertex: r, tokens: &tokenSource{}}
	models := r.base + "/projects/gen-lang-client-0354228918/locations/asia-southeast1/publishers/google/models"

	cases := []struct{ name, got, want string }{
		{"generateContent", p.generateURL("gemini-3.5-flash"), models + "/gemini-3.5-flash:generateContent"},
		{"generateContent strips models/", p.generateURL("models/gemini-3.5-flash"), models + "/gemini-3.5-flash:generateContent"},
		{"streamGenerateContent", p.streamURL("gemini-3.5-flash"), models + "/gemini-3.5-flash:streamGenerateContent?alt=sse"},
		// :predict, NOT :embedContent — the latter 404s for 001 on the platform
		// (and 002 is the mirror image; see the GE2 case below).
		{"embed predict", p.embedURL(EmbeddingModelGE1), models + "/gemini-embedding-001:predict"},
		{"batch embed predict", p.batchEmbedURL(EmbeddingModelGE1), models + "/gemini-embedding-001:predict"},
		// GE2 is served by the OPPOSITE method on the same host: :embedContent
		// answers 200 and :predict answers 404 (measured 2026-10-09 on the
		// production project, region global). A client that hard-coded one method
		// for both models would fail every retrieval of the other's corpus.
		{"embedContent GE2", p.embedURL(EmbeddingModelGE2), models + "/gemini-embedding-2:embedContent"},
		{"cachedContents", p.cachedContentsURL(),
			r.base + "/projects/gen-lang-client-0354228918/locations/asia-southeast1/cachedContents"},
		// The cachedContents body's model must be a BARE RESOURCE NAME — no
		// host, no /v1: the endpoint URL there is a 400 "malformed".
		{"cachedContent model is a resource name", p.cachedContentModel("models/gemini-3.5-flash"),
			"projects/gen-lang-client-0354228918/locations/asia-southeast1/publishers/google/models/gemini-3.5-flash"},
		{"models list", p.listModelsURL(),
			// The catalog route is v1beta1, NOT v1: `/v1/publishers/google/models`
			// answers 404 on every host while `/v1beta1/...` answers 200 (measured
			// 2026-09-25). It is also host-addressed — no project, no location — so
			// the path is the same string for every region; only the host changes.
			"https://asia-southeast1-aiplatform.googleapis.com/v1beta1/publishers/google/models?pageSize=100"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if strings.Contains(p.cachedContentModel("gemini-3.5-flash"), "https://") {
		t.Error("the cachedContents model field must never be a URL")
	}
}

// TestVertexProviderFromEnv — the region, project and base overrides resolve
// into the same resource paths the endpoint builders above produce.
func TestVertexProviderFromEnv(t *testing.T) {
	saPath, _ := writeTestServiceAccount(t, "https://oauth2.googleapis.com/token", "")
	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", saPath)
	t.Setenv("GEMINI_VERTEX_PROJECT", "proj-1")
	t.Setenv("GEMINI_VERTEX_REGION", "europe-west4")
	t.Setenv("GEMINI_VERTEX_API_BASE", "")

	p, err := providerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := "https://europe-west4-aiplatform.googleapis.com/v1/projects/proj-1/locations/europe-west4/publishers/google/models/gemini-3.5-flash:generateContent"
	if got := p.generateURL("gemini-3.5-flash"); got != want {
		t.Errorf("region-qualified URL = %q, want %q", got, want)
	}
	// The default region is the measured target, not the platform's own default.
	t.Setenv("GEMINI_VERTEX_REGION", "")
	p, err = providerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := p.generateURL("m"); !strings.Contains(got, "asia-southeast1-aiplatform.googleapis.com") {
		t.Errorf("default region URL = %q, want asia-southeast1", got)
	}
	if !p.ready() {
		t.Error("a vertex provider carrying a token source must report ready: an empty api key is a valid vertex configuration")
	}
}

// TestVertexAuthorizeUsesBearerToken — vertex must send the OAuth bearer and
// NOT the studio header, whose presence would mean an AI Studio key was sent
// to the platform.
func TestVertexAuthorizeUsesBearerToken(t *testing.T) {
	srv := newTokenStub(t, "tok-abc")
	saPath, _ := writeTestServiceAccount(t, srv.URL, "p")
	ts, err := newTokenSource(saPath)
	if err != nil {
		t.Fatal(err)
	}
	p := provider{kind: providerVertex, tokens: ts}

	req, err := http.NewRequest(http.MethodPost, "https://example.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.authorize(context.Background(), req, "studio-key-should-be-ignored"); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok-abc" {
		t.Errorf("Authorization = %q, want the bearer token", got)
	}
	if got := req.Header.Get("x-goog-api-key"); got != "" {
		t.Errorf("vertex must not send x-goog-api-key, got %q", got)
	}
}

// TestVertexWithoutCredentialsFailsTheRequest — a vertex provider with no token
// source is the shape a future call site would produce by dropping the field.
// It must fail, not silently fall back to the studio header.
func TestVertexWithoutCredentialsFailsTheRequest(t *testing.T) {
	p := provider{kind: providerVertex}
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/x", nil)
	err := p.authorize(context.Background(), req, "k")
	if err == nil || !strings.Contains(err.Error(), "service account") {
		t.Fatalf("err = %v, want a missing-service-account error", err)
	}
	if req.Header.Get("x-goog-api-key") != "" || req.Header.Get("Authorization") != "" {
		t.Error("a credential-less vertex provider must not attach any credential")
	}
}

// TestVertexHostIsNotInterpolatedForGlobal pins the one location whose host
// breaks the usual shape. `global` is served by the unprefixed
// aiplatform.googleapis.com; `global-aiplatform.googleapis.com` answers a bare
// HTML 404 for every model. cmd/vertexprobe built that host by interpolation and
// so reported "required checks failed" against a region that was serving live
// traffic — which is why the host rule is exported instead of duplicated.
func TestVertexHostIsNotInterpolatedForGlobal(t *testing.T) {
	t.Setenv("GEMINI_VERTEX_API_BASE", "")
	if got, want := VertexHost(vertexGlobalRegion), "https://aiplatform.googleapis.com"; got != want {
		t.Errorf("VertexHost(global) = %q, want %q — the multi-home endpoint has no region prefix", got, want)
	}
	if got, want := VertexHost("asia-southeast1"), "https://asia-southeast1-aiplatform.googleapis.com"; got != want {
		t.Errorf("VertexHost(asia-southeast1) = %q, want %q", got, want)
	}
	// The root both the client and the probe build URLs from.
	if got, want := VertexPlatformBase(vertexGlobalRegion)+"/publishers", "https://aiplatform.googleapis.com/v1/publishers"; got != want {
		t.Errorf("VertexPlatformBase(global) = %q, want %q", got, want)
	}
	// An override still wins for every region, or a relay/stub would only cover
	// the default one and the gate would probe the live platform by accident.
	t.Setenv("GEMINI_VERTEX_API_BASE", "https://stub.test/v1")
	for _, region := range []string{vertexGlobalRegion, "us", "asia-southeast1"} {
		if got := VertexPlatformBase(region); got != "https://stub.test/v1" {
			t.Errorf("VertexPlatformBase(%s) ignored the override: %q", region, got)
		}
	}
}
