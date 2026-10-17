package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// clearEnv unsets every variable the package reads so an ambient shell cannot
// turn a disabled test into a live purge attempt.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvAPIToken, EnvZoneID, EnvPurgeHost, EnvPublicURL} {
		t.Setenv(k, "")
	}
}

// With nothing configured the purge must be a no-op: the shipped default has
// neither a token nor a zone, and that must not cost a request or an error.
func TestPurgeDisabledWithoutConfig(t *testing.T) {
	clearEnv(t)
	c := FromEnv()
	if c.Enabled() {
		t.Fatalf("client enabled without token/zone/host: %+v", c)
	}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	c.Endpoint = srv.URL

	if err := c.PurgeURL(context.Background(), "https://media.example.com/kb/1/a.png"); err != nil {
		t.Fatalf("disabled purge returned %v, want nil", err)
	}
	if called {
		t.Fatal("disabled purge still sent a request")
	}
}

// Token and zone alone are not enough: without a known host there is nothing to
// validate the URL against, so the purge stays off rather than flushing blind.
func TestPurgeDisabledWithoutHost(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvAPIToken, "token")
	t.Setenv(EnvZoneID, "zone")
	if c := FromEnv(); c.Enabled() {
		t.Fatalf("client enabled without a purge host: %+v", c)
	}
}

// The purge host is derived from the public media URL (or overridden), so the
// operator only has to configure R2_PUBLIC_URL once.
func TestFromEnvDerivesHost(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvPublicURL, "https://media.example.com/")
	if got := FromEnv().Host; got != "media.example.com" {
		t.Fatalf("host from R2_PUBLIC_URL = %q, want media.example.com", got)
	}

	t.Setenv(EnvPurgeHost, "https://cdn.example.com:8443/prefix")
	if got := FromEnv().Host; got != "cdn.example.com" {
		t.Fatalf("host from CLOUDFLARE_PURGE_HOST = %q, want cdn.example.com", got)
	}

	t.Setenv(EnvPurgeHost, "")
	t.Setenv(EnvPublicURL, "")
	if got := FromEnv().Host; got != "" {
		t.Fatalf("no source must yield an empty host, got %q", got)
	}
}

// A 2xx is success, and the request must carry the documented shape: bearer
// auth, the zone in the path, and the URL in {"files":[...]}.
func TestPurgeURLSuccess(t *testing.T) {
	clearEnv(t)
	var gotPath, gotAuth, gotMethod string
	var gotBody map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"x"}}`))
	}))
	defer srv.Close()

	c := Client{APIToken: "secret-token", ZoneID: "zone123", Host: "media.example.com", Endpoint: srv.URL}
	target := "https://media.example.com/kb/1/abc.png"
	if err := c.PurgeURL(context.Background(), target); err != nil {
		t.Fatalf("PurgeURL = %v, want nil", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/zones/zone123/purge_cache" {
		t.Fatalf("path = %q, want /zones/zone123/purge_cache", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if len(gotBody["files"]) != 1 || gotBody["files"][0] != target {
		t.Fatalf("body = %v, want files=[%s]", gotBody, target)
	}
}

// A non-2xx is reported with the status and a bounded body excerpt; it must not
// panic and it must not look like success.
func TestPurgeURLNon2xx(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Unauthorized to access zone"}]}`))
	}))
	defer srv.Close()

	c := Client{APIToken: "bad", ZoneID: "zone123", Host: "media.example.com", Endpoint: srv.URL}
	err := c.PurgeURL(context.Background(), "https://media.example.com/kb/1/abc.png")
	if err == nil {
		t.Fatal("403 must be an error")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("error = %v, want status and body excerpt", err)
	}
}

// Purging a URL on another host would flush a zone we do not own: refuse it.
func TestPurgeURLRefusesForeignHost(t *testing.T) {
	clearEnv(t)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	c := Client{APIToken: "t", ZoneID: "z", Host: "media.example.com", Endpoint: srv.URL}
	err := c.PurgeURL(context.Background(), "https://evil.example.net/kb/1/a.png")
	if err == nil {
		t.Fatal("foreign host must be refused")
	}
	if called {
		t.Fatal("refused URL still reached the API")
	}
	if err := c.PurgeURL(context.Background(), "not-a-url"); err == nil {
		t.Fatal("relative/invalid target must be refused")
	}
}
