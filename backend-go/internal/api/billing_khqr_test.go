package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/khqrpay"
	"khmer-ai-cs-go/internal/usage"
)

// The KHQR channel's decisions that do not need a database: the order id the
// webhook will have to find, the four states the console renders, what the
// callback's amount is compared against, and the two gates that stand between a
// stranger and a paid plan. Every one of them is the kind of check that quietly
// grants a plan — or quietly loses a payment — when it goes wrong.

// The webhook has nothing but this string to locate the row, so its shape is a
// contract (spec §四.2): [A-Za-z0-9-], ≤ 40 characters, and it carries the tenant
// and the second so two orders are distinguishable by eye in a log.
func TestKHQRTransactionIDFormat(t *testing.T) {
	now := time.Unix(1700000000, 0)
	pattern := regexp.MustCompile(`^MC-7-1700000000-[0-9A-Z]{4}$`)
	for i := 0; i < 50; i++ {
		id := khqrTransactionID(7, now)
		if !pattern.MatchString(id) {
			t.Fatalf("transaction id %q does not match MC-<userID>-<unix>-<4 random>", id)
		}
		if len(id) > 40 {
			t.Fatalf("transaction id %q is longer than the 40 characters the gateway accepts", id)
		}
		for _, r := range id {
			if !(r == '-' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
				t.Fatalf("transaction id %q holds a character outside [A-Za-z0-9-]", id)
			}
		}
	}
	// The suffix is what makes two orders in the same second different; over 50
	// draws from a 36^4 space a repeat would mean the generator is not random at
	// all.
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := khqrTransactionID(7, now)
		if seen[id] {
			t.Fatalf("transaction id %q generated twice in the same second", id)
		}
		seen[id] = true
	}
}

