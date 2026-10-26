package api

import (
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"khmer-ai-cs-go/internal/khqrpay"
	"khmer-ai-cs-go/internal/textutil"
	"khmer-ai-cs-go/internal/usage"
)

// ============================================
// Billing — KHQR checkout (the second one-time channel)
// ============================================
//
// The shape of this money path is internal/paypal's on purpose: an order row is
// written server-side from the configured price, the buyer pays, and activation
// runs the same idempotent transaction (activatePayment). What differs is all on
// the provider's side:
//
//   - KHQR has no redirect and no capture call. The buyer scans a QR, and the
//     gateway either tells us (webhook) or the console polls a local row until
//     it does. So there is no capture route, and no browser-supplied order id is
//     ever part of the activation path.
//   - The webhook carries no signature, so it is a TRIGGER, not a verdict: the
//     row it names decides plan and price, the amount it claims is only
//     compared, and — when an operator has filled in a verification path — the
//     gateway is asked again before anything is granted
//     (docs/khqrpay-integration-spec.md §三.2, §四.5).
//   - The channel ships off and can be removed with one environment variable:
//     NBC warned on 2026-09-11 that khqr.cc is unlicensed, so PayPal stays the
//     channel that is always there (§二).
//
// Nothing in this file logs a secret, a signature or a request body. The
// gateway's own message is the only provider text that reaches a log line, and
// internal/khqrpay redacts even that.

const (
	// khqrProvider is the payments.provider value for this channel. One constant
	// so a typo cannot write rows the lookups never find (and the unique index
	// on (provider, provider_order_id) would happily accept).
	khqrProvider = "khqr"

	// khqrOrderTTL is how long a QR stays payable, and therefore how long the
	// console poll can last. It is recorded in detail.expires_at, so a row whose
	// dialog was closed hours ago still reports "expired" rather than pending
	// forever.
	khqrOrderTTL = 15 * time.Minute
)

// Billing statements for this channel. provider='khqr' is spelled here and
// nowhere else; the shared activation transaction takes the provider as an
// argument instead of growing a second copy. Every statement names user_id,
// which is both the tenantscope binding for payments and what the status handler
// compares with the caller's tenant.
const (
	sqlKHQRPaymentInsert = "INSERT INTO payments (user_id, provider, provider_order_id, plan, amount, currency, status, detail) VALUES ($1,'khqr',$2,$3,$4::numeric,$5,'created',$6::jsonb) ON CONFLICT (provider, provider_order_id) DO NOTHING"

	// The row is the order's memory: plan and amount are read back from here
	// before anything is granted.
	sqlKHQRPaymentOrder = "SELECT user_id, plan, amount::text, currency, status, detail::text, created_at FROM payments WHERE provider='khqr' AND provider_order_id=$1"

	// The gateway's answer is attached to the row it was created for, once it
	// exists — see khqrCreateOrder for why the order is written first.
	sqlKHQRPaymentSetPayload = "UPDATE payments SET detail=$2::jsonb, updated_at=NOW() WHERE provider='khqr' AND provider_order_id=$1 AND user_id=$3 AND status='created'"

	// user_id is part of the WHERE clause, not just of the lookup that produced the
	// id: this statement runs from the public webhook, and naming the tenant keeps
	// the isolation visible to internal/tenantscope (a transaction id alone is a
	// capability, not an authorization).
	sqlKHQRPaymentMarkFailed = "UPDATE payments SET status='failed', detail=$2::jsonb, updated_at=NOW() WHERE provider='khqr' AND provider_order_id=$1 AND user_id=$3 AND status='created'"
)

// khqrDetail is the payments.detail payload for this channel (spec §四.2). Raw
// is the gateway's reply as received, minus signing material, because a disputed
// QR is settled by what the vendor actually said. ReqTime is the gateway's own
// clock from the create response, so a dispute lines up with the vendor's logs
// without guessing at timezones.
type khqrDetail struct {
	QR        string         `json:"qr,omitempty"`
	QRURL     string         `json:"qr_url,omitempty"`
	ExpiresAt string         `json:"expires_at,omitempty"`
	Source    string         `json:"source,omitempty"`
	ReqTime   int64          `json:"req_time,omitempty"`
	Raw       map[string]any `json:"raw,omitempty"`
	// Written only by the mismatch path: after a rejected callback the first
	// question is which figure disagreed.
	Captured string `json:"captured,omitempty"`
	Expected string `json:"expected,omitempty"`
}

