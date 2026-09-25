package gemini

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Vertex service-account authentication.
//
// Vertex does not take an API key. Every request carries an OAuth2 access token
// minted from a service-account JSON key: sign a JWT assertion with the key's
// RSA private key, exchange it at the key's token_uri for a bearer token, and
// cache that token until shortly before it expires. The flow below is the one
// cmd/vertexprobe verified against the real platform; it is reproduced here
// rather than re-derived, because the failure modes (wrong audience, wrong
// grant_type, missing scope) are all 400s that name none of the three.
//
// No golang.org/x/oauth2: the exchange is one signed POST, and the dependency
// would drag in a credential-resolution stack (well-known files, metadata
// server, workload identity) that this deployment deliberately does not use —
// the SA file path is explicit config, so which identity a pod runs as is never
// a guess.

// Sentinel errors for the four distinct ways a service-account setup fails.
// They are exported because the operator's next action differs per class (fix
// the path / fix the JSON / fix the key / fix the credentials at Google), and
// a single "vertex auth failed" hides that. They are also what the startup
// check reports, so a bad deploy is caught before a customer turn pays for it.
var (
	ErrServiceAccountUnreadable = errors.New("vertex: cannot read the service-account file")
	ErrServiceAccountMalformed  = errors.New("vertex: service-account file is not a usable JSON key")
	ErrServiceAccountKeyInvalid = errors.New("vertex: service-account private_key cannot be parsed")
	ErrVertexNotConfigured      = errors.New("vertex: provider has no service account")
)

// tokenRefreshSkew is how long before the real expiry a token is replaced.
//
// A token is minted once and then reused for every request, so its last
// minutes are the dangerous ones: a request that starts with 2 seconds left
// arrives after the token is dead, and the platform answers 401 UNAUTHENTICATED
// — which postWithRetry does NOT retry (it is a 4xx, and retrying with the same
// dead token could not help anyway). Five minutes is far longer than any single
// request this package makes, including the 60s client timeout times three
// attempts.
const tokenRefreshSkew = 5 * time.Minute

// defaultTokenURI is Google's OAuth2 token endpoint, used only when the key
// omits token_uri (which no downloaded key does, but a hand-written test key
// might).
const defaultTokenURI = "https://oauth2.googleapis.com/token"

// serviceAccount is the subset of a downloaded key this client needs. The other
// fields (type, private_key_id, client_id, auth_uri…) are ignored on purpose:
// nothing here validates them, and a key missing one of THESE four is the only
// thing that changes behaviour.
type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	ProjectID   string `json:"project_id"`
	TokenURI    string `json:"token_uri"`
}

// tokenSource mints and caches one access token for one service account.
//
// The credentials are read and parsed ONCE, at construction, so a broken key
// is a startup error rather than the first customer turn's error. The cost is
// that rotating the key needs a restart — acceptable here, because the file
// path is fixed config and the rotation workflow already restarts the process
// to pick up the new secret; the alternative (re-reading per mint) trades a
// startup guarantee for a failure mode where a half-written secret file takes
// the service down an hour later, mid-traffic.
type tokenSource struct {
	email    string
	key      *rsa.PrivateKey
	tokenURI string
	project  string
	client   *http.Client
	// now is a seam for the expiry tests; production always uses time.Now.
	now func() time.Time

	// mu guards the cached token AND serialises minting. Holding it across the
	// token request is deliberate: without it, a burst of concurrent turns on a
	// cold cache would each mint its own token (measured upstream: the platform
	// rate-limits token minting independently of inference), so the first
	// caller pays the latency and the rest wait for its result.
	mu      sync.Mutex
	token   string
	expires time.Time
}

// newTokenSource reads and validates a service-account JSON key.
func newTokenSource(path string) (*tokenSource, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrServiceAccountUnreadable, path, err)
	}
	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrServiceAccountMalformed, path, err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, fmt.Errorf("%w: %s is missing client_email or private_key", ErrServiceAccountMalformed, path)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrServiceAccountKeyInvalid, path, err)
	}
	uri := strings.TrimSpace(sa.TokenURI)
	if uri == "" {
		uri = defaultTokenURI
	}
	return &tokenSource{
		email:    sa.ClientEmail,
		key:      key,
		tokenURI: uri,
		project:  strings.TrimSpace(sa.ProjectID),
		// Its own client, not the serving one: this call must not be cancelled
		// by an admin hot-reload swapping s.client, and it must fail faster than
		// a generation call — a token endpoint that hangs for 60s would stall a
		// reply path the same way the unbounded embed hop did (see embedBudget).
		client: &http.Client{Timeout: 20 * time.Second},
		now:    time.Now,
	}, nil
}

// accessToken returns a valid bearer token, minting one only when the cached
// token is missing or about to expire.
func (t *tokenSource) accessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && t.now().Before(t.expires) {
		return t.token, nil
	}
	token, expires, err := t.mint(ctx)
	if err != nil {
		// The previously cached token, if any, is deliberately NOT returned as a
		// fallback: it is either expired or within the skew window, and a 401
		// from the platform is harder to diagnose than this error.
		return "", err
	}
	t.token, t.expires = token, expires
	return token, nil
}

// mint signs the JWT-bearer assertion and exchanges it for an access token.
func (t *tokenSource) mint(ctx context.Context) (string, time.Time, error) {
	now := t.now()
	// Claims exactly as the platform requires: iss = the service account,
	// aud = the endpoint the assertion is presented to, scope = what the token
	// may do. A mismatch on any of the three is a 400 `invalid_grant` whose
	// body does not say which one — hence the probe-first approach.
	claims := jwt.MapClaims{
		"iss":   t.email,
		"scope": cloudPlatformScope,
		"aud":   t.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(t.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("vertex: sign service-account assertion: %w", err)
	}
	form := url.Values{
		// The grant type that says "this assertion IS the credential" — the
		// authorization_code and refresh_token grants are the other two, and
		// using one of them here is another silent invalid_grant.
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("vertex: token request to %s: %w", t.tokenURI, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// Neither the assertion nor the returned token is logged: the assertion
		// is a bearer credential for an hour, and this string ends up in error
		// logs that outlive it.
		return "", time.Time{}, fmt.Errorf("vertex: token endpoint %s returned HTTP %d: %s",
			t.tokenURI, resp.StatusCode, truncateRunes(strings.TrimSpace(string(body)), 300))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("vertex: token endpoint %s returned no access_token", t.tokenURI)
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		// A missing expires_in must not mean "never expires": assuming the
		// platform's usual hour keeps the cache honest instead of handing out a
		// token that dies unnoticed.
		ttl = time.Hour
	}
	lifetime := ttl - tokenRefreshSkew
	if lifetime < ttl/4 {
		// A token whose whole life fits inside the skew window would be minted
		// again on every single request. Caching it for a quarter of its life
		// keeps the early-refresh guarantee without a mint-per-request storm.
		lifetime = ttl / 4
	}
	return out.AccessToken, now.Add(lifetime), nil
}
