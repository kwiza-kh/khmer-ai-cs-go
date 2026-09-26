package gemini

// The region-catalog tests.
//
// They exist because the admin picker's catalog was built on a belief that was
// measurably wrong: that the platform has no publisher-model list route, so a
// hand-curated list had to stand in for it. The route does exist — at v1beta1,
// not at v1 — and the curated list was quietly limiting which models an operator
// could pick. Each test below pins one half of the replacement: the URL that is
// actually sent, what is parsed out of the answer (including the fields regional
// entries genuinely omit), and the two honesty rules that keep the platform's
// list from being mistaken for a callability oracle.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// catalogEnv wires a vertex deployment whose platform root is a stub, and
// returns the stub. GEMINI_VERTEX_API_BASE carries a /v1 segment exactly as the
// production base does, so a version substitution that failed to happen would
// show up in the recorded path instead of hiding inside a test-only base.
func catalogEnv(t *testing.T, reply func(r *http.Request) (int, string)) *platformStub {
	t.Helper()
	tokenSrv := newTokenStub(t, "tok-vertex")
	saPath, _ := writeTestServiceAccount(t, tokenSrv.URL, "proj-1")
	platform := newPlatformStub(t, func(r *http.Request, _ []byte) (int, string) { return reply(r) })

	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", saPath)
	t.Setenv("GEMINI_VERTEX_PROJECT", "proj-1")
	t.Setenv("GEMINI_VERTEX_REGION", "asia-southeast1")
	t.Setenv("GEMINI_VERTEX_API_BASE", platform.URL+"/v1")
	return platform
}

// studioCatalogEnv is the same for the studio transport: the key travels in
// x-goog-api-key and there is no region at all.
func studioCatalogEnv(t *testing.T, reply func(r *http.Request) (int, string)) *platformStub {
	t.Helper()
	platform := newPlatformStub(t, func(r *http.Request, _ []byte) (int, string) { return reply(r) })
	t.Setenv("GEMINI_PROVIDER", "")
	t.Setenv("GEMINI_API_BASE", platform.URL)
	t.Setenv("GEMINI_VERTEX_API_BASE", "")
	return platform
}

// TestPublisherModelsURLIsV1Beta1OnEveryHost — the URL shape, spelled out as
// literals.
//
// Every part of it was measured on 2026-09-25 and every part has been wrong at
// least once in this repo: /v1 is a 404 for this route (the reason the curated
// list existed), `global` is a real host but `global-…` is not (it does not
// resolve), and the path carries no project or location because publisher
// models are addressed per HOST.
func TestPublisherModelsURLIsV1Beta1OnEveryHost(t *testing.T) {
	t.Setenv("GEMINI_VERTEX_API_BASE", "")

	cases := []struct{ region, token, want string }{
		{"asia-southeast1", "",
			"https://asia-southeast1-aiplatform.googleapis.com/v1beta1/publishers/google/models?pageSize=100"},
		{"global", "",
			"https://aiplatform.googleapis.com/v1beta1/publishers/google/models?pageSize=100"},
		{"us-central1", "PAGE2",
			"https://us-central1-aiplatform.googleapis.com/v1beta1/publishers/google/models?pageSize=100&pageToken=PAGE2"},
	}
	for _, tc := range cases {
		if got := publisherModelsURL(tc.region, tc.token); got != tc.want {
			t.Errorf("publisherModelsURL(%q) = %q, want %q", tc.region, got, tc.want)
		}
	}
	// The v1 form is the exact URL that answered 404 and produced a 502 in the
	// console: it must not be reachable from here by accident.
	if got := publisherModelsURL("asia-southeast1", ""); strings.Contains(got, "/v1/") {
		t.Errorf("URL = %q, want the v1beta1 catalog route, never the v1 one that 404s", got)
	}
	// The global host must not be interpolated like a datacentre: `global-…`
	// does not resolve.
	if got := publisherModelsURL("global", ""); strings.Contains(got, "global-aiplatform") {
		t.Errorf("URL = %q — the global host carries no region prefix", got)
	}

	// A relay/stub override is re-qualified, not appended to: the deployment's
	// override carries /v1, and the catalog must still ask for v1beta1.
	t.Setenv("GEMINI_VERTEX_API_BASE", "https://relay.example/v1")
	if got, want := publisherModelsURL("europe-west4", ""),
		"https://relay.example/v1beta1/publishers/google/models?pageSize=100"; got != want {
		t.Errorf("override URL = %q, want %q", got, want)
	}
}

