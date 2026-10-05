package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"khmer-ai-cs-go/internal/paypal"
	"khmer-ai-cs-go/internal/usage"
)

// ============================================
// Billing — plan catalogue and PayPal checkout
// ============================================
//
// The shape of the money path: the console reads the catalogue (prices come from
// configuration, never from the browser), creates an order server-side, sends the
// buyer to PayPal's approve URL, and then captures. PayPal's capture response and
// PayPal's own webhook can both arrive, so activation is idempotent by
// construction — one conditional UPDATE on the payment row decides which of them
// grants the paid window. Nothing the browser sends is trusted for plan or price:
// both are read back from the row created here.

// Billing statements. Fully parameterized and kept as constants so call sites
// carry no SQL text and internal/sqlcheck finds every one of them.
const (
	sqlPaymentInsert = "INSERT INTO payments (user_id, provider, provider_order_id, plan, amount, currency, status) VALUES ($1,'paypal',$2,$3,$4::numeric,$5,'created') ON CONFLICT (provider, provider_order_id) DO NOTHING"

	sqlPaymentLookup = "SELECT user_id, plan, amount::text, currency, status FROM payments WHERE provider='paypal' AND provider_order_id=$1"

	sqlPaymentOwner = "SELECT user_id, status FROM payments WHERE provider='paypal' AND provider_order_id=$1"

	sqlPaymentMarkCaptured = "UPDATE payments SET status='captured', provider_capture_id=$2, detail=$3::jsonb, updated_at=NOW() WHERE provider='paypal' AND provider_order_id=$1 AND status='created'"

	sqlPaymentMarkFailed = "UPDATE payments SET status='failed', detail=$2::jsonb, updated_at=NOW() WHERE provider='paypal' AND provider_order_id=$1 AND status='created'"

	// GREATEST + COALESCE: renewing before the period ends extends from the end
	// date (not from today), paying after it starts from today.
	sqlExtendPaidUntil = "UPDATE tenant_billing SET paid_until = GREATEST(COALESCE(paid_until, NOW()), NOW()) + INTERVAL '30 days', updated_at = NOW() WHERE user_id = $1"

	sqlPaidState = "SELECT plan, paid_until, messages_used, monthly_message_quota, docs_used, monthly_doc_quota, cycle_end FROM tenant_billing WHERE user_id = $1"

	// paid_until IS NOT NULL is what keeps a manually granted plan out of the
	// sweep: platform staff changing a plan never set it.
	sqlExpiredPaidPlans = "SELECT user_id, plan FROM tenant_billing WHERE plan <> 'free' AND paid_until IS NOT NULL AND paid_until < NOW()"
)

// paypalEnabled reports whether this deployment can take money. Nil-safe: the
// client is absent in tests and in deployments without credentials.
func (a *App) paypalEnabled() bool { return a.PayPal != nil && a.PayPal.Enabled() }

// billingPlanPrice resolves the configured price for a purchasable plan. Free has
// no price by design (registration grants it), and a paid plan with no configured
// price is not for sale rather than free.
func (a *App) billingPlanPrice(plan string) (string, bool) {
	switch plan {
	case usage.PlanPro:
		if v := strings.TrimSpace(a.Cfg.PayPal.PricePro); v != "" {
			return v, true
		}
	case usage.PlanEnterprise:
		if v := strings.TrimSpace(a.Cfg.PayPal.PriceEnterprise); v != "" {
			return v, true
		}
	}
	return "", false
}

// normalizeAmount strips trailing zeros so PayPal's formatting ("29.00", "29.0",
// "29") can never reject a correct capture.
func normalizeAmount(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	if strings.IndexByte(v, '.') >= 0 {
		v = strings.TrimRight(v, "0")
		v = strings.TrimSuffix(v, ".")
	}
	return v
}

// amountMatches reports whether what PayPal captured is what the order said.
// Currency is compared case-insensitively but exactly: 29 USD against a EUR order
// is not the same purchase, whatever the number says.
func amountMatches(captured, expected, capturedCurrency, expectedCurrency string) bool {
	if !strings.EqualFold(strings.TrimSpace(capturedCurrency), strings.TrimSpace(expectedCurrency)) {
		return false
	}
	c, e := normalizeAmount(captured), normalizeAmount(expected)
	return c != "" && c == e
}