// The console knows exactly four states. Everything here comes from a row plus
// the clock, so it is decided without a database — and "expired" has to win over
// "pending" the moment the QR lapses, or the dialog polls a dead code forever.
func TestKHQRStateMapping(t *testing.T) {
	created := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	withExpiry := khqrDetail{ExpiresAt: created.Add(15 * time.Minute).Format(time.RFC3339)}
	cases := []struct {
		name   string
		status string
		detail khqrDetail
		now    time.Time
		want   string
	}{
		{"created, still payable", "created", withExpiry, created.Add(14 * time.Minute), "pending"},
		{"created, one second left", "created", withExpiry, created.Add(15*time.Minute - time.Second), "pending"},
		{"created, exactly at expiry", "created", withExpiry, created.Add(15 * time.Minute), "expired"},
		{"created, long gone", "created", withExpiry, created.Add(3 * time.Hour), "expired"},
		// A row with no detail.expires_at falls back to the documented window from
		// created_at — the shape written before this field existed.
		{"no recorded expiry, inside the window", "created", khqrDetail{}, created.Add(10 * time.Minute), "pending"},
		{"no recorded expiry, past the window", "created", khqrDetail{}, created.Add(16 * time.Minute), "expired"},
		{"captured", "captured", khqrDetail{}, created.Add(time.Hour), "paid"},
		{"failed", "failed", khqrDetail{}, created.Add(time.Minute), "failed"},
		// A state this channel does not know must never read as payable.
		{"unknown row state", "refunded", khqrDetail{}, created.Add(time.Minute), "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := khqrState(tc.status, tc.detail, created, tc.now); got != tc.want {
				t.Errorf("khqrState(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// The callback sample carries no currency, and the order row is the authority
// anyway: refusing every callback for a missing field would make the channel
// webhook-only in name.
func TestKHQRWebhookCurrencyFallsBackToTheOrder(t *testing.T) {
	if got := khqrWebhookCurrency("", "USD"); got != "USD" {
		t.Errorf("missing payload currency = %q, want the order's", got)
	}
	if got := khqrWebhookCurrency("  ", "KHR"); got != "KHR" {
		t.Errorf("blank payload currency = %q, want the order's", got)
	}
	if got := khqrWebhookCurrency("KHR", "USD"); got != "KHR" {
		t.Errorf("payload currency = %q, want the vendor's own value to be judged", got)
	}
}

// What the callback claims is compared with the row through the same amount
// rules PayPal uses: trailing zeros are formatting, the currency is exact, and
// KHR never settles a USD order.
func TestKHQRWebhookAmountIsJudgedLikeACapture(t *testing.T) {
	cases := []struct {
		name                     string
		claimed, expected        string
		claimedCurrency, ordered string
		want                     bool
	}{
		{"identical", "15.00", "15.00", "USD", "USD", true},
		{"trailing zero stripped by the vendor", "15.0", "15.00", "USD", "USD", true},
		{"integer form", "15", "15.00", "USD", "USD", true},
		{"currency case", "15.00", "15.00", "usd", "USD", true},
		{"no currency in the callback", "15.00", "15.00", "", "USD", true},
		{"underpaid", "14.99", "15.00", "USD", "USD", false},
		{"overpaid", "150.00", "15.00", "USD", "USD", false},
		{"KHR does not settle a USD order", "15.00", "15.00", "KHR", "USD", false},
		{"missing amount", "", "15.00", "USD", "USD", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			currency := khqrWebhookCurrency(tc.claimedCurrency, tc.ordered)
			if got := amountMatches(tc.claimed, tc.expected, currency, tc.ordered); got != tc.want {
				t.Errorf("amountMatches(%q, %q, %q, %q) = %v, want %v",
					tc.claimed, tc.expected, currency, tc.ordered, got, tc.want)
			}
		})
	}
}

// The callback's own status word is the only thing that makes it a payment
// signal; the vocabulary is undocumented, so anything unrecognised must not
// count (and the row is left to expire).
func TestKHQRCallbackStatusWords(t *testing.T) {
	for _, paid := range []string{"success", "SUCCESS", " paid ", "completed", "settled"} {
		if !khqrpay.IsPaidWord(paid) {
			t.Errorf("status %q should read as paid", paid)
		}
	}
	for _, notPaid := range []string{"", "pending", "processing", "failed", "canceled", "1"} {
		if khqrpay.IsPaidWord(notPaid) {
			t.Errorf("status %q must not read as paid", notPaid)
		}
	}
}

// Each rail is priced on its own: a plan may be sold through PayPal and not
// through KHQR, and free is never sold at all.
func TestKHQRPlanPrice(t *testing.T) {
	app := &App{Cfg: &config.Config{KHQRPay: config.KHQRPayConfig{
		Toggle: true, ProfileID: "p", Secret: "s",
		PricePro: " 1.50 ", PriceEnterprise: "",
	}}}
	if price, ok := app.khqrPlanPrice(usage.PlanPro); !ok || price != "1.50" {
		t.Errorf("pro price = %q, %v; want 1.50, true", price, ok)
	}
	if _, ok := app.khqrPlanPrice(usage.PlanEnterprise); ok {
		t.Error("enterprise reported as sold through KHQR with no configured price")
	}
	if _, ok := app.khqrPlanPrice(usage.PlanFree); ok {
		t.Error("free reported as sold through KHQR")
	}
	if _, ok := app.khqrPlanPrice("some-unknown-plan"); ok {
		t.Error("unknown plan reported as sold through KHQR")
	}
}

// khqrEnabled gates every route, so it has to survive all four states a
// deployment can boot in — and answer false for the half-configured ones, because
// a button that cannot create a QR is worse than no button.
func TestKHQREnabledIsFailClosed(t *testing.T) {
	var nilApp *App
	if nilApp.khqrEnabled() {
		t.Error("nil App reported as enabled")
	}
	client := khqrpay.New(khqrpay.Config{ProfileID: "p", Secret: "s"})
	if (&App{}).khqrEnabled() {
		t.Error("App without config reported as enabled")
	}
	off := &App{Cfg: &config.Config{KHQRPay: config.KHQRPayConfig{ProfileID: "p", Secret: "s"}}, KHQRPay: client}
	if off.khqrEnabled() {
		t.Error("channel reported as enabled with KHQRPAY_ENABLED unset")
	}
	noClient := &App{Cfg: &config.Config{KHQRPay: config.KHQRPayConfig{
		Toggle: true, ProfileID: "p", Secret: "s",
	}}}
	if noClient.khqrEnabled() {
		t.Error("channel reported as enabled with no client to create a QR")
	}
	ready := &App{Cfg: &config.Config{KHQRPay: config.KHQRPayConfig{
		Toggle: true, ProfileID: "p", Secret: "s",
	}}, KHQRPay: client}
	if !ready.khqrEnabled() {
		t.Error("fully configured channel reported as disabled")
	}
}

// The route gates are pinned at the source, the way the rest of this package
// pins its authorization decisions (tenant_owner_test.go scans router.go for the
// same reason): the order route is a money write and must sit behind
// tenantAdminOnly, the status route is a read that identifies its row by the
// caller's tenant, and the webhook is public by nature and therefore rate limited
// and unable to grant anything on its own.
func TestKHQRRoutesAreGuarded(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	var orderLine, statusLine, webhookLine string
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.Contains(line, "/api/v1/billing/khqr/order"):
			orderLine = strings.TrimSpace(line)
		case strings.Contains(line, "/api/v1/billing/khqr/{transaction_id}/status"):
			statusLine = strings.TrimSpace(line)
		case strings.Contains(line, "/api/v1/billing/khqr/webhook"):
			webhookLine = strings.TrimSpace(line)
		}
	}
	if orderLine == "" || statusLine == "" || webhookLine == "" {
		t.Fatalf("router.go no longer wires the three KHQR routes: order=%q status=%q webhook=%q", orderLine, statusLine, webhookLine)
	}
	if !strings.Contains(orderLine, "a.tenantAdminOnly(") {
		t.Errorf("KHQR order route is not owner-only: %s", orderLine)
	}
	if !strings.Contains(statusLine, "a.handle(a.khqrStatus)") {
		t.Errorf("KHQR status route does not reach khqrStatus: %s", statusLine)
	}
	if strings.Contains(statusLine, "a.tenantAdminOnly(") {
		// A member may watch the owner's QR be paid; the handler's ownership
		// check is what prevents reading someone else's order.
		t.Errorf("KHQR status route became owner-only; the tenant comparison belongs in the handler: %s", statusLine)
	}
	if !strings.HasPrefix(webhookLine, "mux.Handle") || !strings.Contains(webhookLine, "a.rateLimit(120)") {
		t.Errorf("KHQR webhook is not on the public group with the shared limiter: %s", webhookLine)
	}
	// The callback path must carry the unguessable token segment: the bare path
	// let any sign-in tenant open their own plan for free.
	if !strings.Contains(webhookLine, "/api/v1/billing/khqr/webhook/{token}") {
		t.Errorf("KHQR webhook route lost its path token: %s", webhookLine)
	}
	// The status read must compare the row's owner with the caller's tenant, not
	// trust the transaction id.
	status := funcBody(t, "billing_khqr.go", "func (a *App) khqrStatus")
	if !strings.Contains(status, "owner != user.Tenant()") {
		t.Error("khqrStatus must compare the payment row's user_id with the caller's tenant")
	}
	// Reads survive the kill switch (see TestKHQRSettlementOutlivesTheKillSwitch):
	// only issuing a new QR is gated on it.
	if strings.Contains(status, "khqrEnabled()") {
		t.Error("khqrStatus reads the kill switch; an open dialog must keep showing the truth after operations throws it")
	}
}

// The kill switch is a door for NEW orders, not for money already in flight. A
// callback that answered 503 while KHQRPAY_ENABLED=false would strand every
// paid-but-not-yet-granted order: the row stays `created`, the tenant has paid and
// has no plan, and the gateway eventually gives up retrying — the exact "money in,
// plan never opened, nobody can see it" failure this tree has paid for before.
//
// Pinned at the source because reproducing it needs a live database, a paid order
// and a retrying gateway all at once; the same reason tenant_owner_test.go scans
// router.go instead of standing up two tenants.
func TestKHQRSettlementOutlivesTheKillSwitch(t *testing.T) {
	webhook := funcBody(t, "billing_khqr.go", "func (a *App) khqrWebhook(w http.ResponseWriter")
	if strings.Contains(webhook, `ErrServiceUnavailable("支付未配置")`) {
		t.Error("khqrWebhook refuses callbacks while the channel is switched off; in-flight money must still settle")
	}
	if strings.Contains(webhook, "!a.khqrEnabled()") {
		t.Error("khqrWebhook gates settlement on the kill switch")
	}
	if !strings.Contains(webhook, "settling an in-flight callback") {
		t.Error("khqrWebhook no longer logs that it settled a callback under a closed switch")
	}
	if !strings.Contains(webhook, "khqrWebhookVerifyAllowed(") {
		t.Error("khqrWebhook no longer routes its verify decision through khqrWebhookVerifyAllowed")
	}
	if !strings.Contains(webhook, "a.activatePayment(r.Context(), khqrProvider,") {
		t.Error("khqrWebhook no longer activates; a callback that cannot grant is not a settlement path")
	}
	// The switch may only decide whether the gateway is asked again.
	cases := []struct {
		channelOn, verifyConfigured, want bool
	}{
		{true, true, true},
		{true, false, false},
		{false, true, false}, // switched off: credentials may be revoked with the switch
		{false, false, false},
	}
	for _, tc := range cases {
		if got := khqrWebhookVerifyAllowed(tc.channelOn, tc.verifyConfigured); got != tc.want {
			t.Errorf("khqrWebhookVerifyAllowed(channelOn=%v, verifyConfigured=%v) = %v, want %v",
				tc.channelOn, tc.verifyConfigured, got, tc.want)
		}
	}
	// Issuing a new QR is the one path the switch still closes.
	if order := funcBody(t, "billing_khqr.go", "func (a *App) khqrCreateOrder"); !strings.Contains(order, "khqrEnabled()") {
		t.Error("khqrCreateOrder no longer refuses to issue QRs while the channel is switched off")
	}
}

// The revenue board must count every channel. It read `provider = 'paypal'` only,
// so when KHQR started taking money the operator would have watched a figure that
// never moved and concluded nothing had been collected. Pinned at the source
// because the reconciliation test for this view needs a live database.
func TestPlatformRevenueCountsEveryChannel(t *testing.T) {
	body := funcBody(t, "platform_ops_handlers.go", "func (a *App) platformRevenue")
	if strings.Contains(body, "provider = 'paypal'") || strings.Contains(body, "provider='paypal'") {
		t.Error("platformRevenue still filters on provider = 'paypal'; KHQR money would be invisible to operations")
	}
	if !strings.Contains(body, "GROUP BY provider") {
		t.Error("platformRevenue lost the per-provider breakdown")
	}
	for _, key := range []string{`"captured_payments"`, `"gross_usd"`, `"paid_tenants"`, `"pending_payments"`, `"by_provider"`} {
		if !strings.Contains(body, key) {
			t.Errorf("platformRevenue no longer returns %s", key)
		}
	}
}

// The catalog fields the console gates its QR button on are a frontend contract
// (docs/khqrpay-integration-spec.md §五): `khqr_enabled` at the top level and
// `khqr_price` per plan. They are pinned by tag because a rename here is silent
// at compile time and shows up as a missing button.
func TestBillingCatalogKeepsTheKHQRContractFields(t *testing.T) {
	raw, err := os.ReadFile("billing_paypal.go")
	if err != nil {
		t.Fatalf("read billing_paypal.go: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, `json:"khqr_price"`) {
		t.Error("billingCatalog no longer exposes a per-plan khqr_price")
	}
	if !strings.Contains(src, `"khqr_enabled"`) {
		t.Error("billingCatalog no longer exposes the top-level khqr_enabled switch")
	}
	// The existing PayPal fields must survive the addition untouched.
	for _, field := range []string{`json:"price,omitempty"`, `"checkout_ready"`, `"currency"`, `"plans"`, `"current"`} {
		if !strings.Contains(src, field) {
			t.Errorf("billingCatalog lost the %s field the PayPal console already reads", field)
		}
	}
}

// The kinds of tenant binding the isolation gate reads. `payments` is a tenant
// table, and the status/webhook statements run from places that did not just
// authenticate a tenant — so the binding has to be visible in the SQL, not implied
// by the caller (internal/tenantscope enforces exactly this).
func TestKHQRStatementsNameTheTenantColumn(t *testing.T) {
	statements := map[string]struct {
		sql     string
		channel string // how the statement names this provider
	}{
		"insert":       {sqlKHQRPaymentInsert, "'khqr'"},
		"order lookup": {sqlKHQRPaymentOrder, "provider='khqr'"},
		"set payload":  {sqlKHQRPaymentSetPayload, "provider='khqr'"},
		"mark failed":  {sqlKHQRPaymentMarkFailed, "provider='khqr'"},
	}
	for name, tc := range statements {
		if !strings.Contains(tc.sql, "user_id") {
			t.Errorf("%s statement does not name user_id: %s", name, tc.sql)
		}
		if !strings.Contains(tc.sql, tc.channel) {
			t.Errorf("%s statement is not scoped to this channel: %s", name, tc.sql)
		}
	}
}

// One activation transaction, not two. A second copy of "record the capture,
// grant the plan, extend paid_until" is how the PayPal path once shipped money
// in and no plan; the KHQR path must reach it through activatePayment's provider
// argument instead.
func TestKHQRDoesNotGrowItsOwnActivationTransaction(t *testing.T) {
	raw, err := os.ReadFile("billing_khqr.go")
	if err != nil {
		t.Fatalf("read billing_khqr.go: %v", err)
	}
	src := string(raw)
	if strings.Contains(src, "status='captured'") {
		t.Error("billing_khqr.go writes status='captured' itself; activation must go through activatePayment")
	}
	if strings.Contains(src, "sqlExtendPaidUntil") || strings.Contains(src, "applyPlanFrom") {
		t.Error("billing_khqr.go runs the plan-grant transaction itself; activatePayment owns it")
	}
	if !strings.Contains(src, "a.activatePayment(r.Context(), khqrProvider,") {
		t.Error("khqrWebhook no longer activates through the shared transaction")
	}
}

// The order must exist before a payable QR does. The other order (create the QR,
// then try to record it) has a silent failure mode: if the row never lands, the
// buyer pays a QR the webhook cannot find and nothing is granted — money in, no
// plan, no error a human would see. The insert therefore comes first in the
// source, and a refused insert (an id collision) must be noticed rather than
// ignored.
func TestKHQRRecordsTheOrderBeforeRequestingTheQR(t *testing.T) {
	body := funcBody(t, "billing_khqr.go", "func (a *App) khqrCreateOrder")
	insert := strings.Index(body, "sqlKHQRPaymentInsert")
	createQR := strings.Index(body, "CreateQR(")
	attach := strings.Index(body, "sqlKHQRPaymentSetPayload")
	if insert < 0 || createQR < 0 || attach < 0 {
		t.Fatalf("khqrCreateOrder no longer inserts, creates and attaches (insert=%d create=%d attach=%d)", insert, createQR, attach)
	}
	if insert > createQR {
		t.Error("khqrCreateOrder asks the gateway for a QR before the order row exists")
	}
	if attach < createQR {
		t.Error("khqrCreateOrder attaches the QR before it was requested")
	}
	if !strings.Contains(body, "RowsAffected()") {
		t.Error("khqrCreateOrder ignores whether the insert landed; a transaction-id collision would hand out another order's QR")
	}
	// The gateway requires a remark and signs it; the handler must supply one (the
	// client refuses an empty value, which would turn every order into a 503).
	if !strings.Contains(body, "Remark:") {
		t.Error("khqrCreateOrder sends no remark; the gateway requires one and it is covered by the signature")
	}
}

// The callback's sha256 signature is what proves the money moved — the path token
// only proves the caller was given the URL. It must therefore be checked in the
// webhook, and before the amount is compared: the row says what was owed, but the
// signature says a payment happened at all.
func TestKHQRWebhookVerifiesTheCallbackSignature(t *testing.T) {
	webhook := funcBody(t, "billing_khqr.go", "func (a *App) khqrWebhook(w http.ResponseWriter")
	verify := strings.Index(webhook, "VerifyCallback(event.ReqTime, transactionID, event.Amount, event.Status, event.Hash)")
	if verify < 0 {
		t.Fatal("khqrWebhook does not verify the callback signature (or passes something other than the callback's own req_time/amount/status/hash)")
	}
	if amount := strings.Index(webhook, "amountMatches(event.Amount"); amount >= 0 && verify > amount {
		t.Error("the amount is compared before the signature is verified")
	}
	if activate := strings.Index(webhook, "a.activatePayment(r.Context(), khqrProvider,"); activate >= 0 && verify > activate {
		t.Error("activation runs before the callback signature is verified")
	}
	// A rejected signature must answer 400 (the caller knows the URL; it just cannot
	// prove the payment) and must not mark the order failed — a forged callback must
	// not be able to kill a real one.
	if !strings.Contains(webhook, `ErrBadRequest("回调签名校验失败")`) {
		t.Error("a rejected callback signature must answer 400")
	}
}

// ============================================
// Webhook path token — the difference between "a callback" and a free plan
// ============================================
//
// The bare callback path was an unauthenticated "grant me a plan": a tenant owner
// creates an order (and is handed its transaction id), reads the price off the
// page, and POSTs their own success event. The token in the path is the only
// thing that distinguishes the gateway from that tenant, so every branch of it is
// pinned here.

const testWebhookToken = "4f8a2c1e9b7d6350a1c2e4f6079b8d3e"

// A discarded logger and a nil DB: everything these tests do must happen before
// either is touched.
func newWebhookTestApp(token string) *App {
	return &App{
		Cfg:    &config.Config{KHQRPay: config.KHQRPayConfig{Toggle: true, ProfileID: "p", Secret: "s", WebhookToken: token}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func webhookRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	body := `{"transaction_id":"MC-7-1700000000-AB12","status":"success","amount":"29.00"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/khqr/webhook/"+url.PathEscape(token), strings.NewReader(body))
	// What the mux does for a {token} pattern; set by hand so the handler can be
	// exercised without assembling the whole router.
	req.SetPathValue("token", token)
	return req
}

// The gate itself: the right token passes, a wrong one is a 404, and a deployment
// without a token refuses to settle at all (fail closed — a missing environment
// variable must never mean "no check").
func TestKHQRWebhookGateDecidesByThePathToken(t *testing.T) {
	app := newWebhookTestApp(testWebhookToken)
	if err := app.khqrWebhookGate(webhookRequest(t, testWebhookToken)); err != nil {
		t.Fatalf("the configured token was refused: %v", err)
	}

	cases := []struct {
		name     string
		appToken string
		present  string
		want     int
	}{
		{"wrong token", testWebhookToken, "00000000000000000000000000000000", http.StatusNotFound},
		{"empty path token", testWebhookToken, "", http.StatusNotFound},
		{"prefix of the token", testWebhookToken, testWebhookToken[:16], http.StatusNotFound},
		{"token with padding", testWebhookToken, " " + testWebhookToken + " ", 0},
		{"no token configured", "", testWebhookToken, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := newWebhookTestApp(tc.appToken).khqrWebhookGate(webhookRequest(t, tc.present))
			if tc.want == 0 {
				if err != nil {
					t.Fatalf("surrounding whitespace must not reject a correct token: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("token %q against configured %q was accepted", tc.present, tc.appToken)
			}
			apiErr, ok := err.(*ApiError)
			if !ok {
				t.Fatalf("gate error is %T, want *ApiError", err)
			}
			if apiErr.Status != tc.want {
				t.Errorf("status = %d, want %d (%s)", apiErr.Status, tc.want, apiErr.Message)
			}
		})
	}
}

// Behaviourally, through the handler: a wrong or missing token must be answered
// before the body is read and before the order row is looked up. The App has a
// nil DB, so reaching the database panics — which is exactly the failure signal we
// want if the gate ever moves or disappears.
func TestKHQRWebhookRefusesUntokenedCallbacksBeforeTouchingTheOrder(t *testing.T) {
	cases := []struct {
		name     string
		appToken string
		present  string
		want     int
	}{
		{"wrong token", testWebhookToken, "deadbeefdeadbeefdeadbeefdeadbeef", http.StatusNotFound},
		{"token not configured", "", testWebhookToken, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newWebhookTestApp(tc.appToken)
			var (
				got     error
				reached string
			)
			func() {
				defer func() {
					if r := recover(); r != nil {
						reached = "handler reached the database despite the token gate"
					}
				}()
				_, got = app.khqrWebhook(httptest.NewRecorder(), webhookRequest(t, tc.present))
			}()
			if reached != "" {
				t.Fatalf("%s: %v", reached, tc.present)
			}
			apiErr, ok := got.(*ApiError)
			if !ok {
				t.Fatalf("handler error is %T (%v), want *ApiError", got, got)
			}
			if apiErr.Status != tc.want {
				t.Errorf("status = %d, want %d", apiErr.Status, tc.want)
			}
			// The response must not hint that the route exists or that the token was
			// close: 404 body is the plain "not found" the mux would give.
			if tc.want == http.StatusNotFound && apiErr.Message != "not found" {
				t.Errorf("wrong-token response body = %q, want an indistinguishable 404", apiErr.Message)
			}
		})
	}
}

// Constant-time comparison, pinned at the source: a byte-by-byte `==` on a secret
// leaks its prefix through timing, and `hmac.Equal` is the one-line fix that a
// future "simplification" would undo.
func TestKHQRWebhookTokenComparisonIsConstantTime(t *testing.T) {
	gate := funcBody(t, "billing_khqr.go", "func (a *App) khqrWebhookGate")
	if !strings.Contains(gate, "hmac.Equal(") {
		t.Error("the webhook token is not compared with hmac.Equal")
	}
	if strings.Contains(gate, "WebhookToken ==") || strings.Contains(gate, "== expected") {
		t.Error("the webhook token is compared with ==")
	}
	// A missing token must be an error, never a skip.
	if !strings.Contains(gate, `ErrServiceUnavailable`) {
		t.Error("a missing KHQRPAY_WEBHOOK_TOKEN must refuse settlement")
	}
	// And a mismatch must not confirm the route exists.
	if !strings.Contains(gate, "StatusNotFound") {
		t.Error("a wrong token must answer 404, not 401/403")
	}
}

// The gate has to run first: before the body, before the order lookup, and
// without being folded into the channel switch (settlement must survive a switch
// that is off, but never an unauthenticated caller).
func TestKHQRWebhookTokenGateRunsFirst(t *testing.T) {
	webhook := funcBody(t, "billing_khqr.go", "func (a *App) khqrWebhook(w http.ResponseWriter")
	gate := strings.Index(webhook, "a.khqrWebhookGate(r)")
	read := strings.Index(webhook, "io.ReadAll")
	lookup := strings.Index(webhook, "sqlKHQRPaymentOrder")
	verifyDecision := strings.Index(webhook, "khqrWebhookVerifyAllowed(")
	switch {
	case gate < 0:
		t.Fatal("khqrWebhook no longer runs the token gate")
	case read >= 0 && gate > read:
		t.Error("khqrWebhook reads the body before checking the token")
	case lookup >= 0 && gate > lookup:
		t.Error("khqrWebhook looks up the order before checking the token")
	case verifyDecision >= 0 && gate > verifyDecision:
		t.Error("khqrWebhook asks the gateway to confirm before checking the token")
	}
	if strings.Contains(webhook, "if channelOn") && strings.Contains(webhook, "khqrWebhookGate") {
		// The switch may not resurrect as a condition around the gate.
		if before, _, ok := strings.Cut(webhook, "a.khqrWebhookGate(r)"); ok && strings.Contains(before, "channelOn") {
			t.Error("khqrWebhook conditions the token gate on the channel switch")
		}
	}
}