// khqrEnabled reports whether this deployment can take KHQR money. Nil-safe and
// fail-closed — switch, credentials and a constructed client all have to be
// there, because the order route cannot create a QR without any one of them.
func (a *App) khqrEnabled() bool {
	if a == nil || a.Cfg == nil || a.KHQRPay == nil {
		return false
	}
	return a.Cfg.KHQRPay.Enabled()
}

// khqrPlanPrice resolves this channel's price for a plan. Deliberately separate
// from billingPlanPrice: the two rails are priced independently (a local QR
// price can differ from the card price, and one may launch before the other).
func (a *App) khqrPlanPrice(plan string) (string, bool) {
	if a == nil || a.Cfg == nil {
		return "", false
	}
	switch plan {
	case usage.PlanPro:
		if v := strings.TrimSpace(a.Cfg.KHQRPay.PricePro); v != "" {
			return v, true
		}
	case usage.PlanEnterprise:
		if v := strings.TrimSpace(a.Cfg.KHQRPay.PriceEnterprise); v != "" {
			return v, true
		}
	}
	return "", false
}

// khqrSuccessURL is where the banking app sends the buyer after paying. The
// dialog does the confirming by polling, so this URL only has to land somewhere
// sensible — it is signed either way, and an empty value is the honest answer
// for a deployment with no PUBLIC_API_URL.
func (a *App) khqrSuccessURL() string {
	base := a.billingConsoleBase()
	if base == "" {
		return ""
	}
	return base + "/billing?khqr=return"
}

// khqrTransactionID mints the provider order id: MC-<userID>-<unix>-<4 random>.
// Only [A-Za-z0-9-] and at most 40 characters (spec §四.2), and it is the ONLY
// thing an unsigned webhook has to locate the row — so it must be unguessable
// enough not to be brute-forced and unique per attempt. The insert's ON CONFLICT
// plus the (provider, provider_order_id) index refuse a collision instead of
// overwriting somebody's order.
func khqrTransactionID(userID int32, now time.Time) string {
	return "MC-" + strconv.FormatInt(int64(userID), 10) + "-" + strconv.FormatInt(now.Unix(), 10) + "-" + khqrRandomSuffix(4)
}

// khqrRandomSuffix returns n characters from an uppercase alphanumeric alphabet.
// crypto/rand cannot fail on any supported platform; if it somehow did, the
// timestamp in the id still keeps the order findable and the unique index still
// rejects a collision, so a fixed suffix is the safe degradation.
func khqrRandomSuffix(n int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	buf := make([]byte, n)
	if _, err := crand.Read(buf); err != nil {
		return strings.Repeat("0", n)
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf)
}

// khqrExpiresAt is when this order stops being payable: detail.expires_at when
// the row carries one, otherwise the documented 15-minute window counted from
// created_at.
func khqrExpiresAt(detail khqrDetail, createdAt time.Time) time.Time {
	if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(detail.ExpiresAt)); err == nil {
		return ts
	}
	if createdAt.IsZero() {
		return time.Time{}
	}
	return createdAt.Add(khqrOrderTTL)
}

// khqrState maps one payment row to the four states the console renders. Pure,
// and takes now as an argument, so the mapping (and "expired") is testable
// without a database and is decided one way in both places.
func khqrState(status string, detail khqrDetail, createdAt, now time.Time) string {
	switch status {
	case "captured":
		return "paid"
	case "failed":
		return "failed"
	case "created":
		if !khqrExpiresAt(detail, createdAt).After(now) {
			return "expired"
		}
		return "pending"
	}
	// A state this channel does not know (a future one, or a row edited by
	// hand) reads as failed: neither paid nor still payable.
	return "failed"
}

// khqrWebhookCurrency is the currency the callback is judged in: the vendor's own
// field when it sends one, otherwise the order's. The public callback sample
// carries no currency at all, and the row is the authority anyway — refusing
// every callback for a missing field would leave the channel webhook-only in name.
func khqrWebhookCurrency(payloadCurrency, orderCurrency string) string {
	if v := strings.TrimSpace(payloadCurrency); v != "" {
		return v
	}
	return strings.TrimSpace(orderCurrency)
}