// billingReturnURL is where PayPal sends the buyer back. Derived from the public
// API URL (the console is served from the same host) so a deployment cannot end
// up with checkout returning to the wrong site.
func (a *App) billingReturnURL(action string) string {
	base := strings.TrimSpace(a.Cfg.Server.PublicAPIURL)
	base = strings.TrimSuffix(base, "/")
	base = strings.TrimSuffix(base, "/api/v1")
	if base == "" {
		return ""
	}
	return base + "/billing?paypal=" + action
}

// paidState is the tenant's entitlement plus usage: the same numbers the billing
// card shows. paid_until absent means "never paid".
func (a *App) paidState(ctx context.Context, userID int32) (map[string]any, error) {
	var plan string
	var paidUntil *time.Time
	var msgUsed, msgQuota, docUsed, docQuota int64
	var cycleEnd time.Time
	err := a.DB.QueryRow(ctx, sqlPaidState, userID).Scan(&plan, &paidUntil, &msgUsed, &msgQuota, &docUsed, &docQuota, &cycleEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"plan": usage.PlanFree, "paid_until": nil}, nil
	}
	if err != nil {
		return nil, ErrInternal("读取计费状态失败")
	}
	out := map[string]any{
		"plan":                  plan,
		"paid_until":            nil,
		"messages_used":         msgUsed,
		"monthly_message_quota": msgQuota,
		"docs_used":             docUsed,
		"monthly_doc_quota":     docQuota,
		"cycle_end":             cycleEnd,
	}
	if paidUntil != nil {
		out["paid_until"] = *paidUntil
	}
	return out, nil
}

// billingCatalog — GET /api/v1/billing/plans. What the upgrade page renders.
func (a *App) billingCatalog(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	type planEntry struct {
		Plan        string   `json:"plan"`
		Price       string   `json:"price,omitempty"`
		Purchasable bool     `json:"purchasable"`
		Messages    int64    `json:"messages"`
		Documents   *int64   `json:"documents"`
		Channels    *int64   `json:"channels"`
		Seats       *int64   `json:"seats"`
		Included    []string `json:"included"`
	}
	// unlimited serializes as null rather than 1000000000: the console then shows
	// "不限" without having to know the sentinel, and the number can move later.
	unlimited := func(v int64) *int64 {
		if v >= usage.Unlimited {
			return nil
		}
		return &v
	}
	plans := make([]planEntry, 0, len(usage.Plans()))
	for _, spec := range usage.Plans() {
		entry := planEntry{
			Plan:      spec.Name,
			Messages:  spec.Messages,
			Documents: unlimited(spec.Documents),
			Channels:  unlimited(spec.Channels),
			Seats:     unlimited(spec.Seats),
			Included:  spec.Included,
		}
		if price, ok := a.billingPlanPrice(spec.Name); ok {
			entry.Price = price
			entry.Purchasable = a.paypalEnabled()
		}
		plans = append(plans, entry)
	}
	state, err := a.paidState(r.Context(), user.UserID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"currency":       strings.TrimSpace(a.Cfg.PayPal.Currency),
		"checkout_ready": a.paypalEnabled(),
		"plans":          plans,
		"current":        state,
	}, nil
}

