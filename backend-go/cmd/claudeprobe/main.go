// Command claudeprobe — READ-ONLY availability probe for Claude models on
// Vertex AI (Gemini Enterprise Agent Platform), for the Anthropic provider
// integration.
//
// vertexprobe only speaks the Gemini surface
// (publishers/google/models/<m>:generateContent), so it cannot answer whether a
// Claude model is reachable from THIS service account in THIS location. The
// Gemini-style list-models call does not cover partner models either. This probe
// asks the only question that matters before writing a client: does
// publishers/anthropic/models/claude-haiku-5-5:rawPredict answer, and with what
// status, from our own key?
//
// Run it on the application host so the key never leaves the machine:
//
//	claudeprobe -sa /opt/khmer-ai-cs/vertex-sa.json -project <project> \
//	            -model claude-haiku-5-5 -regions global,us,eu
//
// It sends a 16-token "ping" per location (a few hundred tokens of spend at
// most) and writes nothing.
package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	ProjectID   string `json:"project_id"`
}

// vertexBase mirrors gemini.VertexPlatformBase: `global` has NO region prefix,
// while the multi-region families have their own hosts
// (aiplatform.us.rep.googleapis.com), and everything else is <region>-aiplatform.
// Getting this wrong is how the old probe reported a false red on `global`.
func vertexBase(region string) string {
	switch region {
	case "global":
		return "https://aiplatform.googleapis.com"
	case "us", "eu":
		return fmt.Sprintf("https://aiplatform.%s.rep.googleapis.com", region)
	default:
		return fmt.Sprintf("https://%s-aiplatform.googleapis.com", region)
	}
}

func loadServiceAccount(path string) (*serviceAccount, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &sa, nil
}