// TestValidVertexRegion — the region reaches the request HOST, and that request
// carries a service-account bearer token, so this predicate is the only thing
// standing between an authenticated request and an attacker-chosen hostname.
func TestValidVertexRegion(t *testing.T) {
	for _, ok := range []string{"global", "asia-southeast1", "us-central1", "me-central1", "europe-west4"} {
		if !ValidVertexRegion(ok) {
			t.Errorf("ValidVertexRegion(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"", "ASIA-SOUTHEAST1", // the handler lowercases; this function accepts what it is given
		"evil.com", "asia/southeast1", "../v1", "asia_southeast1", "asia southeast1",
		"-asia", "asia-", "asia--southeast1", strings.Repeat("a", 41),
	} {
		if ValidVertexRegion(bad) {
			t.Errorf("ValidVertexRegion(%q) = true, want false — it must not be able to steer the host", bad)
		}
	}
}

// TestPublisherCatalogParsesTheRegionalEnvelope — the fields regional entries
// really carry, including the two they sometimes OMIT.
//
// launchStage and supportedActions are both absent from some measured entries.
// A catalog that required either would drop those models entirely (they are
// real, pickable models), so the assertions below are as much about what happens
// when the platform says nothing as about what happens when it speaks.
// The supportedActions shapes below are copied from the live platform, not
// invented: an OBJECT with a nested references map, an EMPTY object, and
// absent. A fixture that used a JSON array here is what let a `[]string` field
// reach production, where one such entry failed the whole page with
// "cannot unmarshal object into Go struct field … supportedActions of type
// []string" and silently degraded every region's listing to the fallback —
// which reads exactly like the platform being down. Keep all three shapes.
func TestPublisherCatalogParsesTheRegionalEnvelope(t *testing.T) {
	platform := catalogEnv(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"publisherModels":[` +
			`{"name":"publishers/google/models/gemini-2.5-flash","launchStage":"GA","supportedActions":{"openNotebook":{"references":{"us-central1":{"uri":"https://colab.research.google.com/x"}}}},"versionId":"3"},` +
			`{"name":"publishers/google/models/text-embedding-005","launchStage":"GA","supportedActions":{}},` +
			`{"name":"publishers/google/models/gemini-2.5-flash-preview-native-audio"}]}`
	})

	models, warning, err := ModelCatalog(context.Background(), "", "", "gemini-3.5-flash")
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none for a successful listing", warning)
	}

	got := platform.last(t)
	// The version and the path are the bug being fixed: v1 404s.
	if want := "/v1beta1/publishers/google/models"; got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	if want := "pageSize=100"; got.Query != want {
		t.Errorf("query = %q, want %q", got.Query, want)
	}
	if got.Auth != "Bearer tok-vertex" {
		t.Errorf("Authorization = %q, want the minted service-account token", got.Auth)
	}
	if got.APIKey != "" {
		t.Errorf("x-goog-api-key = %q, want no studio key on the platform path", got.APIKey)
	}

	// The configured model is absent from the list above — the asia-southeast1
	// trap — and must still lead the catalog, marked available.
	want := []struct{ name, launchStage, capability string }{
		{"gemini-3.5-flash", "", CapabilityChat},
		{"gemini-2.5-flash", "GA", CapabilityChat},
		{"text-embedding-005", "GA", CapabilityEmbedding},
		{"gemini-2.5-flash-preview-native-audio", "", CapabilityLive},
	}
	if len(models) != len(want) {
		t.Fatalf("catalog = %v, want %d entries", models, len(want))
	}
	for i, w := range want {
		m := models[i]
		if m.Name != w.name || m.LaunchStage != w.launchStage || m.Capability != w.capability {
			t.Errorf("entry %d = %+v, want name=%q launch_stage=%q capability=%q",
				i, m, w.name, w.launchStage, w.capability)
		}
		if !m.Available {
			t.Errorf("entry %d (%s) unavailable, want available — the region listed it", i, m.Name)
		}
		if m.DisplayName == "" {
			t.Errorf("entry %d (%s) has no display name", i, m.Name)
		}
	}
	// The live entry's classification came from its NAME: it carried no
	// supportedActions at all, which is the shape every measured production entry
	// has. An absent launchStage must render as "unknown", never as a value
	// someone could mistake for a real stage.
	if models[3].LaunchStage != "" {
		t.Errorf("launch_stage = %q for an entry that carried none, want \"\"", models[3].LaunchStage)
	}
}

// TestPublisherCatalogPagesThroughNextPageToken — us-central1 answers 133 models
// with a nextPageToken. Stopping after one page would present a truncated
// catalog as the complete one, which is precisely the "the list limits what an
// operator can pick" failure this work removes.
func TestPublisherCatalogPagesThroughNextPageToken(t *testing.T) {
	platform := catalogEnv(t, func(r *http.Request) (int, string) {
		if r.URL.Query().Get("pageToken") == "" {
			return http.StatusOK, `{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash","launchStage":"GA"}],"nextPageToken":"P2"}`
		}
		return http.StatusOK, `{"publisherModels":[{"name":"publishers/google/models/gemini-3.7-flash","launchStage":"GA"}]}`
	})

	models, warning, err := ModelCatalog(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
	names := catalogNames(models)
	if strings.Join(names, ",") != "gemini-3.8-flash,gemini-3.7-flash" {
		t.Fatalf("names = %v, want both pages in order", names)
	}
	reqs := platform.recorded()
	if len(reqs) != 2 {
		t.Fatalf("%d request(s), want 2 (the token must be followed)", len(reqs))
	}
	if reqs[1].Query != "pageSize=100&pageToken=P2" {
		t.Errorf("second request query = %q, want the token carried through", reqs[1].Query)
	}
}

// TestPublisherCatalogStopsAtThePageCap — a platform that keeps handing back a
// nextPageToken must not turn one console page load into an unbounded walk. The
// cap is reported, because a silently truncated catalog is the failure mode this
// whole change exists to end.
func TestPublisherCatalogStopsAtThePageCap(t *testing.T) {
	// A distinct name per request: the catalog de-duplicates by name, and a stub
	// that repeats one would exercise the de-duplication instead of the cap.
	var page atomic.Int32
	platform := catalogEnv(t, func(r *http.Request) (int, string) {
		n := page.Add(1)
		return http.StatusOK, `{"publisherModels":[{"name":"publishers/google/models/gemini-page-` +
			strconv.Itoa(int(n)) + `"}],"nextPageToken":"next"}`
	})

	models, warning, err := ModelCatalog(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if got := len(platform.recorded()); got != vertexListMaxPages {
		t.Errorf("%d request(s), want the cap of %d", got, vertexListMaxPages)
	}
	if len(models) != vertexListMaxPages {
		t.Errorf("catalog has %d entries, want %d (one per page)", len(models), vertexListMaxPages)
	}
	if !strings.Contains(warning, "truncated") || !strings.Contains(warning, "asia-southeast1") {
		t.Errorf("warning = %q, want it to say the list was truncated and where", warning)
	}
}

// TestModelCatalogFallsBackWithoutAnError — a region whose listing 404s (or
// 500s, or answers nothing) must NOT become a 500 in the console: the operator
// gets the configured model, the known-good names, and a reason.
//
// Every entry stays available. A failed listing is evidence about the listing,
// not about any model — the measured production case below is a model answering
// 200 from a region whose list never mentions it — so "unavailable" is not a
// conclusion this code is entitled to draw.
func TestModelCatalogFallsBackWithoutAnError(t *testing.T) {
	platform := catalogEnv(t, func(r *http.Request) (int, string) {
		return http.StatusNotFound, `{"error":{"message":"Requested entity was not found."}}`
	})

	models, warning, err := ModelCatalog(context.Background(), "", "us-central1", "gemini-3.5-flash")
	if err != nil {
		t.Fatalf("a failed listing must not be an error: %v", err)
	}
	if len(platform.recorded()) != 1 {
		t.Errorf("%d request(s), want the one that failed", len(platform.recorded()))
	}
	// The warning has to name the region and carry the platform's own sentence:
	// "the model list failed" is not actionable, "region us-central1: … 404 …"
	// is. The region is also how the caller tells a real region's answer from a
	// fallback.
	if !strings.Contains(warning, "us-central1") || !strings.Contains(warning, "404") {
		t.Errorf("warning = %q, want the region and the HTTP status", warning)
	}
	names := catalogNames(models)
	if names[0] != "gemini-3.5-flash" {
		t.Fatalf("names = %v, want the configured model first", names)
	}
	// The fallback list is what keeps a broken region from reading as "Gemini is
	// broken", so it must actually be there — and must not be dressed up as
	// verified either: the warning above is what says so.
	if !catalogHas(models, "gemini-2.5-flash") || !catalogHas(models, "gemini-embedding-001") {
		t.Errorf("names = %v, want the last-resort names present", names)
	}
	for _, m := range models {
		if !m.Available {
			t.Errorf("%s marked unavailable, but a failed listing is not evidence about the model", m.Name)
		}
	}

	// A region that answers 200 with nothing in it is the same story.
	empty := catalogEnv(t, func(r *http.Request) (int, string) { return http.StatusOK, `{"publisherModels":[]}` })
	models, warning, err = ModelCatalog(context.Background(), "", "", "gemini-3.5-flash")
	if err != nil {
		t.Fatalf("an empty listing must not be an error: %v", err)
	}
	if len(empty.recorded()) != 1 {
		t.Errorf("%d request(s) for the empty listing, want 1", len(empty.recorded()))
	}
	if warning == "" {
		t.Error("an empty listing must say so — an empty dropdown with no reason is the bug")
	}
	if got := catalogNames(models); got[0] != "gemini-3.5-flash" || len(got) < 2 {
		t.Errorf("names = %v, want the configured model plus the fallback", got)
	}
}

// TestModelCatalogUnionRuleIsPerRegion — the union keeps the serving model
// visible no matter which region was asked about, and `available` is never
// derived from whether that region's list happened to mention it.
func TestModelCatalogUnionRuleIsPerRegion(t *testing.T) {
	catalogEnv(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash","launchStage":"GA"},` +
			`{"name":"publishers/google/models/gemini-3.5-flash","launchStage":"GA"}]}`
	})

	// (a) The model IS in the list — one entry, at the front, no duplicate.
	models, _, err := ModelCatalog(context.Background(), "", "", "gemini-3.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	names := catalogNames(models)
	if strings.Join(names, ",") != "gemini-3.5-flash,gemini-3.8-flash" {
		t.Fatalf("names = %v, want the configured model moved to the front exactly once", names)
	}
	if !models[0].Available {
		t.Error("a model the region itself listed must be available")
	}

	// (b) A foreign region that does NOT list it: still offered first, and still
	// available — being absent from a list is not evidence (see
	// TestModelCatalogKeepsTheServingModelAvailableWithoutAListingEntry). A
	// fresh stub, because the region is invisible to it: the only way to model
	// "this region's list does not have the model" is a list without it.
	catalogEnv(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash","launchStage":"GA"}]}`
	})
	models, _, err = ModelCatalog(context.Background(), "", "us-central1", "gemini-3.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if models[0].Name != "gemini-3.5-flash" {
		t.Fatalf("names = %v, want the configured model first", catalogNames(models))
	}
	if !models[0].Available {
		t.Error("absence from a regional list must not mark a model unavailable — that is the exact production case that would hide the serving model")
	}
	if !models[1].Available {
		t.Errorf("%s was listed by the region, so it is available", models[1].Name)
	}
}

// TestModelCatalogKeepsTheServingModelAvailableWithoutAListingEntry — THE
// regression guard for the whole availability rule.
//
// The list below is asia-southeast1's real catalogue, measured on the production
// service account: nine entries and exactly ONE Gemini chat model, with
// gemini-3.5-flash — the model this deployment actually serves customers with —
// nowhere in it. gemini-3.5-flash answers 200 OK in that region; it is simply
// not listed.
//
// So an `available` computed as "is it in the list" would render production's
// own serving model as unusable in production's own region. This test fails the
// moment anyone reintroduces that inference.
func TestModelCatalogKeepsTheServingModelAvailableWithoutAListingEntry(t *testing.T) {
	const asiaSoutheast1List = `{"publisherModels":[` +
		`{"name":"publishers/google/models/gemini-2.5-flash","launchStage":"GA"},` +
		`{"name":"publishers/google/models/gemma3","launchStage":"GA"},` +
		`{"name":"publishers/google/models/gemma4","launchStage":"GA"},` +
		`{"name":"publishers/google/models/image-segmentation-001","launchStage":"GA"},` +
		`{"name":"publishers/google/models/imagetext","launchStage":"GA"},` +
		`{"name":"publishers/google/models/multimodalembedding","launchStage":"GA"},` +
		`{"name":"publishers/google/models/text-embedding-005","launchStage":"GA"},` +
		`{"name":"publishers/google/models/text-multilingual-embedding-002","launchStage":"GA"},` +
		`{"name":"publishers/google/models/textembedding-gecko","launchStage":"GA"}]}`

	platform := catalogEnv(t, func(r *http.Request) (int, string) { return http.StatusOK, asiaSoutheast1List })
	models, warning, err := ModelCatalog(context.Background(), "", "asia-southeast1", "gemini-3.5-flash")
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none — the listing succeeded", warning)
	}
	if got := platform.last(t).Path; got != "/v1beta1/publishers/google/models" {
		t.Errorf("path = %q, want the v1beta1 catalog route", got)
	}

	names := catalogNames(models)
	// The region's nine entries, none dropped, plus the serving model unioned in
	// and placed first.
	if len(names) != 10 {
		t.Fatalf("catalog = %v, want the 9 listed models plus the serving one", names)
	}
	if names[0] != "gemini-3.5-flash" {
		t.Fatalf("names = %v, want the serving model first", names)
	}

	// The guard itself: nothing in this catalog may be marked unavailable.
	for _, m := range models {
		if !m.Available {
			t.Errorf("%s is marked unavailable — the platform's list is not a callability oracle, "+
				"and gemini-3.5-flash answers 200 in this exact region while being absent from this exact list", m.Name)
		}
	}

	// The classifications the console's filters depend on, for the families that
	// really appear in this region.
	wantCapability := map[string]string{
		"gemini-3.5-flash":                CapabilityChat,
		"gemini-2.5-flash":                CapabilityChat,
		"gemma3":                          CapabilityChat,
		"gemma4":                          CapabilityChat,
		"image-segmentation-001":          CapabilityImage,
		"imagetext":                       CapabilityImage,
		"multimodalembedding":             CapabilityEmbedding,
		"text-embedding-005":              CapabilityEmbedding,
		"text-multilingual-embedding-002": CapabilityEmbedding,
		"textembedding-gecko":             CapabilityEmbedding,
	}
	for _, m := range models {
		if want, ok := wantCapability[m.Name]; ok && m.Capability != want {
			t.Errorf("capability for %s = %q, want %q", m.Name, m.Capability, want)
		}
		if m.Name != "gemini-3.5-flash" {
			// launchStage is reliably present on the platform's entries, so the
			// console must receive it rather than a synthesised default.
			if m.LaunchStage != "GA" {
				t.Errorf("launch_stage for %s = %q, want the platform's own GA", m.Name, m.LaunchStage)
			}
			if m.DisplayName == "" {
				t.Errorf("%s has no display name", m.Name)
			}
		}
	}
	// The unioned entry has no platform metadata to report, and must not invent
	// any: "" is the console's "unknown".
	if models[0].LaunchStage != "" {
		t.Errorf("launch_stage = %q for the unioned model, want the empty unknown value — nothing measured it", models[0].LaunchStage)
	}
}

