// Package cloudflare evicts objects from Cloudflare's edge cache.
//
// We need exactly one operation: after deleting an object from R2 we must tell
// the edge to forget its copy. media.wanfanginsulationmaterial.com sits behind
// Cloudflare, so a deleted knowledge-base picture kept answering 200 from the
// edge long after the bucket was clean (2026-10-16, the first KB-photo delete in
// production); a merchant who removes a product photo expects it gone.
//
// Everything here is best-effort by design: a purge failure means the edge may
// keep serving a stale object until it expires, which is a cache problem, never
// a reason to fail the delete the operator asked for.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Environment variables this package reads. The token and the zone are both
// required: with either missing the purge is a no-op, which is the shipped
// default (nothing breaks on a deployment that does not use Cloudflare).
const (
	EnvAPIToken  = "CLOUDFLARE_API_TOKEN"
	EnvZoneID    = "CLOUDFLARE_ZONE_ID"
	EnvPurgeHost = "CLOUDFLARE_PURGE_HOST"
	EnvPublicURL = "R2_PUBLIC_URL"
)

// DefaultEndpoint is Cloudflare's v4 API. Tests replace it via Client.Endpoint.
const DefaultEndpoint = "https://api.cloudflare.com/client/v4"

// bodyLimit caps how much of a failed response body we quote back. Cloudflare
// errors are small; the cap only exists so a proxy's HTML error page cannot
// flood a log line.
const bodyLimit = 512

// defaultTimeout bounds one purge attempt. A delete request waits on this call,
// so it must never hang: a timeout is just another logged purge failure.
const defaultTimeout = 5 * time.Second

// Client is the minimal purge surface: one token, one zone, one host.
//
// Host is the only hostname this client is allowed to purge. It comes from
// CLOUDFLARE_PURGE_HOST or, failing that, R2_PUBLIC_URL — the same host the
// pictures are actually served from, which keeps a misconfigured caller from
// flushing the wrong zone.
type Client struct {
	APIToken string
	ZoneID   string
	Host     string

	// Endpoint overrides the API base URL (tests). Empty means DefaultEndpoint.
	Endpoint string
	// HTTP overrides the transport (tests). Empty means a 5s-timeout client.
	HTTP *http.Client
}

// FromEnv builds the client the application uses.
//
// Read from the environment rather than config.Config on purpose: this package
// has no other configuration, the call site is a rare delete path, and adding a
// field to the config struct would mean touching the startup wiring for a
// feature whose default is "off".
func FromEnv() Client {
	return Client{
		APIToken: strings.TrimSpace(os.Getenv(EnvAPIToken)),
		ZoneID:   strings.TrimSpace(os.Getenv(EnvZoneID)),
		Host:     purgeHost(),
	}
}

// purgeHost resolves the hostname whose cache we are allowed to purge.
func purgeHost() string {
	if h := hostOf(os.Getenv(EnvPurgeHost)); h != "" {
		return h
	}
	return hostOf(os.Getenv(EnvPublicURL))
}

// hostOf extracts a bare lowercase hostname from a URL or a bare host:port.
// "" means "no usable host" (unset, unparsable), which disables purging rather
// than guessing.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Enabled reports whether purging can run at all. With no token, no zone or no
// known host there is nothing truthful to send, and a request would either 403
// or hit some other tenant's zone — so the disabled state is a silent no-op.
func (c Client) Enabled() bool {
	return c.APIToken != "" && c.ZoneID != "" && c.Host != ""
}

// PurgeURL asks Cloudflare to drop one absolute URL from its edge cache.
//
// A disabled client returns nil without touching the network: callers treat the
// purge as fire-and-forget and only log a returned error. A URL whose host is
// not the configured purge host is refused with an error instead of being sent —
// the cache flush is destructive to the target zone, so a wrong URL is a bug the
// operator should see, not something to swallow.
func (c Client) PurgeURL(ctx context.Context, target string) error {
	if !c.Enabled() {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return fmt.Errorf("cloudflare purge: %q is not an absolute URL", target)
	}
	if !strings.EqualFold(u.Hostname(), c.Host) {
		return fmt.Errorf("cloudflare purge: refusing %q: host %q is not the purge host %q", target, u.Hostname(), c.Host)
	}

	payload, err := json.Marshal(map[string][]string{"files": {target}})
	if err != nil {
		return fmt.Errorf("cloudflare purge: encode body: %w", err)
	}
	endpoint := strings.TrimRight(c.Endpoint, "/")
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		endpoint+"/zones/"+url.PathEscape(c.ZoneID)+"/purge_cache", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("cloudflare purge: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIToken)

	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare purge %s: %w", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, bodyLimit))
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	return fmt.Errorf("cloudflare purge %s: status %d: %s", target, resp.StatusCode, strings.TrimSpace(string(body)))
}
