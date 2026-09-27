package gemini

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Transport selection: AI Studio (today's production) or Vertex AI — the
// Gemini Enterprise Agent Platform.
//
// The two transports disagree on all three layers of a request:
//
//	          studio (today)                        vertex
//	host      generativelanguage.googleapis.com    {region}-aiplatform.googleapis.com
//	path      /v1beta/models/{m}:generateContent   /v1/projects/{p}/locations/{l}/publishers/google/models/{m}:…
//	auth      x-goog-api-key: <key>                Authorization: Bearer <oauth token>
//	embed     :embedContent                        :predict (+ outputDimensionality in `parameters`)
//	cache     model = "models/{m}"                 model = the full resource name
//
// So the choice is made once, here, and every endpoint + credential decision
// goes through the provider value. Before this file those decisions were spread
// over six hand-formatted `apiBase() + "/models/…"` strings and three
// `x-goog-api-key` header writes; a fourth call site added later could ship an
// AI Studio key to a Vertex endpoint, which fails as a 401 that names neither
// the key nor the endpoint it was sent to.
//
// The default is deliberately the OLD path: an unset, misspelled or unrecognised
// GEMINI_PROVIDER resolves to studio, so this increment cannot change production
// behaviour by itself, and the studio branch of every method below is the
// verbatim expression it replaced.
type providerKind string

const (
	providerStudio providerKind = "studio"
	providerVertex providerKind = "vertex"
)

// vertexDefaultRegion — the fallback for a deployment that never says where it
// lives. It is NOT a statement about where production is: the live box pins
// GEMINI_VERTEX_REGION=global (measured 2026-09-26: the serving pair
// `gemini-3.8-flash` + `gemini-3.5-flash-lite` answers 404 in
// asia-southeast1 and 200 in global, so the region is now load-bearing).
// The default stays a single region on purpose — a wrong region is a 404 on
// every call, and a *silent* default is what makes that visible instead of
// quietly routing an unset deployment to a multi-home endpoint nobody audited.
const vertexDefaultRegion = "asia-southeast1"

// vertexGlobalRegion is the one Vertex location that is not a datacentre: the
// multi-region endpoint. Its host carries NO region prefix (measured
// 2026-09-25: aiplatform.googleapis.com serves the publisher-model list,
// `global-aiplatform.googleapis.com` does not resolve at all), so it is spelled
// out instead of being interpolated like every other location.
const vertexGlobalRegion = "global"

// vertexListAPIVersion is the version of the publisher-model LIST route.
//
// The serving routes are v1; this one is NOT. `/v1/publishers/google/models`
// answers 404 (measured 2026-09-25) on both the global and the regional host,
// while `/v1beta1/...` answers 200 — which is the entire reason this package
// once carried a hand-curated picker list. A version is a property of the
// route, not of the platform, so it is a named constant rather than a literal
// buried in a format string.
const vertexListAPIVersion = "v1beta1"

// vertexListPageSize is the page size of the publisher-model list: 100 is what
// the platform was measured to accept (us-central1 has 133 entries and answers
// a nextPageToken, so the catalog has to page).
const vertexListPageSize = 100

// vertexListMaxPages bounds the paging loop. The largest region measured 133
// models (two pages); the cap exists so a platform that keeps handing back a
// nextPageToken cannot turn one admin page load into an unbounded walk. Hitting
// it is reported to the caller as a truncation, never as a complete list.
const vertexListMaxPages = 5

// cloudPlatformScope is the only scope this service needs: every Gemini call on
// the platform is authorised by cloud-platform, and asking for less (or for
// generativelanguage.googleapis.com specifically) produces a token that the
// inference endpoint rejects.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// provider resolves endpoints and credentials for one deployment.
//
// The ZERO VALUE is a working studio provider whose base is read per call from
// GEMINI_API_BASE — that is what `&Service{}` means in this package's tests, so
// a Service assembled without New keeps behaving exactly as it did before
// vertex existed.
type provider struct {
	kind providerKind
	// vertex carries the resolved project/region/host; unused by studio.
	vertex vertexResource
	// tokens mints bearer tokens; nil on the studio path, and a configuration
	// error on the vertex path.
	tokens *tokenSource
}

// vertexResource is the AI Platform root plus the project and location every
// resource path is qualified with:
//
//	{base}/projects/{project}/locations/{region}/…
//
// The distinction between a URL and a RESOURCE NAME lives here, and it is the
// subtlest trap of the migration: `:generateContent` takes a URL, while the
// cachedContents body's `model` takes the bare name (`projects/…`, no host, no
// /v1) — passing the URL there is a 400 "The Model name 'https://…' is
// malformed" (measured). Both forms are built from the same fields so they can
// never drift apart.
//
// Pure data with pure methods. These paths are the part of the migration the
// platform rejects loudly (a wrong one 404s), so they are unit-testable without
// a Service, a server or an environment.
type vertexResource struct {
	base    string // https://{region}-aiplatform.googleapis.com/v1
	project string
	region  string
}

// modelName is the bare resource name: projects/{p}/locations/{l}/publishers/google/models/{m}.
func (r vertexResource) modelName(model string) string {
	return fmt.Sprintf("projects/%s/locations/%s/publishers/google/models/%s",
		r.project, r.region, NormalizeModelName(model))
}

// locationName is the bare projects/{p}/locations/{l} prefix.
func (r vertexResource) locationName() string {
	return fmt.Sprintf("projects/%s/locations/%s", r.project, r.region)
}

// model is the endpoint URL for one model.
func (r vertexResource) model(model string) string {
	return r.base + "/" + r.modelName(model)
}

// location is the endpoint URL of the project-location root that owns
// project-scoped resources (cachedContents, and the publisher model list).
func (r vertexResource) location() string {
	return r.base + "/" + r.locationName()
}

// providerFromEnv builds the provider named by GEMINI_PROVIDER.
//
// A vertex selection that is missing its project or its service-account key
// returns an ERROR and never a studio provider: a half-declared migration must
// not keep answering from AI Studio — or, worse, send an AI Studio key to the
// platform — while every dashboard still reports a healthy service. Studio
// reads none of these variables and cannot fail.
func providerFromEnv() (provider, error) {
	if providerKindFromEnv() != providerVertex {
		return provider{kind: providerStudio}, nil
	}
	saPath := strings.TrimSpace(os.Getenv("GEMINI_VERTEX_SA_FILE"))
	if saPath == "" {
		return provider{}, errors.New("GEMINI_PROVIDER=vertex requires GEMINI_VERTEX_SA_FILE " +
			"(path to the service-account JSON key); refusing to fall back to the AI Studio endpoint")
	}
	tokens, err := newTokenSource(saPath)
	if err != nil {
		return provider{}, err
	}
	project := strings.TrimSpace(os.Getenv("GEMINI_VERTEX_PROJECT"))
	if project == "" {
		// Fall back to the key's own project_id: the key already names its
		// project, and requiring both would make a working probe invocation
		// (vertexprobe -sa …) fail as a deployment.
		project = tokens.project
	}
	if project == "" {
		return provider{}, fmt.Errorf("GEMINI_PROVIDER=vertex requires GEMINI_VERTEX_PROJECT: "+
			"it is unset and %s carries no project_id", saPath)
	}
	region := vertexRegion()
	return provider{
		kind:   providerVertex,
		vertex: vertexResource{base: VertexPlatformBase(region), project: project, region: region},
		tokens: tokens,
	}, nil
}

// providerKindFromEnv reads GEMINI_PROVIDER. Anything that is not exactly
// "vertex" (case-insensitive) — unset, "studio", an empty string, a typo — is
// studio. This is the red line of the migration: the new path must be opt-in,
// so an unknown value can never silently reroute production.
func providerKindFromEnv() providerKind {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("GEMINI_PROVIDER")), string(providerVertex)) {
		return providerVertex
	}
	return providerStudio
}