// TestModelCatalogStudioIsUnchangedApartFromAvailabilityMetadata — the studio
// transport keeps its old request exactly (no region, pageSize=200, the key in
// the header) and gains only the catalog's metadata.
//
// It also pins a deliberate behaviour change: the old studio list dropped every
// non-Gemini entry, which is why an embedding model could never be picked. The
// capability vocabulary has a value for embeddings, so they are now listed.
func TestModelCatalogStudioIsUnchangedApartFromAvailabilityMetadata(t *testing.T) {
	platform := studioCatalogEnv(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"models":[` +
			`{"name":"models/gemini-3.5-flash","displayName":"Gemini 3.5 Flash"},` +
			`{"name":"models/text-embedding-005","displayName":"Embeddings 005"}]}`
	})

	models, warning, err := ModelCatalog(context.Background(), "AIza-studio-key", "europe-west4", "")
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none on the studio path", warning)
	}
	got := platform.last(t)
	if want := "/models?pageSize=200"; got.Path+"?"+got.Query != want {
		t.Errorf("studio URI = %q, want %q — the region must not reach the studio URL", got.Path+"?"+got.Query, want)
	}
	if got.APIKey != "AIza-studio-key" {
		t.Errorf("x-goog-api-key = %q, want the studio key", got.APIKey)
	}
	if got.Auth != "" {
		t.Errorf("Authorization = %q, want none on the studio path", got.Auth)
	}
	if names := catalogNames(models); strings.Join(names, ",") != "gemini-3.5-flash,text-embedding-005" {
		t.Fatalf("names = %v, want both entries (embeddings included)", names)
	}
	if models[1].Capability != CapabilityEmbedding {
		t.Errorf("capability = %q for an embedding model, want %q", models[1].Capability, CapabilityEmbedding)
	}
	// The platform's own label wins over the derived one when it exists.
	if models[0].DisplayName != "Gemini 3.5 Flash" {
		t.Errorf("display_name = %q, want the platform's own label", models[0].DisplayName)
	}
}

// TestDisplayNameDerivation — the labels an operator reads. The platform sends
// no displayName at all on the vertex list, so these are the labels that ship.
func TestDisplayNameDerivation(t *testing.T) {
	cases := map[string]string{
		"gemini-3.8-flash":              "Gemini 3.8 Flash",
		"gemini-3.5-flash":              "Gemini 3.5 Flash",
		"gemini-2.5-flash-lite":         "Gemini 2.5 Flash Lite",
		"text-embedding-005":            "Text Embedding 005",
		"gemini-embedding-001":          "Gemini Embedding 001",
		"gemini-2.5-flash-preview-tts":  "Gemini 2.5 Flash Preview TTS",
		"imagen-3.0-generate-002":       "Imagen 3.0 Generate 002",
		"gemini-2.0-flash-live-001":     "Gemini 2.0 Flash Live 001",
		"text-multilingual-embedding-2": "Text Multilingual Embedding 2",
	}
	for in, want := range cases {
		if got := displayNameFor(in); got != want {
			t.Errorf("displayNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCapabilityClassification — the console groups and filters on these
// strings, so a chat model classified as `other` (or worse, an image or speech
// model classified as `chat`) is a product bug, not a cosmetic one.
//
// Name-only, and pure: a measurement against the production service account
// found `supportedActions` absent on every entry it checked, so no case here
// depends on it. The trailing cases are the real families that
// asia-southeast1's list actually contains.
func TestCapabilityClassification(t *testing.T) {
	cases := []struct{ name, want string }{
		{"gemini-3.5-flash", CapabilityChat},
		{"gemini-3.8-flash", CapabilityChat},
		{"gemini-2.5-pro", CapabilityChat},
		{"gemini-2.5-flash-lite", CapabilityChat},
		{"text-bison-002", CapabilityChat},
		{"gemma-3-27b-it", CapabilityChat},
		{"gemma3", CapabilityChat},
		{"gemma4", CapabilityChat},
		{"gemini-embedding-001", CapabilityEmbedding},
		{"text-embedding-005", CapabilityEmbedding},
		{"text-multilingual-embedding-002", CapabilityEmbedding},
		{"multimodalembedding", CapabilityEmbedding},
		{"textembedding-gecko", CapabilityEmbedding},
		{"imagen-3.0-generate-002", CapabilityImage},
		{"gemini-2.5-flash-image", CapabilityImage},
		{"imagegeneration@006", CapabilityImage},
		{"image-segmentation-001", CapabilityImage},
		{"imagetext", CapabilityImage},
		{"gemini-2.5-flash-preview-tts", CapabilityTTS},
		{"gemini-2.0-flash-live-001", CapabilityLive},
		{"gemini-2.5-flash-preview-native-audio", CapabilityLive},
		{"gemini-live-2.5-flash-preview", CapabilityLive},
		{"veo-3.0-generate-preview", CapabilityOther},
		{"chirp-3-hd", CapabilityOther},
		{"", CapabilityOther},
	}
	for _, tc := range cases {
		if got := capabilityFor(tc.name); got != tc.want {
			t.Errorf("capabilityFor(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestVertexRegionsPutsTheConfiguredRegionFirst — the selector must express the
// region the service actually runs in, even when that region is not in the
// static candidate list (there is no "list all regions" API to fall back on).
func TestVertexRegionsPutsTheConfiguredRegionFirst(t *testing.T) {
	t.Setenv("GEMINI_VERTEX_REGION", "asia-southeast1")
	regions, current := VertexRegions()
	if current != "asia-southeast1" {
		t.Fatalf("current = %q, want the configured region", current)
	}
	if regions[0].ID != "asia-southeast1" {
		t.Fatalf("first region = %q, want the configured one", regions[0].ID)
	}
	if regions[0].Label != "asia-southeast1 (Singapore)" {
		t.Errorf("label = %q, want the location's human name", regions[0].Label)
	}
	seen := 0
	for _, r := range regions {
		if r.ID == "asia-southeast1" {
			seen++
		}
		if r.ID == "" || r.Label == "" {
			t.Errorf("region %+v is missing an id or a label", r)
		}
	}
	if seen != 1 {
		t.Errorf("the configured region appears %d times, want exactly 1", seen)
	}
	// The default region is the measured target, so an unset variable still
	// resolves to the region production serves from.
	t.Setenv("GEMINI_VERTEX_REGION", "")
	if regions, current := VertexRegions(); current != vertexDefaultRegion || regions[0].ID != vertexDefaultRegion {
		t.Errorf("unset region resolved to current=%q first=%q, want %q", current, regions[0].ID, vertexDefaultRegion)
	}
	// A region the static list has never heard of is still first and still
	// labelled — the operator's own deployment is not an edge case.
	t.Setenv("GEMINI_VERTEX_REGION", "me-west1")
	regions, current = VertexRegions()
	if current != "me-west1" || regions[0].ID != "me-west1" {
		t.Fatalf("current = %q first = %q, want me-west1", current, regions[0].ID)
	}
	if regions[0].Label != "me-west1 (configured)" {
		t.Errorf("label = %q, want a label that says where it came from", regions[0].Label)
	}
}

// TestModelCatalogRegionReachesTheRequestWarning — with a stub override in place
// the region cannot appear in the HOST, so the warning is where the requested
// region is observable end to end: an omitted region must resolve to the
// configured one, and an explicit one must be used as given.
func TestModelCatalogRegionReachesTheRequestWarning(t *testing.T) {
	catalogEnv(t, func(r *http.Request) (int, string) {
		return http.StatusNotFound, `{"error":{"message":"not found"}}`
	})

	_, warning, err := ModelCatalog(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "region asia-southeast1") {
		t.Errorf("omitted region warned %q, want it to resolve to the configured asia-southeast1", warning)
	}

	_, warning, err = ModelCatalog(context.Background(), "", "EUROPE-WEST4", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "region europe-west4") {
		t.Errorf("warning = %q, want the explicitly requested region, lowercased", warning)
	}
}

// catalogNames is the names of a catalog, for the assertions that care about
// order rather than about metadata.
func catalogNames(models []CatalogModel) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.Name)
	}
	return out
}

// TestCatalogEntryShapeIsTheFrozenWireContract — the catalog is serialised by
// internal/api into the console's field names; this pins the Go-side shape the
// handler reads, so a renamed field fails here rather than in the browser.
func TestCatalogEntryShapeIsTheFrozenWireContract(t *testing.T) {
	raw, err := json.Marshal(CatalogModel{
		Name: "gemini-3.5-flash", DisplayName: "Gemini 3.5 Flash",
		LaunchStage: "GA", Capability: CapabilityChat, Available: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Name", "DisplayName", "LaunchStage", "Capability", "Available"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("CatalogModel lost the %s field the handler maps to the wire", key)
		}
	}
}

// TestModelCatalogRefusesADeploymentWithoutAServiceAccount — the one failure
// that stays hard. A 200 with a fallback list here would paint a broken
// deployment as a healthy one with an empty picker.
func TestModelCatalogRefusesADeploymentWithoutAServiceAccount(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", "")
	if _, _, err := ModelCatalog(context.Background(), "", "", ""); err == nil ||
		!strings.Contains(err.Error(), "GEMINI_VERTEX_SA_FILE") {
		t.Fatalf("err = %v, want the configuration error naming the variable to fix", err)
	}
}

// TestCatalogDoesNotLeakTheStudioKeyOntoThePlatformPath — the red line of the
// provider split, asserted through the new catalog code path rather than only
// through the serving paths.
func TestCatalogDoesNotLeakTheStudioKeyOntoThePlatformPath(t *testing.T) {
	platform := catalogEnv(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"publisherModels":[{"name":"publishers/google/models/gemini-3.5-flash"}]}`
	})
	if _, _, err := ModelCatalog(context.Background(), "AIza-studio-key-from-db", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, req := range platform.recorded() {
		if req.APIKey != "" {
			t.Fatalf("x-goog-api-key = %q reached the platform endpoint", req.APIKey)
		}
		if req.Auth != "Bearer tok-vertex" {
			t.Errorf("Authorization = %q, want the service-account bearer", req.Auth)
		}
	}
}

