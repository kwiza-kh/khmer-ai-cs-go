// Command vertexprobe is a READ-ONLY availability probe for Vertex AI
// (Gemini Enterprise Agent Platform). It answers, before anyone rewrites the
// Gemini client, the questions that decide whether a region/provider is usable:
//
//  1. Can this service account mint an OAuth token at all?
//  2. Which of the Gemini models this product depends on exist in the target
//     region? Model availability is REGION SCOPED, and the pinned names the
//     deployment serves today are not the names the platform served when this
//     probe was written — so the required set is a flag (-require), not a
//     constant nobody can update from the shell.
//  3. Do embeddings work, and do they still return 768 dimensions? The whole
//     knowledge base was embedded with gemini-embedding-001 at that width, so a
//     different width would force a re-embed of every chunk.
//  4. Does explicit context caching (cachedContents) work, and is it
//     creatable+deletable from here?
//  5. Does the preview TTS model exist? This is the one capability most likely
//     to be missing, because preview models are rarely served on the platform.
//
// It never writes to the tenant database, and it deletes every cached-content
// resource it creates. Run it on the application host so the service-account
// key never has to leave the machine:
//
//	vertexprobe -sa /opt/khmer-ai-cs/vertex-sa.json -project <project> \
//	            -region global
//
// Every URL is built through gemini.VertexPlatformBase — the same function the
// serving client uses — because `global` is the one location whose host carries
// NO region prefix. This binary used to interpolate `{region}-aiplatform...`
// itself, so `-region global` probed a host that answers a bare HTML 404 for
// every model and reported "required check failed" on a deployment that was
// serving traffic fine. A gate that disagrees with the thing it certifies is
// worse than no gate.
//
// Exit status is 0 when the mandatory checks pass (token, the -require models,
// embeddings) and 1 otherwise, so it can gate a deployment.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"khmer-ai-cs-go/internal/gemini"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// embeddingWidth mirrors gemini.embeddingVectorDimension and must stay equal to
// knowledge_chunks' vector(768) column: a different width would make every
// stored chunk unsearchable, and the model's own default (3072) is wrong here.
const embeddingWidth = 768

// defaultRequiredModels are the two names the live deployment serves: the DB
// model_configs default (the main chat model) and GEMINI_FAST_MODEL (the
// auxiliary path). Both are region-scoped — in asia-southeast1 they answer 404
// while global answers 200 (measured 2026-09-26) — which is the entire reason
// this set has to be required rather than merely listed.
//
// It used to be `gemini-2.5-flash` plus the `gemini-flash-lite-latest` alias.
// Neither is served anywhere on the platform, so the gate was red on a healthy
// box and, worse, stayed green if the model production actually ran on
// disappeared from the region.
//
// Corrected again on 2026-09-29: it still required `gemini-3.5-flash-lite` after
// the fast model moved to `gemini-3.8-flash` (2026-09-28) — i.e. the gate was
// checking a model this deployment no longer calls while the one it does call
// twice (main and fast are the same name now) was only half-covered. A required
// list that names the wrong models is the same class of bug as one that names a
// retired alias: it fails for the wrong reason, or passes for one.
const defaultRequiredModels = "gemini-3.8-flash"

// serviceAccount is the subset of the downloaded JSON key this probe needs.
type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	ProjectID   string `json:"project_id"`
	TokenURI    string `json:"token_uri"`
}

type probe struct {
	http      *http.Client
	token     string
	project   string
	region    string
	root      string // https://<host>/v1 — see gemini.VertexPlatformBase
	modelBase string // .../publishers/google/models
	locBase   string // .../locations/<region>
}

// result is one check's outcome. err is empty on success; status is the HTTP
// status when the check reached the API (0 when it failed locally).
type result struct {
	name    string
	ok      bool
	status  int
	detail  string
	fatal   bool // counts against the exit status
	skipped bool
}