// vertexRegion — GEMINI_VERTEX_REGION, defaulting to the measured target.
func vertexRegion() string {
	return envOr("GEMINI_VERTEX_REGION", vertexDefaultRegion)
}

// VertexHost is the AI Platform host that serves one location.
//
// Exported because cmd/vertexprobe used to interpolate the region into the host
// itself and therefore probed `global-aiplatform.googleapis.com`, a host that
// answers a bare HTML 404 for every model — which read as "global serves
// nothing" while production, going through the function below, served
// everything. One host rule, shared by the client and the gate, is the only way
// that cannot drift again.
func VertexHost(region string) string {
	if region == vertexGlobalRegion {
		return "https://aiplatform.googleapis.com"
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com", region)
}

// VertexPlatformBase is the version-qualified AI Platform root.
//
// GEMINI_VERTEX_API_BASE overrides it for exactly the reasons GEMINI_API_BASE
// exists on the studio side: to route through a relay when the host's egress is
// geo-blocked, and to let tests point the whole vertex path at a local stub.
// Value must include the /v1 segment, no trailing slash.
//
// Exported for the same reason as VertexHost: a probe that rebuilds this string
// is a probe that can disagree with the client it certifies.
func VertexPlatformBase(region string) string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_VERTEX_API_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return VertexHost(region) + "/v1"
}