// TestStudioCatalogFailureIsStillAnError — studio keeps the behaviour it had
// before this change: no fallback list, no silent empty dropdown. (The console
// reports it as a 502 with the cause.)
func TestStudioCatalogFailureIsStillAnError(t *testing.T) {
	studioCatalogEnv(t, func(r *http.Request) (int, string) { return http.StatusForbidden, `{"error":"bad key"}` })
	if _, _, err := ModelCatalog(context.Background(), "AIza-bad", "", ""); err == nil {
		t.Fatal("a studio listing failure must stay an error")
	}
}

// catalogHas reports whether one catalog carries a model id — a test-only
// helper: nothing in the package needs to search a catalog it just built.
func catalogHas(models []CatalogModel, model string) bool {
	for _, m := range models {
		if m.Name == model {
			return true
		}
	}
	return false
}

// recorded is a copy of every request the stub saw — for the assertions that
// need more than the last one (paging).
func (s *platformStub) recorded() []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubRequest(nil), s.requests...)
}

// TestProbeModelOnlyVetoesOnNotFound pins the single property that makes the
// admin save guard safe: it may refuse a save ONLY when the platform itself said
// the model is not there.
//
// This is the difference between a guard and an outage. Treating any non-200 as
// "unusable" would refuse to save a perfectly good model during a quota spike
// (429), behind a permission gap (403) or through a blip (503) — and because the
// guard runs on the one write that decides which model answers customers, a
// false refusal is an operational dead end while a false acceptance is merely a
// failed test. So the mapping is deliberately asymmetric.
func TestProbeModelOnlyVetoesOnNotFound(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want ProbeOutcome
	}{
		{"accepted", http.StatusOK, `{"candidates":[]}`, ProbeServed},
		{"quota is not absence", http.StatusTooManyRequests,
			`{"error":{"status":"RESOURCE_EXHAUSTED"}}`, ProbeIndeterminate},
		{"permission is not absence", http.StatusForbidden,
			`{"error":{"status":"PERMISSION_DENIED"}}`, ProbeIndeterminate},
		{"server fault is not absence", http.StatusServiceUnavailable,
			`{"error":{"status":"UNAVAILABLE"}}`, ProbeIndeterminate},
		{"bad request is not absence", http.StatusBadRequest,
			`{"error":{"status":"INVALID_ARGUMENT"}}`, ProbeIndeterminate},
		{"NOT_FOUND is the one veto", http.StatusNotFound,
			`{"error":{"status":"NOT_FOUND","message":"Publisher model ... was not found"}}`, ProbeNotServed},
		// A 404 with a non-NOT_FOUND status tells us about the ROUTE, not the
		// model, so it must not block a save either.
		{"404 without NOT_FOUND says nothing", http.StatusNotFound,
			`{"error":{"status":"PERMISSION_DENIED"}}`, ProbeIndeterminate},
		{"unparsable 404 says nothing", http.StatusNotFound,
			`<html>gateway</html>`, ProbeIndeterminate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			platform := catalogEnv(t, func(r *http.Request) (int, string) { return tc.code, tc.body })
			if got := ProbeModel(context.Background(), "asia-southeast1", "gemini-3.8-flash"); got != tc.want {
				t.Errorf("outcome = %q, want %q", got, tc.want)
			}
			// The probe must address the region and model it was asked about, or
			// the guard would be answering a different question than the caller's.
			last := platform.last(t)
			if !strings.Contains(last.Path, "/locations/asia-southeast1/") ||
				!strings.Contains(last.Path, "gemini-3.8-flash:generateContent") {
				t.Errorf("path = %q, want the region and model under test", last.Path)
			}
		})
	}
}

