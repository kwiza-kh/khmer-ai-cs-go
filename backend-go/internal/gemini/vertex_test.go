package gemini

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// writeTestServiceAccount writes a throwaway service-account key — a real RSA
// key in Google's own PKCS#8 PEM form — and returns its path plus the public
// key, so a stub token endpoint can verify what was actually signed. A fixture
// string would let a broken PEM/JSON path pass unnoticed.
func writeTestServiceAccount(t *testing.T, tokenURI, projectID string) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	sa := map[string]any{
		"type":         "service_account",
		"project_id":   projectID,
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "vertex-test@example.iam.gserviceaccount.com",
		"token_uri":    tokenURI,
	}
	raw, err := json.Marshal(sa)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, &key.PublicKey
}

// newTokenStub serves the OAuth2 token exchange with a fixed access token.
func newTokenStub(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"expires_in":3600,"token_type":"Bearer"}`, token)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestTokenSourceSignsTheVerifiedAssertion pins the three claims the platform
// validates and the grant type it is presented under. A wrong iss/aud/scope or
// grant_type is a 400 invalid_grant whose body names none of them, which is
// exactly the class of failure the probe had to burn round trips to find.
func TestTokenSourceSignsTheVerifiedAssertion(t *testing.T) {
	var (
		calls     int32
		assertion string
		grantType string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("token request Content-Type = %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		assertion = r.PostForm.Get("assertion")
		grantType = r.PostForm.Get("grant_type")
		_, _ = w.Write([]byte(`{"access_token":"tok-1","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	saPath, pub := writeTestServiceAccount(t, srv.URL, "proj-from-key")
	ts, err := newTokenSource(saPath)
	if err != nil {
		t.Fatalf("newTokenSource: %v", err)
	}
	got, err := ts.accessToken(context.Background())
	if err != nil {
		t.Fatalf("accessToken: %v", err)
	}
	if got != "tok-1" {
		t.Fatalf("accessToken = %q, want tok-1", got)
	}
	if grantType != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Errorf("grant_type = %q", grantType)
	}

	parsed, err := jwt.Parse(assertion, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", tok.Header["alg"])
		}
		return pub, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("assertion does not verify against the service-account key: %v", err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("claims are not a MapClaims")
	}
	if iss, _ := claims.GetIssuer(); iss != "vertex-test@example.iam.gserviceaccount.com" {
		t.Errorf("iss = %q, want the service account email", iss)
	}
	if aud, _ := claims.GetAudience(); len(aud) != 1 || aud[0] != srv.URL {
		t.Errorf("aud = %v, want the token endpoint %q", aud, srv.URL)
	}
	if scope, _ := claims["scope"].(string); scope != cloudPlatformScope {
		t.Errorf("scope = %q, want %q", scope, cloudPlatformScope)
	}
	iat, _ := claims.GetIssuedAt()
	exp, _ := claims.GetExpirationTime()
	if iat == nil || exp == nil || exp.Sub(iat.Time) != time.Hour {
		t.Errorf("iat/exp = %v/%v, want a one-hour assertion", iat, exp)
	}
	if ct := atomic.LoadInt32(&calls); ct != 1 {
		t.Errorf("token endpoint called %d times for one mint", ct)
	}
}

// TestTokenSourceCachesAndRefreshesEarly guards the cost model: a token is
// minted once and reused, and replaced BEFORE it expires. Without the early
// refresh a token with seconds left is sent on a request that outlives it, and
// the platform answers 401 — which is a 4xx, so postWithRetry does not retry it.
func TestTokenSourceCachesAndRefreshesEarly(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, n)
	}))
	defer srv.Close()

	saPath, _ := writeTestServiceAccount(t, srv.URL, "p")
	ts, err := newTokenSource(saPath)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	ts.now = func() time.Time { return base }

	for i := 0; i < 3; i++ {
		if _, err := ts.accessToken(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("a cached token must be reused: %d token requests", got)
	}

	// Just inside the 5-minute refresh window (3600s - 5min = 3300s of life).
	base = base.Add(3299 * time.Second)
	if _, err := ts.accessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("a token with more than the skew left must not be re-minted: %d requests", got)
	}
	base = base.Add(2 * time.Second)
	if _, err := ts.accessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("a token inside the refresh window must be replaced: %d requests", got)
	}
}

// TestTokenSourceSingleFlightsConcurrentTurns — the mutex is held across the
// token request on purpose: a burst of cold-cache turns must produce ONE mint,
// not one per turn.
func TestTokenSourceSingleFlightsConcurrentTurns(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseMint := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-release
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	}))
	defer srv.Close()
	// Idempotent: a failure before the explicit release below must not leave the
	// handler (and srv.Close) blocked forever.
	defer releaseMint()

	saPath, _ := writeTestServiceAccount(t, srv.URL, "p")
	ts, err := newTokenSource(saPath)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := ts.accessToken(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if tok != "tok" {
				errs <- fmt.Errorf("token = %q", tok)
			}
		}()
	}
	// Give the goroutines time to pile up on the mutex, then let the single
	// in-flight mint answer them all.
	time.Sleep(100 * time.Millisecond)
	releaseMint()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("concurrent turns minted %d tokens, want 1", got)
	}
}

