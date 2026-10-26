package khqrpay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The client is the only thing in this service that talks to the KHQRPay
// gateway, and three of its properties are money-path properties: what it signs,
// which of the two documented response shapes it understands, and that nothing
// it returns can leak the secret or the signature into a log line. All three are
// testable without a network or a database, so they are pinned here.

const (
	testSecret  = "s3cr3t"
	testAmount  = "15.00"
	testTx      = "MC-7-1700000000-AB12"
	testSuccess = "https://cs.example/billing?khqr=return"
)

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	return New(Config{ProfileID: "PROF1", Secret: testSecret, BaseURL: baseURL, Currency: "USD"})
}

// stubGateway answers every request with a body and records the one it received.
type stubGateway struct {
	server  *httptest.Server
	request *http.Request
	form    url.Values
	body    string
}

func newStubGateway(t *testing.T, status int, response string) *stubGateway {
	t.Helper()
	g := &stubGateway{body: response}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.request = r.Clone(context.Background())
		raw, _ := io.ReadAll(r.Body)
		g.form, _ = url.ParseQuery(string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(g.body))
	}))
	t.Cleanup(g.server.Close)
	return g
}

// This pins the signature construction, not just its format: the gateway
// re-derives the hash from the fields it receives, so the ORDER of the four
// concatenated fields decides whether a correct order is accepted. The digest
// below is sha1("s3cr3t" + tx + "15.00" + success_url + remark) computed once,
// off-line, with the remark the production handler actually sends.
func TestCreateQRSignsTheDocumentedFieldsInOrder(t *testing.T) {
	g := newStubGateway(t, 200, `{"responseCode":0,"responseMessage":"Success","data":{"data":{"qr":"KHQR-PAYLOAD","qr_url":"https://cdn.example/q.png","req_time":1710500000}}}`)
	client := newTestClient(t, g.server.URL)

	const remark = "MekongChat pro 30-day plan"
	qr, err := client.CreateQR(context.Background(), CreateInput{
		TransactionID: testTx,
		Amount:        testAmount,
		SuccessURL:    testSuccess,
		Remark:        remark,
	})
	if err != nil {
		t.Fatalf("CreateQR: %v", err)
	}
	if qr.QR != "KHQR-PAYLOAD" || qr.QRURL != "https://cdn.example/q.png" {
		t.Errorf("qr = %q / %q, want the payload from data.data", qr.QR, qr.QRURL)
	}
	// data.req_time is the vendor's own clock; it goes into payments.detail so a
	// dispute can be lined up with their logs.
	if qr.ReqTime != 1710500000 {
		t.Errorf("req_time = %d, want 1710500000", qr.ReqTime)
	}
	// The verified route: /api prefix and `qr-api`, not the marketing page's
	// `qr-api-khqr` without it.
	if got, want := g.request.URL.Path, "/api/PROF1/payment-gateway/v1/payments/qr-api"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got := g.request.Method; got != http.MethodPost {
		t.Errorf("method = %q, want POST", got)
	}
	if got := g.request.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Errorf("content type = %q", got)
	}
	want := map[string]string{
		"transaction_id": testTx,
		"amount":         testAmount,
		"success_url":    testSuccess,
		"remark":         remark,
		"hash":           "2e5bc0f12f0748ab8c9a6eeac2d4b56421c5172b",
	}
	for field, expected := range want {
		if got := g.form.Get(field); got != expected {
			t.Errorf("form %s = %q, want %q", field, got, expected)
		}
	}
}

// The same request with a different remark is a different signature — pinned so
// nobody "simplifies" the concatenation by trimming or dropping the field.
func TestCreateQRSignsTheRemarkWhenPresent(t *testing.T) {
	g := newStubGateway(t, 200, `{"code":0,"data":{"qr":"Q"}}`)
	client := newTestClient(t, g.server.URL)

	if _, err := client.CreateQR(context.Background(), CreateInput{
		TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess, Remark: "order 42",
	}); err != nil {
		t.Fatalf("CreateQR: %v", err)
	}
	if got, want := g.form.Get("hash"), "9aa426d946384d8567e5c8b1a9340de2889e5c2b"; got != want {
		t.Errorf("hash = %q, want %q", got, want)
	}
	if got := g.form.Get("remark"); got != "order 42" {
		t.Errorf("remark = %q, want it posted", got)
	}
}