func main() {
	saPath := flag.String("sa", "", "path to the service-account JSON key")
	project := flag.String("project", "", "GCP project ID (default: project_id from the key)")
	region := flag.String("region", "global", "Vertex region (production lives in global; pass -region to audit another)")
	timeout := flag.Duration("timeout", 30*time.Second, "per-request timeout")
	modelsFlag := flag.String("models", "", "comma-separated chat model candidates to list (default: built-in list)")
	requireFlag := flag.String("require", defaultRequiredModels, "comma-separated models that MUST resolve; a missing one fails the gate")
	capsFlag := flag.String("caps", "", "run the capability probe against this model and exit")
	audioDirFlag := flag.String("audiodir", "", "send every audio file in this directory as an inlineData part (needs -caps)")
	taskTypeFlag := flag.Bool("tasktype", false, "probe whether :predict task-type parameters condition the embedding (needs -caps)")
	flag.Parse()

	if *saPath == "" {
		fmt.Fprintln(os.Stderr, "vertexprobe: -sa is required")
		os.Exit(2)
	}
	sa, err := loadServiceAccount(*saPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vertexprobe: %v\n", err)
		os.Exit(2)
	}
	if *project == "" {
		*project = sa.ProjectID
	}
	if *project == "" {
		fmt.Fprintln(os.Stderr, "vertexprobe: no project id in the key; pass -project")
		os.Exit(2)
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	// The region goes into the request HOST of a credential-bearing request, so
	// the client's own validation applies here too — a probe must not be able to
	// aim a service-account token at a host of the operator's choosing.
	if !gemini.ValidVertexRegion(*region) {
		fmt.Fprintf(os.Stderr, "vertexprobe: -region %q is not a valid location id\n", *region)
		os.Exit(2)
	}
	required := splitNames(*requireFlag)
	if len(required) == 0 {
		fmt.Fprintln(os.Stderr, "vertexprobe: -require must name at least one model")
		os.Exit(2)
	}

	// One shared root with the serving client. Everything below is relative to
	// it, so no URL in this binary can disagree with the host rule again.
	root := gemini.VertexPlatformBase(*region)
	p := &probe{
		http:      &http.Client{Timeout: *timeout},
		project:   *project,
		region:    *region,
		root:      root,
		modelBase: fmt.Sprintf("%s/projects/%s/locations/%s/publishers/google/models", root, *project, *region),
		locBase:   fmt.Sprintf("%s/projects/%s/locations/%s", root, *project, *region),
	}

	fmt.Printf("vertexprobe — project=%s region=%s\n", p.project, p.region)
	fmt.Printf("  service account: %s\n\n", sa.ClientEmail)

	ctx := context.Background()
	var results []result

	// 1. Token minting. Everything else depends on it.
	if err := p.mintToken(ctx, sa); err != nil {
		fmt.Printf("✗ token minting failed: %v\n", err)
		os.Exit(1)
	}
	results = append(results, result{name: "oauth token minting", ok: true, detail: "bearer token acquired"})

	if m := strings.TrimSpace(*capsFlag); m != "" && *taskTypeFlag {
		if n := p.runTaskTypeProbe(ctx, m, "khmer customer service probe about delivery times"); n > 0 {
			os.Exit(1)
		}
		return
	}
	if m := strings.TrimSpace(*capsFlag); m != "" && strings.TrimSpace(*audioDirFlag) != "" {
		if n := p.runAudioDir(ctx, m, *audioDirFlag); n > 0 {
			os.Exit(1)
		}
		return
	}

	if m := strings.TrimSpace(*capsFlag); m != "" {
		if n := p.runCaps(ctx, m); n > 0 {
			os.Exit(1)
		}
		return
	}

	// 2. Chat models. The built-in list is a survey of what the region serves —
	//    it includes names this product does NOT run (a retired release, an
	//    alias the platform never recognised) purely because seeing them 404 is
	//    how an operator learns the list is region-scoped. The models that
	//    actually gate a deployment are -require, and they are probed even when
	//    -models replaces the survey list: a flag that can delete the checks it
	//    is supposed to fail is not a gate.
	chatCandidates := []string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-flash-lite-latest",
		"gemini-3.6-flash",
		"gemini-3.5-flash",
	}
	if strings.TrimSpace(*modelsFlag) != "" {
		chatCandidates = nil
		for _, m := range strings.Split(*modelsFlag, ",") {
			if m = strings.TrimSpace(m); m != "" {
				chatCandidates = append(chatCandidates, m)
			}
		}
	}
	fatalModels := make(map[string]bool, len(required))
	for _, m := range required {
		fatalModels[m] = true
		if !containsString(chatCandidates, m) {
			chatCandidates = append(chatCandidates, m)
		}
	}
	for _, m := range chatCandidates {
		results = append(results, p.checkChat(ctx, m, fatalModels[m]))
	}

	// 3. Embeddings. Vertex may serve these as :predict rather than
	//    :embedContent, so both are tried and reported.
	results = append(results, p.checkEmbedPredict(ctx, "gemini-embedding-001"))
	results = append(results, p.checkEmbedContent(ctx, "gemini-embedding-001"))

	// 4. Explicit context caching — creatable and removable. Cached content is
	//    pinned to a model, so the probe uses a required one: pointing it at a
	//    model the region does not serve reports "caching is broken" when only
	//    the name is wrong.
	results = append(results, p.checkCachedContents(ctx, required[0]))

	// 5. Preview TTS — the capability most likely to be missing.
	results = append(results, p.checkChat(ctx, "gemini-2.5-flash-preview-tts", false))

	printReport(results)

	for _, r := range results {
		if r.fatal && !r.ok {
			os.Exit(1)
		}
	}
}

