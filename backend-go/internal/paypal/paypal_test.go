package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The client is the only thing in this service that talks to PayPal, so what it
// puts on the wire is pinned here: the token is fetched once and reused, an order
// asks for exactly the amount and currency it was handed, the capture's amount is
// surfaced for the caller to verify against its own order row, and a webhook is
// only accepted when PayPal itself says SUCCESS.
//
// Every case runs against a stub server rather than the sandbox so the suite is
// offline and deterministic; the credentials are fake and BaseURL points at the
// stub.

type stub struct {
	server      *httptest.Server
	tokenCalls  int
	orderBody   map[string]any
	captureHdr  http.Header
	verifyBody  map[string]any
	verifyReply string
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{verifyReply: "SUCCESS"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		s.tokenCalls++
		if id, secret, ok := r.BasicAuth(); !ok || id != "client-id" || secret != "client-secret" {
			t.Errorf("token request carried wrong basic auth: %q %q %v", id, secret, ok)
		}
		writeJSON(w, map[string]any{"access_token": "token-" + strings.Repeat("x", 4), "expires_in": 3600})
	})
	mux.HandleFunc("/v2/checkout/orders", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &s.orderBody)
		writeJSON(w, map[string]any{
			"id":     "ORDER-1",
			"status": "CREATED",
			"links": []map[string]string{
				{"rel": "self", "href": "https://api.test/v2/checkout/orders/ORDER-1"},
				{"rel": "approve", "href": "https://www.sandbox.paypal.com/checkoutnow?token=ORDER-1"},
			},
		})
	})
	mux.HandleFunc("/v2/checkout/orders/ORDER-1/capture", func(w http.ResponseWriter, r *http.Request) {
		s.captureHdr = r.Header.Clone()
		writeJSON(w, map[string]any{
			"id":     "ORDER-1",
			"status": "COMPLETED",
			"purchase_units": []map[string]any{{
				"payments": map[string]any{
					"captures": []map[string]any{{
						"id":     "CAPTURE-1",
						"status": "COMPLETED",
						"amount": map[string]string{"value": "29.00", "currency_code": "USD"},
					}},
				},
			}},
		})
	})
	mux.HandleFunc("/v1/notifications/verify-webhook-signature", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &s.verifyBody)
		writeJSON(w, map[string]any{"verification_status": s.verifyReply})
	})
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *stub) client() *Client {
	return New(Config{ClientID: "client-id", ClientSecret: "client-secret", BaseURL: s.server.URL})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func TestCreateOrderSendsTheAmountItWasGiven(t *testing.T) {
	s := newStub(t)
	order, err := s.client().CreateOrder(context.Background(), CreateOrderInput{
		Plan: "pro", Amount: "29.00", Currency: "USD", UserID: 7,
		ReturnURL: "https://cs.example/billing?paypal=return", CancelURL: "https://cs.example/billing?paypal=cancel",
		BrandName: "RelayChat",
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.ID != "ORDER-1" || order.ApproveURL != "https://www.sandbox.paypal.com/checkoutnow?token=ORDER-1" {
		t.Fatalf("order = %+v, want ORDER-1 with the approve link", order)
	}
	units, _ := s.orderBody["purchase_units"].([]any)
	if len(units) != 1 {
		t.Fatalf("purchase_units = %#v, want exactly one", s.orderBody["purchase_units"])
	}
	unit, _ := units[0].(map[string]any)
	if unit["reference_id"] != "pro" || unit["custom_id"] != "7" {
		t.Errorf("plan/tenant missing from the order: %#v", unit)
	}
	amount, _ := unit["amount"].(map[string]any)
	if amount["value"] != "29.00" || amount["currency_code"] != "USD" {
		t.Errorf("amount = %#v, want 29.00 USD", amount)
	}
	if intent, _ := s.orderBody["intent"].(string); intent != "CAPTURE" {
		t.Errorf("intent = %q, want CAPTURE", intent)
	}
}

// The approve link is where the buyer is sent, so a tampered or redirected
// response must not be able to point checkout at a phishing page: only PayPal's
// own hosts are accepted, and only for the configured mode.
func TestCreateOrderRejectsNonPayPalApproveLink(t *testing.T) {
	for _, link := range []string{
		"https://paypal.test/approve?token=ORDER-1",
		"http://www.sandbox.paypal.com/checkoutnow?token=ORDER-1",
		"https://paypal.com.evil.test/checkoutnow?token=ORDER-1",
		"https://www.paypal.com/checkoutnow?token=ORDER-1", // live host, sandbox mode
		"",
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"access_token": "t", "expires_in": 3600})
		})
		mux.HandleFunc("/v2/checkout/orders", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"id": "ORDER-1", "status": "CREATED",
				"links": []map[string]string{{"rel": "approve", "href": link}}})
		})
		srv := httptest.NewServer(mux)
		_, err := New(Config{ClientID: "i", ClientSecret: "s", BaseURL: srv.URL}).CreateOrder(context.Background(),
			CreateOrderInput{Plan: "pro", Amount: "1.00", Currency: "USD"})
		srv.Close()
		if err == nil {
			t.Errorf("CreateOrder accepted approve link %q", link)
		}
	}
}