// The real envelope: {"responseCode":0,"responseMessage":"Success","data":{…}} —
// a number 0 for success and responseMessage for the text. The older `code`
// shapes stay supported, so both forms are read; what must not survive is the
// assumption that 200 means success when responseCode is present.
func TestCreateQRReadsTheRealEnvelope(t *testing.T) {
	body := `{"responseCode":0,"responseMessage":"Success","data":{"transaction_id":"ORDER_1710500000","amount":"5.00","qr":"00020101021230620014","qr_url":"https://pay.khqr.cc/qr/abc","md5":"a1b2","req_time":1710500000,"hash":"abc123hash"}}`
	g := newStubGateway(t, 200, body)
	client := newTestClient(t, g.server.URL)
	qr, err := client.CreateQR(context.Background(), CreateInput{
		TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess, Remark: "order 42",
	})
	if err != nil {
		t.Fatalf("CreateQR: %v", err)
	}
	if qr.QR != "00020101021230620014" || qr.QRURL != "https://pay.khqr.cc/qr/abc" {
		t.Errorf("qr = %q / %q", qr.QR, qr.QRURL)
	}
	if qr.ReqTime != 1710500000 {
		t.Errorf("req_time = %d", qr.ReqTime)
	}
	// The vendor's own fields may include a `hash`; the copy kept for evidence must
	// not carry it.
	if raw, _ := json.Marshal(qr.Response); strings.Contains(string(raw), "abc123hash") {
		t.Errorf("stored response kept the vendor's hash: %s", raw)
	}

	// A rejected call reads responseCode/responseMessage — the shape the real 422
	// answered with. Before this the diagnosis was empty, because the message field
	// was named differently.
	rejected := newStubGateway(t, 422, `{"responseCode":1,"responseMessage":"Bakong Token Required: No active official Bakong OpenAPI token configured"}`)
	_, err = newTestClient(t, rejected.server.URL).CreateQR(context.Background(), CreateInput{
		TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess, Remark: "order 42",
	})
	if err == nil {
		t.Fatal("a 422 must be an error")
	}
	for _, want := range []string{"HTTP 422", "responseCode 1", "Bakong Token Required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// The public example reads data.data.qr; a flatter envelope is equally likely
// (and the detailed docs are behind a merchant login). Both must work, and a
// body with neither field must be an error rather than an empty QR shown to a
// buyer.
func TestCreateQRReadsBothDocumentedShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"nested data.data", `{"code":0,"message":"ok","data":{"data":{"qr":"PAYLOAD","qr_url":"https://cdn.example/q.png"}}}`},
		{"flat data", `{"code":0,"message":"ok","data":{"qr":"PAYLOAD","qr_url":"https://cdn.example/q.png"}}`},
		{"top level", `{"code":200,"qr":"PAYLOAD","qr_url":"https://cdn.example/q.png"}`},
		// Absent code: the payload is the evidence (spec §三.1 leaves the
		// success code open).
		{"no code field", `{"data":{"qr":"PAYLOAD","qr_url":"https://cdn.example/q.png"}}`},
		{"string success code", `{"code":"success","data":{"qr":"PAYLOAD","qr_url":"https://cdn.example/q.png"}}`},
		{"responseCode 0", `{"responseCode":0,"responseMessage":"Success","data":{"qr":"PAYLOAD","qr_url":"https://cdn.example/q.png"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newStubGateway(t, 200, tc.body)
			client := newTestClient(t, g.server.URL)
			qr, err := client.CreateQR(context.Background(), CreateInput{
				TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess, Remark: "order 42",
			})
			if err != nil {
				t.Fatalf("CreateQR: %v", err)
			}
			if qr.QR != "PAYLOAD" || qr.QRURL != "https://cdn.example/q.png" {
				t.Errorf("qr = %q / %q", qr.QR, qr.QRURL)
			}
		})
	}
}

// responseCode wins when present: the real API answers 0 on success, so a body
// carrying responseCode 1 must be rejected even if a legacy `code: 0` rides along.
func TestCreateQRRejectsANonZeroResponseCode(t *testing.T) {
	g := newStubGateway(t, 200, `{"responseCode":1,"responseMessage":"Invalid hash","code":0,"data":{"qr":"PAYLOAD"}}`)
	client := newTestClient(t, g.server.URL)
	if _, err := client.CreateQR(context.Background(), CreateInput{
		TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess, Remark: "order 42",
	}); err == nil {
		t.Fatal("responseCode 1 must be an error even with code 0 present")
	}
}

// remark is mandatory: the gateway requires it and it is inside the signature, so
// an empty one is refused here instead of posting a request that will be rejected.
func TestCreateQRRequiresARemark(t *testing.T) {
	g := newStubGateway(t, 200, `{"responseCode":0,"data":{"qr":"PAYLOAD"}}`)
	client := newTestClient(t, g.server.URL)
	for _, remark := range []string{"", "   "} {
		if _, err := client.CreateQR(context.Background(), CreateInput{
			TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess, Remark: remark,
		}); err == nil {
			t.Fatalf("remark %q was accepted; the gateway requires it", remark)
		}
	}
	if g.request != nil {
		t.Error("a request without a remark must not reach the gateway")
	}
}

// items and custom_fields are optional and unsigned; when supplied they are sent,
// custom_fields as base64 JSON (the gateway echoes it back decoded, which is what
// makes a payment match a tenant by eye).
func TestCreateQRPostsItemsAndEncodedCustomFields(t *testing.T) {
	g := newStubGateway(t, 200, `{"responseCode":0,"data":{"qr":"PAYLOAD"}}`)
	client := newTestClient(t, g.server.URL)
	if _, err := client.CreateQR(context.Background(), CreateInput{
		TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess, Remark: "order 42",
		Items:        "pro",
		CustomFields: map[string]any{"user_id": 7, "plan": "pro"},
	}); err != nil {
		t.Fatalf("CreateQR: %v", err)
	}
	if got := g.form.Get("items"); got != "pro" {
		t.Errorf("items = %q, want the plan", got)
	}
	encoded := g.form.Get("custom_fields")
	if encoded == "" {
		t.Fatal("custom_fields was not posted")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("custom_fields is not base64: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(decoded, &fields); err != nil {
		t.Fatalf("custom_fields is not JSON after decoding: %v", err)
	}
	if fields["plan"] != "pro" || fields["user_id"] != float64(7) {
		t.Errorf("custom_fields = %v", fields)
	}
	// Neither field is signed: the hash must be the same as without them.
	without := newStubGateway(t, 200, `{"responseCode":0,"data":{"qr":"PAYLOAD"}}`)
	if _, err := newTestClient(t, without.server.URL).CreateQR(context.Background(), CreateInput{
		TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess, Remark: "order 42",
	}); err != nil {
		t.Fatalf("CreateQR: %v", err)
	}
	if g.form.Get("hash") != without.form.Get("hash") {
		t.Errorf("items/custom_fields changed the signature: %q vs %q", g.form.Get("hash"), without.form.Get("hash"))
	}
}

// ============================================
// Callback signature (sha256 over the gateway's report)
// ============================================

// The documented formula, pinned off-line:
// sha256_hex("s3cr3t" + "1710500000" + tx + "5.00" + "SUCCESS").
const testCallbackHash = "f914e35bbf200b95106bac58ee388c0cb89e936b99febed99560ba854459155d"

func TestVerifyCallbackAcceptsTheDocumentedSignature(t *testing.T) {
	client := New(Config{ProfileID: "PROF1", Secret: testSecret})
	if err := client.VerifyCallback(1710500000, testTx, "5.00", "SUCCESS", testCallbackHash); err != nil {
		t.Fatalf("the documented signature was rejected: %v", err)
	}
	// Case-insensitive status, and a hex digest in either case.
	if err := client.VerifyCallback(1710500000, testTx, "5.00", "success", strings.ToUpper(testCallbackHash)); err != nil {
		t.Errorf("SUCCESS/success and hex case must not matter: %v", err)
	}
}

func TestVerifyCallbackRejectsATamperedCallback(t *testing.T) {
	client := New(Config{ProfileID: "PROF1", Secret: testSecret})
	// The "normalised amount" case is the one that matters most: the gateway signs
	// the string it sent ("5.00"), so hashing "5" must fail even though the two
	// mean the same money.
	callbacks := []struct {
		name    string
		reqTime int64
		tx      string
		amount  string
		status  string
		hash    string
	}{
		{"one character changed", 1710500000, testTx, "5.00", "SUCCESS", strings.Replace(testCallbackHash, "f", "0", 1)},
		{"normalised amount", 1710500000, testTx, "5", "SUCCESS", testCallbackHash},
		{"different amount", 1710500000, testTx, "5.01", "SUCCESS", testCallbackHash},
		{"different transaction", 1710500000, "MC-other", "5.00", "SUCCESS", testCallbackHash},
		{"different req_time", 1710500001, testTx, "5.00", "SUCCESS", testCallbackHash},
	}
	for _, tc := range callbacks {
		t.Run(tc.name, func(t *testing.T) {
			if err := client.VerifyCallback(tc.reqTime, tc.tx, tc.amount, tc.status, tc.hash); err == nil {
				t.Fatalf("%s verified against the wrong message", tc.name)
			}
		})
	}
}

func TestVerifyCallbackRefusesAnIncompleteCallback(t *testing.T) {
	client := New(Config{ProfileID: "PROF1", Secret: testSecret})
	if err := client.VerifyCallback(1710500000, testTx, "5.00", "SUCCESS", ""); err == nil {
		t.Error("a callback without hash must be refused")
	}
	if err := client.VerifyCallback(1710500000, testTx, "5.00", "SUCCESS", "   "); err == nil {
		t.Error("a blank hash must be refused")
	}
	// req_time 0 means the field was absent: there is nothing the gateway could
	// have signed over, so this is refused rather than hashed as "0".
	if err := client.VerifyCallback(0, testTx, "5.00", "SUCCESS", testCallbackHash); err == nil {
		t.Error("req_time 0 must be refused")
	}
	// A status that is not a paid word is not a payment, whatever the hash says.
	if err := client.VerifyCallback(1710500000, testTx, "5.00", "FAILED", testCallbackHash); err == nil {
		t.Error("a non-paid status must be refused")
	}
	// And an unconfigured client cannot verify anything.
	if err := New(Config{}).VerifyCallback(1710500000, testTx, "5.00", "SUCCESS", testCallbackHash); err != ErrNotConfigured {
		t.Errorf("unconfigured client err = %v, want ErrNotConfigured", err)
	}
}

func TestCreateQRRejectsEmptyPayload(t *testing.T) {
	g := newStubGateway(t, 200, `{"code":0,"message":"ok","data":{}}`)
	client := newTestClient(t, g.server.URL)
	if _, err := client.CreateQR(context.Background(), CreateInput{
		TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess,
	}); err == nil {
		t.Fatal("a response without qr/qr_url must be an error")
	}
}

// A failed create must be an error, and that error is what the handler logs — so
// it must not contain the secret or the signature, even when the gateway echoes
// the form it received back at us (which is exactly what a debugging endpoint
// does).
func TestCreateQRErrorsNeverCarryTheSecretOrTheHash(t *testing.T) {
	echoed := `{"code":1,"message":"invalid hash: ` + testSecret + ` 8d71a7b82ad1663e43d32309ea1c562ff25891e2 for ` + testSuccess + `"}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"http error with echo", 400, echoed},
		{"non-success code with echo", 200, echoed},
		{"non-json error body", 502, "<html>gateway down</html>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newStubGateway(t, tc.status, tc.body)
			client := newTestClient(t, g.server.URL)
			_, err := client.CreateQR(context.Background(), CreateInput{
				TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess,
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			msg := err.Error()
			if strings.Contains(msg, testSecret) {
				t.Errorf("error string carries the secret: %s", msg)
			}
			if strings.Contains(msg, "8d71a7b82ad1663e43d32309ea1c562ff25891e2") {
				t.Errorf("error string carries the signature: %s", msg)
			}
			// The reason must still be there: a redacted error nobody can
			// diagnose is a different way to lose a payment.
			if !strings.Contains(msg, "khqrpay:") {
				t.Errorf("error lost its reason: %s", msg)
			}
		})
	}
}