// TestTokenSourceErrorClasses — the four failures an operator has to tell
// apart are four different next actions: fix the path, the JSON, the key, or
// the credentials at Google.
func TestTokenSourceErrorClasses(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		_, err := newTokenSource(filepath.Join(t.TempDir(), "absent.json"))
		if !errors.Is(err, ErrServiceAccountUnreadable) {
			t.Fatalf("err = %v, want ErrServiceAccountUnreadable", err)
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sa.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := newTokenSource(path)
		if !errors.Is(err, ErrServiceAccountMalformed) {
			t.Fatalf("err = %v, want ErrServiceAccountMalformed", err)
		}
	})
	t.Run("missing fields", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sa.json")
		if err := os.WriteFile(path, []byte(`{"project_id":"p"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := newTokenSource(path)
		if !errors.Is(err, ErrServiceAccountMalformed) || !strings.Contains(err.Error(), "client_email") {
			t.Fatalf("err = %v, want a malformed-key error naming the missing field", err)
		}
	})
	t.Run("bad private key", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sa.json")
		body := `{"client_email":"a@b.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nnot-a-key\n-----END PRIVATE KEY-----\n"}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := newTokenSource(path)
		if !errors.Is(err, ErrServiceAccountKeyInvalid) {
			t.Fatalf("err = %v, want ErrServiceAccountKeyInvalid", err)
		}
	})
	t.Run("token endpoint non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))
		}))
		defer srv.Close()
		saPath, _ := writeTestServiceAccount(t, srv.URL, "p")
		ts, err := newTokenSource(saPath)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ts.accessToken(context.Background())
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "invalid_grant") {
			t.Fatalf("err = %v, want the status and the endpoint's own reason", err)
		}
		// The assertion is a bearer credential for an hour: it must not be part
		// of an error string that outlives it.
		if strings.Contains(err.Error(), "assertion") {
			t.Errorf("the signed assertion leaked into the error: %v", err)
		}
	})
	t.Run("no access token in body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"token_type":"Bearer"}`))
		}))
		defer srv.Close()
		saPath, _ := writeTestServiceAccount(t, srv.URL, "p")
		ts, err := newTokenSource(saPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ts.accessToken(context.Background()); err == nil || !strings.Contains(err.Error(), "access_token") {
			t.Fatalf("err = %v, want a missing-access_token error", err)
		}
	})
}

// TestValidateProviderConfig — the startup check. It must be silent on the
// studio path (so wiring it into main() cannot change today's behaviour) and
// explicit on a half-declared vertex deployment.
func TestValidateProviderConfig(t *testing.T) {
	t.Run("studio default is fine", func(t *testing.T) {
		t.Setenv("GEMINI_PROVIDER", "")
		t.Setenv("GEMINI_VERTEX_SA_FILE", "")
		if err := ValidateProviderConfig(); err != nil {
			t.Fatalf("studio must never fail this check: %v", err)
		}
	})
	t.Run("a bad vertex key file cannot fail studio", func(t *testing.T) {
		t.Setenv("GEMINI_PROVIDER", "studio")
		t.Setenv("GEMINI_VERTEX_SA_FILE", "/does/not/exist.json")
		t.Setenv("GEMINI_VERTEX_PROJECT", "")
		if err := ValidateProviderConfig(); err != nil {
			t.Fatalf("studio ignores the vertex variables: %v", err)
		}
	})
	t.Run("vertex without a key file", func(t *testing.T) {
		t.Setenv("GEMINI_PROVIDER", "vertex")
		t.Setenv("GEMINI_VERTEX_SA_FILE", "")
		err := ValidateProviderConfig()
		if err == nil || !strings.Contains(err.Error(), "GEMINI_VERTEX_SA_FILE") {
			t.Fatalf("err = %v, want a message naming GEMINI_VERTEX_SA_FILE", err)
		}
	})
	t.Run("vertex without a project anywhere", func(t *testing.T) {
		saPath, _ := writeTestServiceAccount(t, "https://oauth2.googleapis.com/token", "")
		t.Setenv("GEMINI_PROVIDER", "vertex")
		t.Setenv("GEMINI_VERTEX_SA_FILE", saPath)
		t.Setenv("GEMINI_VERTEX_PROJECT", "")
		err := ValidateProviderConfig()
		if err == nil || !strings.Contains(err.Error(), "GEMINI_VERTEX_PROJECT") {
			t.Fatalf("err = %v, want a message naming GEMINI_VERTEX_PROJECT", err)
		}
	})
	t.Run("vertex takes the project from the key", func(t *testing.T) {
		saPath, _ := writeTestServiceAccount(t, "https://oauth2.googleapis.com/token", "proj-from-key")
		t.Setenv("GEMINI_PROVIDER", "vertex")
		t.Setenv("GEMINI_VERTEX_SA_FILE", saPath)
		t.Setenv("GEMINI_VERTEX_PROJECT", "")
		if err := ValidateProviderConfig(); err != nil {
			t.Fatalf("the key's own project_id must be accepted: %v", err)
		}
	})
}
