// Package paypal is the smallest client that can take money through a PayPal
// business account: an OAuth2 token, creating a CAPTURE order, capturing it, and
// verifying a webhook signature.
//
// It deliberately does not model PayPal Billing Plans / subscriptions yet: the
// first iteration sells one 30-day period at a time, which is exactly the cycle
// tenant_billing already keeps, and a subscription can be added later without
// changing the shape of this API.
//
// Nothing here reads the environment — the caller passes a Config, so tests aim
// BaseURL at an httptest server and internal/config owns the env plumbing.
package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"khmer-ai-cs-go/internal/textutil"
)

const (
	SandboxBaseURL = "https://api-m.sandbox.paypal.com"
	LiveBaseURL    = "https://api-m.paypal.com"

	// A capture PayPal accepted answers in a second or two; anything beyond this
	// is a hung connection, not a slow payment.
	requestTimeout = 30 * time.Second

	// Refresh the token a minute before it expires so a capture never races it.
	tokenSkew = time.Minute

	// Error bodies are PayPal's own text; keep enough to diagnose, not the whole
	// payload.
	maxErrorBody = 400
)

// ErrNotConfigured means the deployment has no PayPal credentials, so every call
// would fail: callers check Enabled() first and show "contact us" instead.
var ErrNotConfigured = errors.New("paypal: client id/secret not configured")

type Config struct {
	ClientID     string
	ClientSecret string
	// Mode is "sandbox" or "live"; anything else (including empty) means sandbox,
	// so a typo can never point a test deployment at the live API by accident.
	Mode string
	// BaseURL overrides the mode-derived host. Tests set it; production leaves it
	// empty.
	BaseURL string
}

type Client struct {
	cfg  Config
	http *http.Client

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: requestTimeout}}
}

// Enabled reports whether credentials are present. A half-configured pair is a
// deployment error and internal/config refuses to boot on it; this stays false
// so an unconfigured deployment simply cannot sell anything.
func (c *Client) Enabled() bool {
	return strings.TrimSpace(c.cfg.ClientID) != "" && strings.TrimSpace(c.cfg.ClientSecret) != ""
}

func (c *Client) baseURL() string {
	if custom := strings.TrimRight(strings.TrimSpace(c.cfg.BaseURL), "/"); custom != "" {
		return custom
	}
	if strings.EqualFold(strings.TrimSpace(c.cfg.Mode), "live") {
		return LiveBaseURL
	}
	return SandboxBaseURL
}

// CreateOrderInput is what the checkout page asked for, already resolved to a
// price by the caller (prices come from configuration, not from the browser).
type CreateOrderInput struct {
	Plan      string
	Amount    string // decimal string, e.g. "29.00"
	Currency  string
	UserID    int32
	ReturnURL string
	CancelURL string
	BrandName string
}

type Order struct {
	ID         string
	Status     string
	ApproveURL string
}

// Capture is the part of PayPal's capture response this service acts on. Amount
// and Currency are what the buyer actually paid — the caller compares them with
// the price it intended to charge before granting anything.
type Capture struct {
	OrderID   string
	CaptureID string
	Status    string
	Amount    string
	Currency  string
}

func (c *Client) CreateOrder(ctx context.Context, in CreateOrderInput) (Order, error) {
	body := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{{
			// reference_id and custom_id both carry the source of truth for the
			// reconciliation: which plan, and which tenant paid for it.
			"reference_id": in.Plan,
			"custom_id":    strconv.FormatInt(int64(in.UserID), 10),
			"description":  "RelayChat " + in.Plan + " plan (30 days)",
			"amount": map[string]string{
				"currency_code": in.Currency,
				"value":         in.Amount,
			},
		}},
		"application_context": map[string]any{
			"brand_name":  in.BrandName,
			"user_action": "PAY_NOW",
			"return_url":  in.ReturnURL,
			"cancel_url":  in.CancelURL,
		},
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Links  []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if err := c.do(ctx, http.MethodPost, "/v2/checkout/orders", body, &out, nil); err != nil {
		return Order{}, err
	}
	order := Order{ID: out.ID, Status: out.Status}
	for _, link := range out.Links {
		if link.Rel == "approve" {
			order.ApproveURL = link.Href
		}
	}
	if order.ID == "" || order.ApproveURL == "" {
		return Order{}, errors.New("paypal: create order returned no id or approve link")
	}
	if !c.isPayPalURL(order.ApproveURL) {
		// The approve link is where the buyer gets sent. It arrives over a
		// connection we authenticated, but a redirected or tampered response must
		// not be able to point checkout at a phishing page — the host is pinned to
		// PayPal, mode-aware, and the console checks it again before navigating.
		return Order{}, fmt.Errorf("paypal: approve link is not a PayPal URL: %s", textutil.Ellipsize(strings.TrimSpace(order.ApproveURL), 120))
	}
	return order, nil
}