func loadServiceAccount(path string) (serviceAccount, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return serviceAccount{}, fmt.Errorf("read %s: %w", path, err)
	}
	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return serviceAccount{}, fmt.Errorf("parse service-account JSON: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return serviceAccount{}, fmt.Errorf("service-account JSON is missing client_email or private_key")
	}
	return sa, nil
}

// mintToken signs the standard JWT-bearer assertion and exchanges it for an
// access token. This is the same flow the production client will need, so a
// failure here is a migration blocker, not a probe quirk.
func (p *probe) mintToken(ctx context.Context, sa serviceAccount) error {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   sa.ClientEmail,
		"scope": cloudPlatformScope,
		"aud":   sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		return fmt.Errorf("sign assertion: %w", err)
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sa.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token endpoint %d: %s", resp.StatusCode, firstLine(body))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return fmt.Errorf("token response had no access_token")
	}
	p.token = out.AccessToken
	return nil
}

func (p *probe) post(ctx context.Context, target string, payload any) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}

func (p *probe) checkChat(ctx context.Context, model string, fatal bool) result {
	r := result{name: "chat: " + model, fatal: fatal}
	payload := map[string]any{
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]any{{"text": "hi"}},
		}},
		"generationConfig": map[string]any{"maxOutputTokens": 8},
	}
	status, body, err := p.post(ctx, p.modelBase+"/"+model+":generateContent", payload)
	r.status = status
	if err != nil {
		r.detail = err.Error()
		return r
	}
	r.ok = status == http.StatusOK
	r.detail = summarize(status, body)
	return r
}

// checkEmbedPredict tries Vertex's :predict embedding shape, which is what the
// platform documents for embedding models (the AI Studio :embedContent shape is
// a different method).
//
// outputDimensionality is sent explicitly and verified: knowledge_chunks holds
// vector(768) and every stored chunk was embedded at that width, so a different
// width would invalidate the index. The model's own default is 3072 — sending
// the parameter is what keeps this migration a config change rather than a full
// re-embed of the knowledge base.
func (p *probe) checkEmbedPredict(ctx context.Context, model string) result {
	r := result{name: "embedding :predict: " + model, fatal: true}
	payload := map[string]any{
		"instances":  []map[string]any{{"content": "khmer customer service probe"}},
		"parameters": map[string]any{"outputDimensionality": embeddingWidth},
	}
	status, body, err := p.post(ctx, p.modelBase+"/"+model+":predict", payload)
	r.status = status
	if err != nil {
		r.detail = err.Error()
		return r
	}
	if status != http.StatusOK {
		r.detail = summarize(status, body)
		return r
	}
	got := dimensions(body)
	r.ok = got == fmt.Sprint(embeddingWidth)
	r.detail = fmt.Sprintf("dim=%s (want %d)", got, embeddingWidth)
	return r
}

// checkEmbedContent tries the AI Studio method name, so the report says
// explicitly whether the existing shape survives on the platform.
func (p *probe) checkEmbedContent(ctx context.Context, model string) result {
	r := result{name: "embedding :embedContent: " + model}
	payload := map[string]any{
		"model":   "models/" + model,
		"content": map[string]any{"parts": []map[string]any{{"text": "probe"}}},
	}
	status, body, err := p.post(ctx, p.modelBase+"/"+model+":embedContent", payload)
	r.status = status
	if err != nil {
		r.detail = err.Error()
		return r
	}
	r.ok = status == http.StatusOK
	r.detail = summarize(status, body)
	return r
}