func TestTwoDecimals(t *testing.T) {
	cases := map[string]string{
		"15":     "15.00",
		"15.0":   "15.00",
		"15.00":  "15.00",
		"0.5":    "0.50",
		"0.05":   "0.05",
		"29.99":  "29.99",
		" 12.30": "12.30",
		"15.005": "15.01",
		"9.999":  "10.00",
		"0":      "0.00",
	}
	for in, want := range cases {
		got, err := twoDecimals(in)
		if err != nil {
			t.Errorf("twoDecimals(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("twoDecimals(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3", "-5", "+5", "1,5"} {
		if _, err := twoDecimals(bad); err == nil {
			t.Errorf("twoDecimals(%q) accepted a value it cannot sign", bad)
		}
	}
}

// Verify is dormant until an operator fills in KHQRPAY_VERIFY_PATH, and then it
// must call exactly that path — the endpoint is not documented, so guessing one
// would produce a confirmation that never fires.
func TestVerifyUsesTheConfiguredPath(t *testing.T) {
	g := newStubGateway(t, 200, `{"code":0,"message":"ok","data":{"transaction_id":"`+testTx+`","status":"success","amount":"15.00","currency":"USD"}}`)
	client := New(Config{ProfileID: "PROF1", Secret: testSecret, BaseURL: g.server.URL, Currency: "USD", VerifyPath: "/payments/verify"})

	if !client.VerifyConfigured() {
		t.Fatal("VerifyConfigured() = false with a path set")
	}
	result, err := client.Verify(context.Background(), testTx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got, want := g.request.URL.Path, "/PROF1/payments/verify"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got := g.request.URL.Query().Get("transaction_id"); got != testTx {
		t.Errorf("transaction_id = %q", got)
	}
	if got := g.request.URL.Query().Get("hash"); got != sha1Hex(testSecret+testTx) {
		t.Errorf("verify hash = %q, want the id-and-secret signature", got)
	}
	if !result.Paid {
		t.Errorf("status %q should read as paid", result.Status)
	}
	if result.Amount != "15.00" || result.Currency != "USD" {
		t.Errorf("amount/currency = %q/%q", result.Amount, result.Currency)
	}
}

func TestVerifyRefusesToGuessAnEndpoint(t *testing.T) {
	client := New(Config{ProfileID: "PROF1", Secret: testSecret, Currency: "USD"})
	if client.VerifyConfigured() {
		t.Fatal("VerifyConfigured() = true with no path")
	}
	if _, err := client.Verify(context.Background(), testTx); err != ErrVerifyNotConfigured {
		t.Errorf("Verify err = %v, want ErrVerifyNotConfigured", err)
	}
}

// An unrecognised status word must never read as paid: the endpoint's vocabulary
// is not documented, and "true by default" on a money path grants plans for free.
func TestVerifyUnknownStatusIsNotPaid(t *testing.T) {
	for _, status := range []string{"pending", "processing", "", "SUCCESS "} {
		g := newStubGateway(t, 200, `{"code":0,"data":{"status":"`+status+`"}}`)
		client := New(Config{ProfileID: "PROF1", Secret: testSecret, BaseURL: g.server.URL, VerifyPath: "/verify"})
		result, err := client.Verify(context.Background(), testTx)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		want := strings.EqualFold(strings.TrimSpace(status), "success")
		if result.Paid != want {
			t.Errorf("status %q → Paid = %v, want %v", status, result.Paid, want)
		}
	}
}

// The response stored in payments.detail is evidence for a disputed QR; a
// signature must not be part of it.
func TestStoredResponseDropsSigningMaterial(t *testing.T) {
	g := newStubGateway(t, 200, `{"code":0,"data":{"qr":"Q","hash":"abc123","nested":{"secret":"s","keep":"me"}}}`)
	client := newTestClient(t, g.server.URL)
	qr, err := client.CreateQR(context.Background(), CreateInput{
		TransactionID: testTx, Amount: testAmount, SuccessURL: testSuccess, Remark: "order 42",
	})
	if err != nil {
		t.Fatalf("CreateQR: %v", err)
	}
	raw, err := json.Marshal(qr.Response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(raw), "abc123") || strings.Contains(string(raw), `"secret":"s"`) {
		t.Errorf("stored response kept signing material: %s", raw)
	}
	if !strings.Contains(string(raw), "keep") {
		t.Errorf("stored response lost the vendor's payload: %s", raw)
	}
}

// Enabled gates every call: without both credentials nothing may be attempted,
// and the caller answers "not configured" instead.
func TestEnabledNeedsBothCredentials(t *testing.T) {
	if (New(Config{})).Enabled() {
		t.Error("empty config reported as enabled")
	}
	if (New(Config{ProfileID: "PROF1"})).Enabled() {
		t.Error("profile id without secret reported as enabled")
	}
	if (New(Config{Secret: testSecret})).Enabled() {
		t.Error("secret without profile id reported as enabled")
	}
	if _, err := New(Config{}).CreateQR(context.Background(), CreateInput{TransactionID: testTx, Amount: testAmount}); err != ErrNotConfigured {
		t.Errorf("CreateQR err = %v, want ErrNotConfigured", err)
	}
}

// ============================================
// KHQRPAY_CREATE_PATH — the route the vendor actually serves
// ============================================
//
// The marketing page's documented create path answered 404 on the real gateway
// (probed 2026-10-26), so the path is now an environment variable. What must not
// change is the URL an unconfigured deployment sends: an existing installation
// has to keep behaving byte for byte.

// With no CreatePath the built-in template is used, and the result is pinned as a
// literal — the whole point of the default branch is that an unconfigured
// deployment sends exactly the route the logged-in documentation describes.
func TestCreatePathDefaultsToTheBuiltInTemplate(t *testing.T) {
	client := New(Config{ProfileID: "PROF1", Secret: testSecret})
	if got, want := client.createEndpoint(), "https://anajakpay.com/api/PROF1/payment-gateway/v1/payments/qr-api"; got != want {
		t.Errorf("default create URL = %q, want %q", got, want)
	}
	// BASE_URL still owns the host, and only the host.
	withHost := New(Config{ProfileID: "PROF1", Secret: testSecret, BaseURL: "https://sandbox.example/"})
	if got, want := withHost.createEndpoint(), "https://sandbox.example/api/PROF1/payment-gateway/v1/payments/qr-api"; got != want {
		t.Errorf("create URL with a custom host = %q, want %q", got, want)
	}
}

// A configured path replaces the whole path — no profile segment is invented for
// it, because the operator's route may not have a place for one.
func TestCreatePathReplacesTheWholePath(t *testing.T) {
	client := New(Config{
		ProfileID:  "PROF1",
		Secret:     testSecret,
		BaseURL:    "https://khqr.cc",
		CreatePath: "/api/payments/qr-api-khqr",
	})
	got, want := client.createEndpoint(), "https://khqr.cc/api/payments/qr-api-khqr"
	if got != want {
		t.Fatalf("custom create URL = %q, want %q", got, want)
	}
	if strings.Contains(got, "PROF1") {
		t.Errorf("a path without {profile} must not grow a profile segment: %q", got)
	}
	// And the verify endpoint keeps its own, profile-prefixed shape: customizing one
	// route must never move the other.
	verify := New(Config{ProfileID: "PROF1", Secret: testSecret, BaseURL: "https://khqr.cc", CreatePath: "/api/payments/qr-api-khqr", VerifyPath: "/payments/verify"})
	if got, want := verify.endpoint("/payments/verify"), "https://khqr.cc/PROF1/payments/verify"; got != want {
		t.Errorf("verify URL moved with the create path: %q, want %q", got, want)
	}
}

// {profile} is substituted wherever it appears, and the merchant id is escaped:
// a profile id is configuration, but it is still interpolated into a URL.
func TestCreatePathExpandsEveryProfilePlaceholder(t *testing.T) {
	client := New(Config{
		ProfileID:  "prof 1/2",
		Secret:     testSecret,
		BaseURL:    "https://khqr.cc",
		CreatePath: "/{profile}/gateway/{profile}/qr",
	})
	got, want := client.createEndpoint(), "https://khqr.cc/prof%201%2F2/gateway/prof%201%2F2/qr"
	if got != want {
		t.Errorf("expanded create URL = %q, want %q", got, want)
	}
	if strings.Contains(got, ProfilePlaceholder) {
		t.Errorf("a {profile} placeholder survived into the URL: %q", got)
	}
}

// Padding around a pasted path, and a missing leading slash, must not silently
// produce a broken URL: "https://khqr.ccapi/…" is a DNS failure nobody can read.
func TestCreatePathIsTrimmedAndRooted(t *testing.T) {
	client := New(Config{
		ProfileID:  "PROF1",
		Secret:     testSecret,
		BaseURL:    "https://khqr.cc",
		CreatePath: "  api/payments/qr-api-khqr  ",
	})
	if got, want := client.createEndpoint(), "https://khqr.cc/api/payments/qr-api-khqr"; got != want {
		t.Errorf("trimmed create URL = %q, want %q", got, want)
	}
	blank := New(Config{ProfileID: "PROF1", Secret: testSecret, CreatePath: "   "})
	if got, want := blank.createEndpoint(), "https://anajakpay.com/api/PROF1/payment-gateway/v1/payments/qr-api"; got != want {
		t.Errorf("whitespace-only CreatePath must fall back to the built-in template: %q, want %q", got, want)
	}
}