func (sa *serviceAccount) accessToken(ctx context.Context, httpClient *http.Client) (string, error) {
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("private_key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if rsaKey, rsaErr := x509.ParsePKCS1PrivateKey(block.Bytes); rsaErr == nil {
			key = rsaKey
		} else {
			return "", fmt.Errorf("parse private key: %w", err)
		}
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("private key is %T, want *rsa.PrivateKey", key)
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   sa.ClientEmail,
		"scope": cloudPlatformScope,
		"aud":   tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(rsaKey)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {signed},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint %d: %s", res.StatusCode, truncate(body, 300))
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.AccessToken == "" {
		return "", fmt.Errorf("token response unparseable: %s", truncate(body, 300))
	}
	return parsed.AccessToken, nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func main() {
	saPath := flag.String("sa", "", "path to the service-account JSON key")
	project := flag.String("project", "", "GCP project ID (default: project_id from the key)")
	model := flag.String("model", "claude-haiku-5-5", "Claude model id to probe")
	regions := flag.String("regions", "global", "comma-separated locations to probe")
	timeout := flag.Duration("timeout", 60*time.Second, "per-request timeout")
	skipStream := flag.Bool("skip-stream", false, "skip the :streamRawPredict check")
	quotasFlag := flag.Bool("quotas", false, "only read the project's aiplatform quota limits (no inference)")
	quotaFilter := flag.String("quota-filter", "anthropic", "case-insensitive substring matched against a quota's id/metric/display name/dimensions")
	flag.Parse()

	if *saPath == "" {
		fmt.Fprintln(os.Stderr, "claudeprobe: -sa is required")
		os.Exit(2)
	}
	sa, err := loadServiceAccount(*saPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claudeprobe: %v\n", err)
		os.Exit(2)
	}
	if *project == "" {
		*project = sa.ProjectID
	}
	if *project == "" {
		fmt.Fprintln(os.Stderr, "claudeprobe: no project id in the key; pass -project")
		os.Exit(2)
	}
	httpClient := &http.Client{Timeout: *timeout}
	ctx := context.Background()

	token, err := sa.accessToken(ctx, httpClient)
	if err != nil {
		fmt.Printf("✗ oauth token: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ oauth token minted (client=%s project=%s)\n", sa.ClientEmail, *project)

	if *quotasFlag {
		if err := printQuotas(ctx, httpClient, token, *project, *quotaFilter); err != nil {
			fmt.Printf("✗ quota read: %v\n", err)
			os.Exit(1)
		}
		return
	}

	body := map[string]any{
		"anthropic_version": "vertex-2023-10-16",
		"messages":          []map[string]any{{"role": "user", "content": "ping"}},
		"max_tokens":        16,
	}
	raw, _ := json.Marshal(body)

	ok := 0
	for _, region := range strings.Split(*regions, ",") {
		region = strings.TrimSpace(region)
		if region == "" {
			continue
		}
		base := vertexBase(region)
		modelPath := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/anthropic/models/%s",
			base, *project, region, *model)

		// 1) listing (may or may not be supported for partner publishers)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, modelPath, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := httpClient.Do(req)
		if err != nil {
			fmt.Printf("• %-8s list: transport error: %v\n", region, err)
		} else {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
			res.Body.Close()
			fmt.Printf("• %-8s list: HTTP %d %s\n", region, res.StatusCode, truncate(b, 120))
		}

		// 2) the real question: rawPredict
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, modelPath+":rawPredict", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		res, err = httpClient.Do(req)
		if err != nil {
			fmt.Printf("✗ %-8s rawPredict: transport error: %v\n", region, err)
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
		took := time.Since(start).Round(time.Millisecond)
		if res.StatusCode == http.StatusOK {
			ok++
			fmt.Printf("✓ %-8s rawPredict: HTTP 200 in %s — %s\n", region, took, truncate(b, 220))
		} else {
			fmt.Printf("✗ %-8s rawPredict: HTTP %d in %s — %s\n", region, res.StatusCode, took, truncate(b, 220))
		}

		// 3) streaming (our chat path needs SSE)
		if *skipStream || res.StatusCode != http.StatusOK {
			continue
		}
		sreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, modelPath+":streamRawPredict", bytes.NewReader(raw))
		sreq.Header.Set("Authorization", "Bearer "+token)
		sreq.Header.Set("Content-Type", "application/json")
		sres, err := httpClient.Do(sreq)
		if err != nil {
			fmt.Printf("? %-8s streamRawPredict: transport error: %v\n", region, err)
			continue
		}
		sb, _ := io.ReadAll(io.LimitReader(sres.Body, 1<<16))
		sres.Body.Close()
		fmt.Printf("%s %-8s streamRawPredict: HTTP %d — %s\n",
			map[bool]string{true: "✓", false: "✗"}[sres.StatusCode == http.StatusOK],
			region, sres.StatusCode, truncate(sb, 160))
	}

	if ok == 0 {
		os.Exit(1)
	}
}

// printQuotas reads the project's aiplatform quota limits from the Cloud Quotas
// API. The 429 a Claude call returns names the metric and the base model but not
// the limit; this answers "is it 0, and can it be raised from here?" — exactly
// what a quota request needs. It needs cloudquotas.quotas.get: a 403 below means
// this key cannot read quotas, not that the quota is fine.
func printQuotas(ctx context.Context, httpClient *http.Client, token, project, filter string) error {
	filter = strings.ToLower(filter)
	pageToken := ""
	printed := 0
	for page := 0; page < 10; page++ {
		u := fmt.Sprintf("https://cloudquotas.googleapis.com/v1/projects/%s/locations/global/services/aiplatform.googleapis.com/quotaInfos?pageSize=200", project)
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("cloudquotas HTTP %d: %s", res.StatusCode, truncate(body, 300))
		}
		var parsed struct {
			QuotaInfos []struct {
				QuotaID     string         `json:"quotaId"`
				Metric      string         `json:"metric"`
				DisplayName string         `json:"quotaDisplayName"`
				Dimensions  map[string]any `json:"dimensions"`
				Details     struct {
					Value string `json:"value"`
				} `json:"details"`
				IsPrecise                bool `json:"isPrecise"`
				QuotaIncreaseEligibility struct {
					IsEligible          bool   `json:"isEligible"`
					IneligibilityReason string `json:"ineligibilityReason"`
				} `json:"quotaIncreaseEligibility"`
			} `json:"quotaInfos"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return fmt.Errorf("parse cloudquotas response: %w", err)
		}
		for _, q := range parsed.QuotaInfos {
			dims, _ := json.Marshal(q.Dimensions)
			hay := strings.ToLower(q.QuotaID + " " + q.Metric + " " + q.DisplayName + " " + string(dims))
			if filter != "" && !strings.Contains(hay, filter) {
				continue
			}
			printed++
			fmt.Printf("• %s\n    metric: %s\n    limit: %s (precise=%v, adjustable=%v)\n    dimensions: %s\n",
				q.QuotaID, q.Metric, q.Details.Value, q.IsPrecise, q.QuotaIncreaseEligibility.IsEligible, string(dims))
			if printed >= 25 {
				fmt.Println("(truncated at 25 matching quotas)")
				return nil
			}
		}
		if parsed.NextPageToken == "" {
			break
		}
		pageToken = parsed.NextPageToken
	}
	if printed == 0 {
		fmt.Printf("no quota matched %q (the service may expose them under a different location)\n", filter)
	}
	return nil
}
