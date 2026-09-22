// Package storager2 is a dependency-free Cloudflare R2 client (S3-compatible):
// SigV4-signed object PUT plus query-signed (presigned) GET URLs. It backs
// inbound platform media replay in the inbox and outbound TTS/voice delivery.
package storager2

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	region        = "auto"
	service       = "s3"
	amzDateFormat = "20060102T150405Z"
	dateFormat    = "20060102"
)

// Client talks to one R2 bucket.
type Client struct {
	AccountID string
	AccessKey string
	SecretKey string
	Bucket    string
	PublicURL string

	http *http.Client
}

// New builds a client; Enabled() reports whether it can be used.
func New(accountID, accessKey, secretKey, bucket, publicURL string) *Client {
	return &Client{
		AccountID: accountID, AccessKey: accessKey, SecretKey: secretKey,
		Bucket: bucket, PublicURL: strings.TrimRight(publicURL, "/"),
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// Enabled reports whether credentials + bucket are present.
func (c *Client) Enabled() bool {
	return c != nil && c.AccountID != "" && c.AccessKey != "" && c.SecretKey != "" && c.Bucket != ""
}

func (c *Client) endpoint(key string) string {
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com/%s/%s",
		c.AccountID, url.PathEscape(c.Bucket), escapeKeyPath(key))
}

// escapeKeyPath percent-encodes each path segment per SigV4 rules (RFC 3986,
// space → %20, keep unreserved [A-Za-z0-9-._~] and '/').
func escapeKeyPath(key string) string {
	segments := strings.Split(key, "/")
	for i, s := range segments {
		var b strings.Builder
		for _, ch := range []byte(s) {
			switch {
			case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
				ch == '-', ch == '.', ch == '_', ch == '~':
				b.WriteByte(ch)
			default:
				fmt.Fprintf(&b, "%%%02X", ch)
			}
		}
		segments[i] = b.String()
	}
	return strings.Join(segments, "/")
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// PutObject uploads one object (SigV4 header signing).
func (c *Client) PutObject(ctx context.Context, key string, data []byte, contentType string) error {
	if !c.Enabled() {
		return fmt.Errorf("r2 not configured")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	u := c.endpoint(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	amzDate := time.Now().UTC().Format(amzDateFormat)
	dateStamp := time.Now().UTC().Format(dateFormat)
	payloadHash := sha256.Sum256(data)
	hexPayload := hex.EncodeToString(payloadHash[:])

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", hexPayload)

	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "content-type:" + contentType + "\n" +
		"host:" + req.URL.Host + "\n" +
		"x-amz-content-sha256:" + hexPayload + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalRequest := strings.Join([]string{
		"PUT", req.URL.Path, "", canonicalHeaders, signedHeaders, hexPayload,
	}, "\n")

	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	crHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(crHash[:]),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+c.SecretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKey, scope, signedHeaders, signature))

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("r2 put: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("r2 put %s failed (%d): %s", key, resp.StatusCode, string(body))
	}
	return nil
}

// PresignedGET returns a temporary HTTPS URL that downloads the object.
func (c *Client) PresignedGET(key string, ttl time.Duration) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("r2 not configured")
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	now := time.Now().UTC()
	amzDate := now.Format(amzDateFormat)
	dateStamp := now.Format(dateFormat)
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	host := fmt.Sprintf("%s.r2.cloudflarestorage.com", c.AccountID)
	path := "/" + url.PathEscape(c.Bucket) + "/" + escapeKeyPath(key)

	query := url.Values{}
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", c.AccessKey+"/"+scope)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", fmt.Sprintf("%.0f", ttl.Seconds()))
	query.Set("X-Amz-SignedHeaders", "host")

	canonicalQuery := query.Encode() // url.Values.Encode sorts keys — required.
	canonicalRequest := strings.Join([]string{
		"GET", path, canonicalQuery, "host:" + host + "\n", "host", "UNSIGNED-PAYLOAD",
	}, "\n")
	crHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(crHash[:]),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+c.SecretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	return fmt.Sprintf("https://%s%s?%s&X-Amz-Signature=%s", host, path, canonicalQuery, signature), nil
}

// PublicOrPresigned prefers the bucket public URL (permanent, for provider
// side downloads), falling back to a presigned GET.
//
// Only use this for objects that are inherently publishable (a merchant's own
// avatar, a customer photo the tenant is meant to display). It ignores ttl
// whenever PublicURL is configured, so the result never expires.
func (c *Client) PublicOrPresigned(key string, ttl time.Duration) string {
	if c.PublicURL != "" {
		return c.PublicURL + "/" + escapeKeyPath(key)
	}
	u, err := c.PresignedGET(key, ttl)
	if err != nil {
		return ""
	}
	return u
}

// PresignedOnly returns an expiring presigned URL and never falls back to the
// permanent public host. Use this for tenant-private objects — synthesized
// voice replies, inbound customer media, transcripts — where the requested TTL
// is part of the access grant: the public branch of PublicOrPresigned discards
// that TTL, and the bucket's public host is not a per-object ACL.
func (c *Client) PresignedOnly(key string, ttl time.Duration) string {
	u, err := c.PresignedGET(key, ttl)
	if err != nil {
		return ""
	}
	return u
}