// checkCachedContents creates a tiny cache and deletes it again.
//
// The create needs a model resource name and a prefix over the platform's
// minimum: measured 2026-09-27 on global + gemini-3.8-flash, the API answers
// `INVALID_ARGUMENT: The cached content is of 401 tokens. The minimum token
// count to start explicit caching is 4096`. The payload below is sized past
// that floor on purpose — "probe " costs one token per repetition — because a
// check that can only ever fail is how a capability gets written off as absent.
// The app's own guard (minCacheableTokens, default 1024) predates that number.
func (p *probe) checkCachedContents(ctx context.Context, model string) result {
	r := result{name: "context caching: cachedContents (" + model + ")"}
	payload := map[string]any{
		// model 必须是资源名而非 URL：传完整 endpoint 会得到 400 "The Model name
		// 'https://…' is malformed"。区域由已鉴权的 endpoint 隐含。
		"model":       fmt.Sprintf("projects/%s/locations/%s/publishers/google/models/%s", p.project, p.region, model),
		"ttl":         "60s",
		"contents":    []map[string]any{{"role": "user", "parts": []map[string]any{{"text": strings.Repeat("probe ", 4600)}}}},
		"displayName": "vertexprobe",
	}
	status, body, err := p.post(ctx, p.locBase+"/cachedContents", payload)
	r.status = status
	if err != nil {
		r.detail = err.Error()
		return r
	}
	if status != http.StatusOK {
		r.detail = summarize(status, body)
		return r
	}
	r.ok = true
	var created struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body, &created)
	// Always clean up: leaving a cache behind bills by token-hour. The name the
	// API returns is resource-relative, so it goes onto the same root the create
	// used — never a host rebuilt from the region.
	if created.Name != "" {
		if delErr := p.delete(ctx, p.root+"/"+created.Name); delErr != nil {
			r.detail = "created OK but delete failed: " + delErr.Error()
			return r
		}
	}
	r.detail = "created and deleted"
	return r
}

func (p *probe) delete(ctx context.Context, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// dimensions digs the returned embedding width out of either response shape.
func dimensions(body []byte) string {
	var v map[string]any
	if json.Unmarshal(body, &v) != nil {
		return "?"
	}
	for _, key := range []string{"predictions", "embeddings"} {
		arr, _ := v[key].([]any)
		if len(arr) == 0 {
			continue
		}
		first, _ := arr[0].(map[string]any)
		for _, vk := range []string{"embeddings", "values"} {
			if vals, ok := first[vk].(map[string]any); ok {
				if a, ok := vals["values"].([]any); ok {
					return fmt.Sprint(len(a))
				}
			}
			if a, ok := first[vk].([]any); ok {
				return fmt.Sprint(len(a))
			}
		}
	}
	return "?"
}

// summarize keeps the first line of an API error — that is where Google puts
// the actionable reason (PERMISSION_DENIED vs NOT_FOUND vs API not enabled).
func summarize(status int, body []byte) string {
	if status == http.StatusOK {
		return "OK"
	}
	// Google wraps failures as {"error":{"code":..,"status":"NOT_FOUND",
	// "message":".."}}. Taking the first LINE of that yields just "{", which
	// hides the whole reason — so read the message field out explicitly.
	var e struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg := strings.ReplaceAll(e.Error.Message, "\n", " ")
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return fmt.Sprintf("HTTP %d %s: %s", status, e.Error.Status, msg)
	}
	return fmt.Sprintf("HTTP %d: %s", status, firstLine(body))
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

// splitNames parses a comma-separated flag value, dropping blanks so
// `-require "a, ,b"` reads as exactly two models.
func splitNames(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func containsString(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func printReport(rs []result) {
	fmt.Println("── results ──────────────────────────────────────────────")
	// Fatal checks first so blockers are read first.
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].fatal && !rs[j].fatal })
	for _, r := range rs {
		mark := "•"
		switch {
		case r.ok:
			mark = "✓"
		case r.fatal:
			mark = "✗"
		}
		tag := ""
		if r.fatal {
			tag = "  [required]"
		}
		fmt.Printf("%s %-46s %s%s\n", mark, r.name, r.detail, tag)
	}
	fmt.Println()
	failed := 0
	for _, r := range rs {
		if r.fatal && !r.ok {
			failed++
		}
	}
	if failed > 0 {
		fmt.Printf("%d required check(s) failed — do not start the migration yet.\n", failed)
		return
	}
	fmt.Println("All required checks passed. Review the non-fatal rows for TTS/caching coverage.")
}