// paypalCreateOrder — POST /api/v1/billing/paypal/order {plan}.
func (a *App) paypalCreateOrder(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req struct {
		Plan string `json:"plan"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	plan := strings.TrimSpace(req.Plan)
	price, ok := a.billingPlanPrice(plan)
	if !ok {
		return nil, ErrBadRequest("该套餐未开放购买，请联系管理员")
	}
	if !a.paypalEnabled() {
		return nil, ErrServiceUnavailable("支付通道未配置，请联系管理员")
	}
	currency := strings.TrimSpace(a.Cfg.PayPal.Currency)
	order, err := a.PayPal.CreateOrder(r.Context(), paypal.CreateOrderInput{
		Plan:      plan,
		Amount:    price,
		Currency:  currency,
		UserID:    user.UserID,
		ReturnURL: a.billingReturnURL("return"),
		CancelURL: a.billingReturnURL("cancel"),
		BrandName: "RelayChat",
	})
	if err != nil {
		a.Logger.Error("paypal create order failed", "user_id", user.UserID, "plan", plan, "error", err.Error())
		return nil, ErrServiceUnavailable("支付通道暂时不可用，请稍后再试")
	}
	// The row is the order's memory: capture and webhook both read plan and price
	// back from here.
	if _, err := a.DB.Exec(r.Context(), sqlPaymentInsert, user.UserID, order.ID, plan, price, currency); err != nil {
		a.Logger.Error("paypal payment row insert failed", "user_id", user.UserID, "order_id", order.ID, "error", err.Error())
		return nil, ErrInternal("创建支付记录失败")
	}
	return map[string]any{
		"order_id":    order.ID,
		"approve_url": order.ApproveURL,
		"plan":        plan,
		"amount":      price,
		"currency":    currency,
	}, nil
}

// paypalCapture — POST /api/v1/billing/paypal/capture {order_id}. Called by the
// page PayPal returns the buyer to.
func (a *App) paypalCapture(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req struct {
		OrderID string `json:"order_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	orderID := strings.TrimSpace(req.OrderID)
	if orderID == "" {
		return nil, ErrBadRequest("缺少订单号")
	}
	// Ownership first: another tenant's order must answer exactly like a missing
	// one, and an order id is a capability worth not confirming.
	var owner int32
	var status string
	if err := a.DB.QueryRow(r.Context(), sqlPaymentOwner, orderID).Scan(&owner, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &ApiError{http.StatusNotFound, "订单不存在"}
		}
		return nil, ErrInternal("读取订单失败")
	}
	if owner != user.UserID {
		return nil, &ApiError{http.StatusNotFound, "订单不存在"}
	}
	if status == "captured" {
		state, err := a.paidState(r.Context(), user.UserID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"captured": true, "already": true, "current": state}, nil
	}
	if !a.paypalEnabled() {
		return nil, ErrServiceUnavailable("支付通道未配置，请联系管理员")
	}
	capture, err := a.PayPal.CaptureOrder(r.Context(), orderID)
	if err != nil {
		a.Logger.Error("paypal capture failed", "user_id", user.UserID, "order_id", orderID, "error", err.Error())
		return nil, ErrServiceUnavailable("支付确认失败，请稍后在账单页重试或联系管理员")
	}
	if capture.Status != "" && !strings.EqualFold(capture.Status, "COMPLETED") {
		return nil, ErrServiceUnavailable("支付尚未完成（状态 " + capture.Status + "），请稍后重试")
	}
	if _, err := a.activatePayment(r.Context(), orderID, capture.CaptureID, capture.Amount, capture.Currency, "capture"); err != nil {
		a.Logger.Error("paypal activation failed", "user_id", user.UserID, "order_id", orderID, "error", err.Error())
		return nil, ErrInternal("支付已收到但开通失败，请联系管理员")
	}
	state, err := a.paidState(r.Context(), user.UserID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"captured": true, "current": state}, nil
}