// TestProbeModelRefusesToGuessWithoutAVertexDeployment — on the studio transport
// there is one endpoint and one catalog, so a region-shaped question has no
// answer and the guard must stay out of the way.
func TestProbeModelRefusesToGuessWithoutAVertexDeployment(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "studio")
	if got := ProbeModel(context.Background(), "asia-southeast1", "gemini-3.5-flash"); got != ProbeIndeterminate {
		t.Errorf("outcome = %q, want %q on the studio transport", got, ProbeIndeterminate)
	}
}

// TestProbeModelRejectsAHostSteeringRegion — the region reaches the request HOST,
// so an unvalidated value would aim an authenticated request at a caller-chosen
// host. The probe must refuse to build that request at all.
func TestProbeModelRejectsAHostSteeringRegion(t *testing.T) {
	platform := catalogEnv(t, func(r *http.Request) (int, string) { return http.StatusOK, `{}` })
	for _, region := range []string{"evil.example/x", "as ia", "../etc", "asia-southeast1.evil.com"} {
		if got := ProbeModel(context.Background(), region, "gemini-3.5-flash"); got != ProbeIndeterminate {
			t.Errorf("region %q: outcome = %q, want %q (must not build a request)", region, got, ProbeIndeterminate)
		}
	}
	if n := platform.count(); n != 0 {
		t.Errorf("requests = %d, want 0 — a rejected region must not reach the network", n)
	}
}

// TestProbeModelAcceptsAMixedCaseRegion — GEMINI_VERTEX_REGION is hand-written in
// a shell env file, so "Asia-Southeast1" can reach the probe. Rejecting it would
// make the save guard silently stop guarding (indeterminate = allow), which is
// precisely the failure mode it exists to prevent.
func TestProbeModelAcceptsAMixedCaseRegion(t *testing.T) {
	catalogEnv(t, func(r *http.Request) (int, string) { return http.StatusOK, `{}` })
	if got := ProbeModel(context.Background(), "Asia-Southeast1", "gemini-3.5-flash"); got != ProbeServed {
		t.Errorf("outcome = %q, want %q — a mixed-case region must be normalised, not rejected", got, ProbeServed)
	}
}