// khqrWebhookVerifyAllowed reports whether a callback may ask the gateway to
// confirm itself. Two independent reasons to skip it: no verification path is
// configured (the shipped default), and the channel is switched off — a switch
// that is thrown during an incident usually takes the credentials with it, and a
// confirmation call that fails on a revoked secret would turn a settlement
// problem into an outage of the read path too.
func khqrWebhookVerifyAllowed(channelOn, verifyConfigured bool) bool {
	return channelOn && verifyConfigured
}

// khqrSetFailed marks one of this channel's orders failed and grants nothing.
// Only a detail this service built is stored — never the callback body, which is
// unauthenticated input.
func (a *App) khqrSetFailed(ctx context.Context, transactionID string, userID int32, detail khqrDetail) {
	if detail.Source == "" {
		detail.Source = khqrProvider
	}
	raw, _ := json.Marshal(detail)
	if _, err := a.DB.Exec(ctx, sqlKHQRPaymentMarkFailed, transactionID, string(raw), userID); err != nil {
		a.Logger.Error("khqr payment row could not be marked failed", "transaction_id", transactionID, "error", err.Error())
	}
}

// khqrCreateOrder — POST /api/v1/billing/khqr/order {plan}. The only entry point
// the kill switch closes: no new QR is issued while KHQRPAY_ENABLED is false
// (read and settlement keep working — see khqrStatus and khqrWebhook).
func (a *App) khqrCreateOrder(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req struct {
		Plan string `json:"plan"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	plan := strings.TrimSpace(req.Plan)
	if !a.khqrEnabled() {
		return nil, ErrServiceUnavailable("KHQR 通道未启用，请联系管理员")
	}
	price, ok := a.khqrPlanPrice(plan)
	if !ok {
		return nil, ErrBadRequest("该套餐未开放扫码购买，请联系管理员")
	}
	currency := strings.TrimSpace(a.Cfg.KHQRPay.Currency)
	now := time.Now()
	expiresAt := now.Add(khqrOrderTTL)
	// The row is written BEFORE the QR exists, and that order is the point: a
	// payable QR whose transaction we never recorded is money the webhook cannot
	// find (it answers "ignored") and a plan nobody grants — the same
	// money-in/no-plan failure the PayPal transaction comment describes, reached
	// from the other side. Writing first also makes an id collision cheap: the
	// insert refuses it and we mint another instead of handing out a QR that
	// belongs to somebody else's order.
	transactionID := ""
	for attempt := 0; attempt < 3 && transactionID == ""; attempt++ {
		candidate := khqrTransactionID(user.Tenant(), now)
		seed, _ := json.Marshal(khqrDetail{ExpiresAt: expiresAt.UTC().Format(time.RFC3339), Source: khqrProvider})
		tag, err := a.DB.Exec(r.Context(), sqlKHQRPaymentInsert, user.Tenant(), candidate, plan, price, currency, string(seed))
		if err != nil {
			a.Logger.Error("khqr payment row insert failed", "user_id", user.Tenant(), "plan", plan, "error", err.Error())
			return nil, ErrInternal("创建支付记录失败")
		}
		if tag.RowsAffected() > 0 {
			transactionID = candidate
		}
	}
	if transactionID == "" {
		a.Logger.Error("khqr transaction id collided three times; nothing created", "user_id", user.Tenant(), "plan", plan)
		return nil, ErrInternal("创建支付记录失败，请重试")
	}
	qr, err := a.KHQRPay.CreateQR(r.Context(), khqrpay.CreateInput{
		TransactionID: transactionID,
		Amount:        price,
		SuccessURL:    a.khqrSuccessURL(),
		// remark is mandatory for this gateway and is part of the signature; the
		// plan name is what makes a bank statement line readable. No tenant data
		// here — the vendor does not need it, and custom_fields carries the id.
		Remark: "MekongChat " + plan + " 30-day plan",
		// Optional, unsigned, echoed back in the callback: items says which plan, and
		// custom_fields (base64 JSON on the wire) lets a human match a payment to a
		// tenant without decoding a transaction id.
		Items:        plan,
		CustomFields: map[string]any{"user_id": user.Tenant(), "plan": plan},
	})
	if err != nil {
		// err carries the reason only (internal/khqrpay redacts the secret and
		// the signature), so it is safe in a log and unusable in a response. The
		// row is closed rather than left pending: this order will never produce a
		// payable QR, and a "pending" nobody can pay is what the console would
		// poll for fifteen minutes.
		a.khqrSetFailed(r.Context(), transactionID, user.Tenant(), khqrDetail{Source: "create"})
		a.Logger.Error("khqr create qr failed", "user_id", user.UserID, "plan", plan, "error", err.Error())
		return nil, ErrServiceUnavailable("扫码支付通道暂时不可用，请稍后再试")
	}
	detail, _ := json.Marshal(khqrDetail{
		QR:        qr.QR,
		QRURL:     qr.QRURL,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		Source:    khqrProvider,
		ReqTime:   qr.ReqTime,
		Raw:       qr.Response,
	})
	if _, err := a.DB.Exec(r.Context(), sqlKHQRPaymentSetPayload, transactionID, string(detail), user.Tenant()); err != nil {
		// Without the QR on the row the console cannot render it, so this is a
		// failure the buyer sees and can retry — not a silent one.
		a.Logger.Error("khqr payment detail update failed", "user_id", user.Tenant(), "transaction_id", transactionID, "error", err.Error())
		return nil, ErrInternal("创建支付记录失败")
	}
	return map[string]any{
		"transaction_id": transactionID,
		"qr":             qr.QR,
		"qr_url":         qr.QRURL,
		"plan":           plan,
		"amount":         price,
		"currency":       currency,
		"expires_at":     expiresAt,
	}, nil
}

// khqrStatus — GET /api/v1/billing/khqr/{transaction_id}/status. The dialog polls
// this every few seconds, so it is a read of the caller's own row and nothing
// else: no gateway call, and no state change.
//
// Deliberately NOT gated on khqrEnabled(): the kill switch closes the door for
// new orders, and a dialogs already open when operations throws it must keep
// showing pending → paid rather than an error, because the money may already be
// in flight. The tenant check below is the only authorization this read needs.
func (a *App) khqrStatus(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	transactionID := strings.TrimSpace(r.PathValue("transaction_id"))
	if transactionID == "" {
		return nil, ErrBadRequest("缺少交易号")
	}
	var owner int32
	var plan, amount, currency, status, detailRaw string
	var createdAt time.Time
	err := a.DB.QueryRow(r.Context(), sqlKHQRPaymentOrder, transactionID).
		Scan(&owner, &plan, &amount, &currency, &status, &detailRaw, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Another tenant's transaction id answers exactly like a missing one:
			// the id is a capability, and a 403 would confirm that it exists.
			return nil, &ApiError{http.StatusNotFound, "订单不存在"}
		}
		return nil, ErrInternal("读取订单失败")
	}
	if owner != user.Tenant() {
		return nil, &ApiError{http.StatusNotFound, "订单不存在"}
	}
	var detail khqrDetail
	if err := json.Unmarshal([]byte(detailRaw), &detail); err != nil {
		// A detail we cannot read only costs the QR image and the recorded
		// expiry; the row's status still decides the answer, so this is a WARN
		// rather than a 500 in front of a poll.
		a.Logger.Warn("khqr payment detail unreadable", "transaction_id", transactionID, "error", err.Error())
	}
	state := khqrState(status, detail, createdAt, time.Now())
	out := map[string]any{
		"status":   state,
		"paid":     state == "paid",
		"plan":     plan,
		"amount":   amount,
		"currency": currency,
	}
	if expires := khqrExpiresAt(detail, createdAt); !expires.IsZero() {
		out["expires_at"] = expires
	}
	if state == "paid" {
		current, err := a.paidState(r.Context(), user.Tenant())
		if err != nil {
			return nil, err
		}
		out["current"] = current
		if paidUntil, ok := current["paid_until"]; ok {
			out["paid_until"] = paidUntil
		}
	}
	return out, nil
}

// khqrWebhookGate decides whether a callback may be considered at all. It runs
// before the body is read, let alone decoded.
//
// WHY A PATH TOKEN — with no signature header, this route is an unauthenticated
// "grant me a plan" call, and every tenant owner holds exactly the two things it
// used to need: khqrCreateOrder hands them their own transaction id, and the price
// is printed on the page. Order → read txid → POST {"status":"success","amount":…}
// was a reproducible free upgrade. The token in the path proves the caller was
// given the URL, which only the gateway was (KHQRPAY_WEBHOOK_TOKEN — a separate
// secret from the signing key, because a URL ends up in access logs and a signing
// key must never).
//
// Two failure shapes, deliberately different:
//   - no token configured → 503 plus an ERROR line. The deployment is broken, the
//     gateway should keep retrying while operations fix it, and — the point — a
//     missing environment variable must never quietly mean "no check".
//   - token mismatch → 404, not 401/403, so a scanner learns nothing about
//     whether the route exists. The presented value is never logged.
func (a *App) khqrWebhookGate(r *http.Request) error {
	expected := ""
	if a != nil && a.Cfg != nil {
		expected = strings.TrimSpace(a.Cfg.KHQRPay.WebhookToken)
	}
	if expected == "" {
		if a.Logger != nil {
			a.Logger.Error("KHQRPAY_WEBHOOK_TOKEN 未配置，拒绝结算 KHQR 回调",
				"remote_ip", clientIP(r))
		}
		return ErrServiceUnavailable("支付未配置")
	}
	if !hmac.Equal([]byte(strings.TrimSpace(r.PathValue("token"))), []byte(expected)) {
		if a.Logger != nil {
			a.Logger.Warn("khqr webhook path token did not match; callback refused",
				"remote_ip", clientIP(r))
		}
		return &ApiError{http.StatusNotFound, "not found"}
	}
	return nil
}

// khqrWebhook — POST /api/v1/billing/khqr/webhook/{token}. Public: the gateway
// holds no account here.
//
// WHAT AUTHENTICATES WHAT, in the order the checks run:
//
//  1. the path token (KHQRPAY_WEBHOOK_TOKEN) — authenticates the CALLER: whoever
//     was given the URL, which only the gateway was;
//  2. the sha256 signature over the callback body (VerifyCallback) —
//     authenticates THIS PAYMENT, which is a different question, and the reason
//     the token alone is not enough;
//  3. the `payments` row this transaction id names (written by khqrCreateOrder
//     before the QR existed) — decides plan and price;
//  4. the amount/currency matching that row — the row is the authority;
//  5. optionally, KHQRPAY_VERIFY_PATH — the gateway's own record again.
//
// DEPLOYMENT FACT: the gateway posts to its "Global Webhook URL" when one is
// configured and otherwise falls back to the order's success_url — which is a
// browser page, not this endpoint. Operations must set the Global Webhook URL in
// the merchant dashboard (that URL plus the path token); without it there is no
// server-side callback at all, the sha256-signed body never arrives, and the
// console can only ever poll.
//
// The kill switch does NOT stop this route. KHQRPAY_ENABLED=false closes the door
// for new orders; a callback for an order already on the books is money already
// in flight, and stranding it is the failure this tree keeps repeating — money
// collected, plan never opened, nothing a human would see. So a switched-off
// channel only means we skip the gateway confirmation (its credentials may have
// been revoked with the switch) and log that we settled under a closed switch.
func (a *App) khqrWebhook(w http.ResponseWriter, r *http.Request) (any, error) {
	// Before the body: an unauthenticated caller gets no further than this line.
	if err := a.khqrWebhookGate(r); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, ErrBadRequest("读取请求失败")
	}
	var event struct {
		ReqTime       int64  `json:"req_time"`
		TransactionID string `json:"transaction_id"`
		Status        string `json:"status"`
		Amount        string `json:"amount"`
		Currency      string `json:"currency"`
		CaptureID     string `json:"capture_id"`
		Hash          string `json:"hash"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, ErrBadRequest("事件格式错误")
	}
	transactionID := strings.TrimSpace(event.TransactionID)
	if transactionID == "" {
		a.Logger.Warn("khqr webhook carried no transaction id")
		return map[string]string{"status": "ignored"}, nil
	}
	// Read once, before any decision: this is the operator's switch, and its only
	// effect on a callback is whether the gateway is asked to confirm again.
	channelOn := a.khqrEnabled()
	if !channelOn {
		a.Logger.Warn("khqr channel is switched off; settling an in-flight callback",
			"transaction_id", transactionID)
	}
	var owner int32
	var plan, amount, currency, status, detailRaw string
	var createdAt time.Time
	err = a.DB.QueryRow(r.Context(), sqlKHQRPaymentOrder, transactionID).
		Scan(&owner, &plan, &amount, &currency, &status, &detailRaw, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// An order we never wrote. Answer 200 so the gateway stops retrying
			// something we cannot act on (spec §四.5).
			a.Logger.Info("khqr webhook for an unknown transaction id", "transaction_id", transactionID)
			return map[string]string{"status": "ignored"}, nil
		}
		// A database failure is not the gateway's fault, but a 5xx is the only
		// way to get the callback delivered again.
		a.Logger.Error("khqr webhook lookup failed", "transaction_id", transactionID, "error", err.Error())
		return nil, ErrServiceUnavailable("稍后重试")
	}
	if status != "created" {
		// Already granted or already rejected: acknowledge, do not touch the row.
		return map[string]string{"status": "ignored"}, nil
	}
	if !khqrpay.IsPaidWord(event.Status) {
		// An unrecognised status word is not a payment we can grant. Leave the row
		// alone: a stuck "created" row simply expires, which is the fail-safe
		// direction on a money path. (The gateway is documented to send "SUCCESS"
		// only, so this branch is a guard against a changed contract, not a path
		// real traffic takes.)
		a.Logger.Warn("khqr webhook status is not a paid one; nothing granted",
			"transaction_id", transactionID, "status", textutil.Ellipsize(strings.Join(strings.Fields(event.Status), " "), 32))
		return map[string]string{"status": "ignored"}, nil
	}
	// The signed proof that this payment happened, checked BEFORE the amount: the
	// row below decides how much was owed, but only the gateway's own sha256 over
	// req_time + transaction_id + amount says the money moved at all. The amount
	// used here is the callback's original string — normalising it would verify a
	// different message than the one signed.
	if err := a.KHQRPay.VerifyCallback(event.ReqTime, transactionID, event.Amount, event.Status, event.Hash); err != nil {
		a.Logger.Warn("khqr webhook signature rejected; nothing granted",
			"transaction_id", transactionID, "error", err.Error())
		// 400, not the 404 the path gate uses: the caller proved it knows the URL,
		// it just cannot prove the payment.
		return nil, ErrBadRequest("回调签名校验失败")
	}
	payloadCurrency := khqrWebhookCurrency(event.Currency, currency)
	if !amountMatches(event.Amount, amount, payloadCurrency, currency) {
		a.khqrSetFailed(r.Context(), transactionID, owner, khqrDetail{
			Source:   "webhook",
			Captured: event.Amount + " " + payloadCurrency,
			Expected: amount + " " + currency,
		})
		a.Logger.Warn("khqr webhook amount did not match the order; order marked failed",
			"transaction_id", transactionID, "claimed", event.Amount+" "+payloadCurrency, "expected", amount+" "+currency)
		return map[string]string{"status": "ignored"}, nil
	}
	// Optional server-side confirmation: with a verification path configured the
	// gateway's own record is the judge, and the callback is only what woke us up.
	if khqrWebhookVerifyAllowed(channelOn, a.KHQRPay.VerifyConfigured()) {
		check, verifyErr := a.KHQRPay.Verify(r.Context(), transactionID)
		if verifyErr != nil {
			// Fail loud: the money may well have arrived, and a 5xx is what makes
			// the gateway deliver the callback again.
			a.Logger.Error("khqr verify failed; waiting for a redelivery",
				"transaction_id", transactionID, "error", verifyErr.Error())
			return nil, ErrServiceUnavailable("核对失败，等待重试")
		}
		verifiedCurrency := khqrWebhookCurrency(check.Currency, currency)
		if !check.Paid || (check.Amount != "" && !amountMatches(check.Amount, amount, verifiedCurrency, currency)) {
			a.Logger.Warn("khqr verify did not confirm the payment; nothing granted",
				"transaction_id", transactionID,
				"status", textutil.Ellipsize(strings.Join(strings.Fields(check.Status), " "), 32),
				"paid", check.Paid)
			return map[string]string{"status": "unverified"}, nil
		}
	}
	// false from here means either "lost the race" (a second callback for the same
	// order, or a mismatch that activatePayment recorded as failed) — in both
	// cases the row is settled and the gateway should stop retrying.
	if _, err := a.activatePayment(r.Context(), khqrProvider, transactionID,
		strings.TrimSpace(event.CaptureID), event.Amount, payloadCurrency, "webhook"); err != nil {
		a.Logger.Error("khqr webhook activation failed", "transaction_id", transactionID, "error", err.Error())
		// Everything that reaches here is retriable: the transaction rolls back,
		// so a redelivered callback is the correct next step.
		return nil, ErrServiceUnavailable("开通失败，等待重试")
	}
	return map[string]string{"status": "ok"}, nil
}