// paypalWebhook — POST /api/v1/billing/paypal/webhook. Public, so the signature
// check is the whole authentication: the client refuses to run without a
// configured webhook id.
func (a *App) paypalWebhook(w http.ResponseWriter, r *http.Request) (any, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, ErrBadRequest("读取请求失败")
	}
	if !a.paypalEnabled() {
		return nil, ErrServiceUnavailable("支付未配置")
	}
	if err := a.PayPal.VerifyWebhook(r.Context(), a.Cfg.PayPal.WebhookID, r.Header, body); err != nil {
		a.Logger.Warn("paypal webhook rejected", "error", err.Error())
		return nil, ErrBadRequest("webhook 签名校验失败")
	}
	var event struct {
		EventType string `json:"event_type"`
		Resource  struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Amount struct {
				Value        string `json:"value"`
				CurrencyCode string `json:"currency_code"`
			} `json:"amount"`
			SupplementaryData struct {
				RelatedIDs struct {
					OrderID string `json:"order_id"`
				} `json:"related_ids"`
			} `json:"supplementary_data"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, ErrBadRequest("事件格式错误")
	}
	if event.EventType != "PAYMENT.CAPTURE.COMPLETED" {
		// Verified event we do not act on: acknowledge so PayPal stops retrying.
		return map[string]string{"status": "ignored", "event_type": event.EventType}, nil
	}
	orderID := strings.TrimSpace(event.Resource.SupplementaryData.RelatedIDs.OrderID)
	if orderID == "" {
		a.Logger.Warn("paypal capture webhook carried no order id", "capture_id", event.Resource.ID)
		return map[string]string{"status": "ignored"}, nil
	}
	if _, err := a.activatePayment(r.Context(), orderID, event.Resource.ID, event.Resource.Amount.Value, event.Resource.Amount.CurrencyCode, "webhook"); err != nil {
		// The money is already captured: a 5xx makes PayPal retry instead of
		// leaving a paid tenant unprovisioned.
		a.Logger.Error("paypal webhook activation failed", "order_id", orderID, "error", err.Error())
		return nil, ErrServiceUnavailable("开通失败，等待 PayPal 重试")
	}
	return map[string]string{"status": "ok"}, nil
}

// activatePayment flips one payment row to captured and, only for the caller that
// flipped it, grants the paid window. The conditional UPDATE is the idempotency:
// PayPal's capture response and its webhook race each other routinely, and only
// one of them may hand out 30 days.
//
// The amount is checked against the row we wrote at order time, so a tampered or
// mismatched capture (wrong plan, wrong currency, wrong value) is recorded as
// failed instead of activating anything.
func (a *App) activatePayment(ctx context.Context, orderID, captureID, capturedAmount, capturedCurrency, source string) (bool, error) {
	var userID int32
	var plan, amount, currency, status string
	if err := a.DB.QueryRow(ctx, sqlPaymentLookup, orderID).Scan(&userID, &plan, &amount, &currency, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // an order we never recorded: nothing to grant
		}
		return false, err
	}
	if status == "captured" {
		return false, nil // the other caller already granted it
	}
	if !amountMatches(capturedAmount, amount, capturedCurrency, currency) {
		detail, _ := json.Marshal(map[string]string{
			"source":   source,
			"captured": capturedAmount + " " + capturedCurrency,
			"expected": amount + " " + currency,
		})
		if _, err := a.DB.Exec(ctx, sqlPaymentMarkFailed, orderID, string(detail)); err != nil {
			return false, err
		}
		return false, &ApiError{http.StatusBadGateway, "支付金额与订单不一致，已拒绝开通"}
	}
	detail, _ := json.Marshal(map[string]string{"source": source, "capture_id": captureID})
	tag, err := a.DB.Exec(ctx, sqlPaymentMarkCaptured, orderID, captureID, string(detail))
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil // lost the race: the other caller grants
	}
	if err := a.applyPlan(ctx, userID, plan); err != nil {
		return false, err
	}
	if _, err := a.DB.Exec(ctx, sqlExtendPaidUntil, userID); err != nil {
		return false, err
	}
	a.Logger.Info("paid plan activated", "user_id", userID, "plan", plan, "amount", amount+" "+currency, "source", source)
	return true, nil
}

// expirePaidPlans downgrades tenants whose paid window has closed. A plan with
// paid_until NULL (never paid, or granted by hand in the platform console) is
// deliberately left alone: a payment sweep must not revoke an operator's grant.
func (a *App) expirePaidPlans(ctx context.Context) {
	rows, err := a.DB.Query(ctx, sqlExpiredPaidPlans)
	if err != nil {
		a.Logger.Warn("paid-plan expiry scan failed", "error", err.Error())
		return
	}
	type lapsed struct {
		userID int32
		plan   string
	}
	var due []lapsed
	for rows.Next() {
		var l lapsed
		if err := rows.Scan(&l.userID, &l.plan); err != nil {
			rows.Close()
			a.Logger.Warn("paid-plan expiry scan failed", "error", err.Error())
			return
		}
		due = append(due, l)
	}
	rows.Close()
	for _, l := range due {
		if err := a.applyPlan(ctx, l.userID, usage.PlanFree); err != nil {
			a.Logger.Error("paid-plan expiry downgrade failed", "user_id", l.userID, "plan", l.plan, "error", err.Error())
			continue
		}
		a.Logger.Info("paid plan expired; tenant returned to the free plan", "user_id", l.userID, "was", l.plan)
		if a.Pipe != nil {
			// English on purpose for now: the merchant's own UI language would be
			// the right one and needs the bilingual text table (bot_texts.go).
			a.Pipe.SendTelegramNotify(ctx, l.userID,
				"RelayChat: your "+l.plan+" plan has expired and the account is back on the free plan (20 documents / 500 messages per 30 days). Renew from the billing page to restore it.")
		}
	}
}