// A create-order call without an approve link would leave the console with
// nothing to redirect to; the client must treat that as a failure.
func TestCreateOrderRequiresAnApproveLink(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"access_token": "t", "expires_in": 3600})
	})
	mux.HandleFunc("/v2/checkout/orders", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": "ORDER-1", "status": "CREATED"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, err := New(Config{ClientID: "i", ClientSecret: "s", BaseURL: srv.URL}).CreateOrder(context.Background(), CreateOrderInput{Plan: "pro", Amount: "1.00", Currency: "USD"})
	if err == nil {
		t.Fatal("CreateOrder returned nil error for a response with no approve link")
	}
}

func TestAccessTokenIsFetchedOnce(t *testing.T) {
	s := newStub(t)
	c := s.client()
	for i := 0; i < 3; i++ {
		if _, err := c.CreateOrder(context.Background(), CreateOrderInput{Plan: "pro", Amount: "1.00", Currency: "USD", UserID: 1}); err != nil {
			t.Fatalf("CreateOrder #%d: %v", i, err)
		}
	}
	if s.tokenCalls != 1 {
		t.Errorf("token fetched %d times, want 1 (a burst must share one token)", s.tokenCalls)
	}
}

func TestCaptureOrderSurfacesAmountAndIsProviderIdempotent(t *testing.T) {
	s := newStub(t)
	capture, err := s.client().CaptureOrder(context.Background(), "ORDER-1")
	if err != nil {
		t.Fatalf("CaptureOrder: %v", err)
	}
	if capture.CaptureID != "CAPTURE-1" || capture.Amount != "29.00" || capture.Currency != "USD" {
		t.Fatalf("capture = %+v, want CAPTURE-1 / 29.00 USD", capture)
	}
	// PayPal-Request-Id is what makes a retry after a timeout return the same
	// capture instead of charging twice.
	if got := s.captureHdr.Get("PayPal-Request-Id"); got != "ORDER-1" {
		t.Errorf("PayPal-Request-Id = %q, want the order id", got)
	}
}

func TestVerifyWebhookRequiresPayPalApproval(t *testing.T) {
	s := newStub(t)
	headers := http.Header{}
	headers.Set("Paypal-Auth-Algo", "SHA256withRSA")
	headers.Set("Paypal-Cert-Url", "https://api.test/cert")
	headers.Set("Paypal-Transmission-Id", "tid")
	headers.Set("Paypal-Transmission-Sig", "sig")
	headers.Set("Paypal-Transmission-Time", "2026-10-05T00:00:00Z")
	event := []byte(`{"event_type":"PAYMENT.CAPTURE.COMPLETED"}`)

	if err := s.client().VerifyWebhook(context.Background(), "WH-1", headers, event); err != nil {
		t.Fatalf("VerifyWebhook with SUCCESS: %v", err)
	}
	if s.verifyBody["webhook_id"] != "WH-1" {
		t.Errorf("verification sent webhook_id %#v, want WH-1", s.verifyBody["webhook_id"])
	}
	if _, ok := s.verifyBody["webhook_event"].(map[string]any); !ok {
		t.Errorf("verification did not forward the event body: %#v", s.verifyBody["webhook_event"])
	}

	s.verifyReply = "FAILURE"
	if err := s.client().VerifyWebhook(context.Background(), "WH-1", headers, event); err == nil {
		t.Fatal("VerifyWebhook accepted a FAILURE verification")
	}
}

// A webhook without PayPal's headers must be refused locally: asking PayPal to
// verify an empty signature only wastes a round trip.
func TestVerifyWebhookRejectsMissingHeadersWithoutCallingPayPal(t *testing.T) {
	s := newStub(t)
	s.verifyReply = "SUCCESS"
	err := s.client().VerifyWebhook(context.Background(), "WH-1", http.Header{}, []byte(`{}`))
	if err == nil {
		t.Fatal("VerifyWebhook accepted an unsigned event")
	}
	if s.verifyBody != nil {
		t.Error("VerifyWebhook called PayPal for an event with no signature headers")
	}
	// No webhook id configured is a configuration error, not a verification
	// failure: the caller must never accept the event in that state.
	if err := s.client().VerifyWebhook(context.Background(), "", http.Header{}, []byte(`{}`)); err == nil {
		t.Fatal("VerifyWebhook accepted an event with no configured webhook id")
	}
}

func TestUnconfiguredClientRefusesToCall(t *testing.T) {
	c := New(Config{})
	if c.Enabled() {
		t.Fatal("Enabled() true without credentials")
	}
	if _, err := c.CreateOrder(context.Background(), CreateOrderInput{Plan: "pro", Amount: "1.00", Currency: "USD"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("CreateOrder error = %v, want ErrNotConfigured", err)
	}
}

// Mode picks the host, and a typo must never reach the live API.
func TestBaseURLFollowsMode(t *testing.T) {
	cases := map[string]string{"": SandboxBaseURL, "sandbox": SandboxBaseURL, "LIVE": LiveBaseURL, "live": LiveBaseURL, "production": SandboxBaseURL}
	for mode, want := range cases {
		if got := New(Config{Mode: mode}).baseURL(); got != want {
			t.Errorf("mode %q → %q, want %q", mode, got, want)
		}
	}
	if got := New(Config{Mode: "live", BaseURL: "http://127.0.0.1:1/"}).baseURL(); got != "http://127.0.0.1:1" {
		t.Errorf("BaseURL override ignored: %q", got)
	}
}