// isPayPalURL pins an approve link to PayPal's own hosts. Live and sandbox use
// different hosts on purpose: a deployment must not accept the other mode's link,
// or a wrong PAYPAL_MODE would silently change where buyers are sent.
func (c *Client) isPayPalURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if strings.EqualFold(strings.TrimSpace(c.cfg.Mode), "live") {
		return host == "paypal.com" || host == "www.paypal.com"
	}
	return host == "sandbox.paypal.com" || host == "www.sandbox.paypal.com"
}

func (c *Client) CaptureOrder(ctx context.Context, orderID string) (Capture, error) {
	var out struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		PurchaseUnits []struct {
			Payments struct {
				Captures []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Amount struct {
						Value        string `json:"value"`
						CurrencyCode string `json:"currency_code"`
					} `json:"amount"`
				} `json:"captures"`
			} `json:"payments"`
		} `json:"purchase_units"`
	}
	// PayPal-Request-Id makes the capture idempotent provider-side: a retry after
	// a timeout returns the original capture instead of charging twice.
	headers := map[string]string{"PayPal-Request-Id": orderID}
	if err := c.do(ctx, http.MethodPost, "/v2/checkout/orders/"+url.PathEscape(orderID)+"/capture", map[string]any{}, &out, headers); err != nil {
		return Capture{}, err
	}
	capture := Capture{OrderID: out.ID, Status: out.Status}
	if len(out.PurchaseUnits) > 0 && len(out.PurchaseUnits[0].Payments.Captures) > 0 {
		first := out.PurchaseUnits[0].Payments.Captures[0]
		capture.CaptureID = first.ID
		capture.Amount = first.Amount.Value
		capture.Currency = first.Amount.CurrencyCode
		if capture.Status == "" {
			capture.Status = first.Status
		}
	}
	return capture, nil
}

// VerifyWebhook asks PayPal whether a webhook really came from PayPal for the
// configured webhook id. A webhook route that skips this is an open endpoint
// that grants plans to anyone who can POST to it.
func (c *Client) VerifyWebhook(ctx context.Context, webhookID string, headers http.Header, event []byte) error {
	if strings.TrimSpace(webhookID) == "" {
		return errors.New("paypal: no webhook id configured")
	}
	var payload struct {
		AuthAlgo         string          `json:"auth_algo"`
		CertURL          string          `json:"cert_url"`
		TransmissionID   string          `json:"transmission_id"`
		TransmissionSig  string          `json:"transmission_sig"`
		TransmissionTime string          `json:"transmission_time"`
		WebhookID        string          `json:"webhook_id"`
		WebhookEvent     json.RawMessage `json:"webhook_event"`
	}
	payload.AuthAlgo = headers.Get("Paypal-Auth-Algo")
	payload.CertURL = headers.Get("Paypal-Cert-Url")
	payload.TransmissionID = headers.Get("Paypal-Transmission-Id")
	payload.TransmissionSig = headers.Get("Paypal-Transmission-Sig")
	payload.TransmissionTime = headers.Get("Paypal-Transmission-Time")
	payload.WebhookID = webhookID
	payload.WebhookEvent = json.RawMessage(event)
	for name, value := range map[string]string{
		"auth_algo": payload.AuthAlgo, "cert_url": payload.CertURL, "transmission_id": payload.TransmissionID,
		"transmission_sig": payload.TransmissionSig, "transmission_time": payload.TransmissionTime,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("paypal: webhook header %s is missing", name)
		}
	}
	var out struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/notifications/verify-webhook-signature", payload, &out, nil); err != nil {
		return err
	}
	if out.VerificationStatus != "SUCCESS" {
		return fmt.Errorf("paypal: webhook verification failed (%s)", out.VerificationStatus)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any, headers map[string]string) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	var reader io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("paypal: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, reader)
	if err != nil {
		return fmt.Errorf("paypal: build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("paypal %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return fmt.Errorf("paypal %s %s: read response: %w", method, path, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("paypal %s %s: HTTP %d: %s", method, path, resp.StatusCode, textutil.Ellipsize(strings.TrimSpace(string(raw)), maxErrorBody))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("paypal %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// accessToken caches the OAuth2 client-credentials token. The lock is held
// across the request on purpose: a burst of captures should fetch one token, not
// one per caller.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	if !c.Enabled() {
		return "", ErrNotConfigured
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("paypal: build token request: %w", err)
	}
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("paypal: token request: %w", err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return "", fmt.Errorf("paypal: read token response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The response never contains our secret, but the status is the whole
		// diagnosis for a bad client id (401 invalid_client).
		return "", fmt.Errorf("paypal: token request: HTTP %d: %s", resp.StatusCode, textutil.Ellipsize(strings.TrimSpace(string(raw)), maxErrorBody))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("paypal: decode token: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("paypal: token response carried no access_token")
	}
	c.token = out.AccessToken
	lifetime := time.Duration(out.ExpiresIn)*time.Second - tokenSkew
	if lifetime <= 0 {
		lifetime = time.Minute
	}
	c.tokenExpiry = time.Now().Add(lifetime)
	return c.token, nil
}