// vertexVersionBase is VertexPlatformBase with a caller-chosen API version.
//
// The region catalog lives at v1beta1 while everything else this package sends
// is v1, and both must be built from the same host and honour the same override
// — so the version is substituted at the END of the base rather than appended
// to it. Appending would produce `.../v1/v1beta1/...` on the production base
// and, worse, would hide the version from a stub whose override already carries
// one: the test seam has to see the path the deployment actually sends, or it
// proves nothing.
func vertexVersionBase(region, version string) string {
	base := VertexPlatformBase(region)
	if i := strings.LastIndex(base, "/"); i > len("https://") {
		if isAPIVersionSegment(base[i+1:]) {
			return base[:i+1] + version
		}
	}
	return base + "/" + version
}

// isAPIVersionSegment reports whether one path segment names a Google API
// version (v1, v1beta, v1beta1, v2…). Shape-based rather than a fixed list, so
// a future v2 base is re-qualified instead of silently keeping the old segment.
func isAPIVersionSegment(seg string) bool {
	if len(seg) < 2 || seg[0] != 'v' || seg[1] < '0' || seg[1] > '9' {
		return false
	}
	for i := 2; i < len(seg); i++ {
		if c := seg[i]; (c < '0' || c > '9') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

// ValidVertexRegion reports whether region can be used as a Vertex location.
//
// This is a SECURITY check, not cosmetics: the region is interpolated into the
// request HOST, and that request carries a service-account bearer token. An
// unvalidated value containing a dot or a slash could aim a
// credential-bearing request at a host of the caller's choosing. Only the
// characters a real location id uses are accepted, which makes the hostname
// this package builds impossible to steer.
func ValidVertexRegion(region string) bool {
	if region == "" || len(region) > 40 {
		return false
	}
	for i := 0; i < len(region); i++ {
		c := region[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return region[0] != '-' && region[len(region)-1] != '-' && !strings.Contains(region, "--")
}

// publisherModelsURL is the catalog URL for ONE location.
//
// This is the route that did not exist at v1: the platform serves the
// publisher-model list at `/v1beta1/publishers/google/models` on the global and
// on every regional host (all four measured 2026-09-25), and answers 404 on the
// /v1 form of it. It is addressed by HOST — no project, no location segment —
// so it is rebuilt per region instead of from a provider's single resolved
// base; GEMINI_VERTEX_API_BASE still wins, so a relay or a test stub sees every
// region's request.
func publisherModelsURL(region, pageToken string) string {
	u := fmt.Sprintf("%s/publishers/google/models?pageSize=%d",
		vertexVersionBase(region, vertexListAPIVersion), vertexListPageSize)
	if pageToken != "" {
		u += "&pageToken=" + url.QueryEscape(pageToken)
	}
	return u
}

// ValidateProviderConfig reports, as an error, a GEMINI_PROVIDER=vertex
// deployment that cannot work — the check a server should run while it is
// starting, so a missing key file or project is a log line and a non-zero exit
// instead of a 500 on the first customer turn.
//
// It is a no-op for studio (the default), so wiring it into main() cannot
// change today's behaviour. It does not contact the network: only the
// service-account file is read, and the token exchange itself is deliberately
// left to the first real call (a startup that requires network reachability to
// Google would fail a deploy that is otherwise fine).
func ValidateProviderConfig() error {
	_, err := providerFromEnv()
	return err
}

// ready reports whether this provider can serve requests WITHOUT an API key.
// Only vertex can: its credential is the service account, so a vertex Service
// built with an empty api key must not be mistaken for mock mode — the product
// would otherwise answer customers with template replies while the logs said
// "not configured".
func (p provider) ready() bool {
	return p.kind == providerVertex && p.tokens != nil
}

// generateURL — the :generateContent endpoint.
func (p provider) generateURL(model string) string {
	if p.kind == providerVertex {
		return p.vertex.model(model) + ":generateContent"
	}
	return fmt.Sprintf("%s/models/%s:generateContent", apiBase(), NormalizeModelName(model))
}

// streamURL — the SSE variant. alt=sse is required on both platforms: without
// it the response is a JSON array delivered in one piece, which defeats the
// whole point of streaming.
func (p provider) streamURL(model string) string {
	if p.kind == providerVertex {
		return p.vertex.model(model) + ":streamGenerateContent?alt=sse"
	}
	return fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse",
		apiBase(), NormalizeModelName(model))
}

// embedURL — the two platforms do not even agree on the METHOD for embeddings.
// AI Studio serves :embedContent; the platform serves embedding models through
// the generic :predict. Probing :embedContent against the platform returns
// 400 `oneof field '_model' is already set` (measured 2026-09-25), so this is
// not a cosmetic naming difference: the wrong one fails every retrieval.
func (p provider) embedURL() string {
	if p.kind == providerVertex {
		return p.vertex.model(EmbeddingModel) + ":predict"
	}
	return fmt.Sprintf("%s/models/%s:embedContent", apiBase(), EmbeddingModel)
}

// batchEmbedURL — Vertex has no batchEmbedContents: one :predict carries every
// instance (see embedBatchBody).
func (p provider) batchEmbedURL() string {
	if p.kind == providerVertex {
		return p.vertex.model(EmbeddingModel) + ":predict"
	}
	return fmt.Sprintf("%s/models/%s:batchEmbedContents", apiBase(), EmbeddingModel)
}

// cachedContentsURL — the context-cache collection.
func (p provider) cachedContentsURL() string {
	if p.kind == providerVertex {
		return p.vertex.location() + "/cachedContents"
	}
	return apiBase() + "/cachedContents"
}

// cachedContentModel is the `model` field of a cachedContents create, and the
// name a cache is registered under.
//
// Vertex accepts ONLY the bare resource name here — no host, no /v1: passing
// the endpoint URL returns 400 "The Model name 'https://…' is malformed"
// (measured 2026-09-25). The region is implied by the already-authenticated
// endpoint.
func (p provider) cachedContentModel(model string) string {
	if p.kind == providerVertex {
		return p.vertex.modelName(model)
	}
	return "models/" + NormalizeModelName(model)
}

// listModelsURL — model discovery for the admin UI on this provider's own
// location.
//
// Vertex answers with a different envelope (`publisherModels` holding
// `publishers/google/models/{m}` names) — see publisherModelList.
//
// The vertex branch used to point at `.../locations/{l}/models`, on the belief
// that the publisher-model route 404s before authentication. That belief came
// from probing the route at /v1; at /v1beta1 it answers 200 (measured
// 2026-09-25), while the route it fell back to lists the project's OWN
// tuned/uploaded models — names this client cannot address, because every
// serving URL it builds is a `publishers/google/models/{m}` path. So the old
// fallback listed entries that could never be picked, and hid the publisher
// models that could.
func (p provider) listModelsURL() string {
	if p.kind == providerVertex {
		return publisherModelsURL(p.vertex.region, "")
	}
	return apiBase() + "/models?pageSize=200"
}

// authorize applies this provider's credential to req — the ONE place the
// authentication header is chosen.
//
// It takes a context because the vertex path may have to mint a token first,
// and that mint must honour the caller's deadline. It returns an error rather
// than panicking so a misconfigured deployment fails the request (loudly, and
// without sending anything) instead of crashing the server.
func (p provider) authorize(ctx context.Context, req *http.Request, apiKey string) error {
	if p.kind == providerVertex {
		if p.tokens == nil {
			return ErrVertexNotConfigured
		}
		token, err := p.tokens.accessToken(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
	// The key travels in the header, never the URL query: a transport error
	// would otherwise render the key inside the error string (Go's *url.Error
	// carries the full URL, redacting only userinfo).
	req.Header.Set("x-goog-api-key", apiKey)
	return nil
}
