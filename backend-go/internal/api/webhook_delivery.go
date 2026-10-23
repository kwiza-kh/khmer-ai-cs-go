package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/textutil"
	"khmer-ai-cs-go/internal/webhookout"
)

// Outbound webhook delivery worker.
//
// A delivery row is written by webhookout.Emit next to the business write that
// produced the event; this file turns those rows into signed HTTP POSTs. The
// queue exists so a merchant's slow or broken endpoint can never delay a
// customer reply: the worker runs on its own loop, retries with backoff, and a
// terminal failure is recorded (and logged) instead of being silently dropped.
const (
	webhookDeliveryBatch       = 10
	webhookDeliveryInterval    = 10 * time.Second
	webhookDeliveryTimeout     = 10 * time.Second
	webhookDeliveryMaxAttempts = 6
	webhookResponseBodyLimit   = 2048
	webhookResponseErrorLimit  = 1000
	// A worker that dies mid-request leaves the row in 'delivering'; after this
	// grace period another instance may reclaim it. The request itself times out
	// far earlier, so a live worker never has a row locked this long.
	webhookDeliveryStaleLock = 5 * time.Minute
)

// Backoff between attempts, indexed by the number of attempts already consumed:
// the first failure waits one minute, the second five, and so on. A merchant
// whose server is down for an hour gets a terminal record instead of an
// infinite retry loop; one that is down for seconds still gets every event.
var webhookRetryDelays = []time.Duration{
	1 * time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour,
}

// webhookDeliveryClient is the SSRF-guarded client shared with URL ingestion:
// the dialer's Control hook rejects any non-public address (including after DNS
// resolution, so rebinding cannot land on 169.254.169.254) and redirects are
// refused outright — a webhook endpoint that answers 30x is a configuration
// error, and following it would let a subscriber point our POST at another
// host after validation.
var webhookDeliveryClient = &http.Client{
	Timeout:   webhookDeliveryTimeout,
	Transport: rag.PublicFetchClient().Transport,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// webhookDeliveryRow is one claimed queue row.
type webhookDeliveryRow struct {
	deliveryID     int64
	subscriptionID int32
	userID         int32
	event          string
	eventID        string
	payload        string
	attempts       int32
}

// webhookOutcome is what happens to a delivery after one attempt.
type webhookOutcome struct {
	success  bool
	terminal bool
	retryIn  time.Duration
}

// classifyWebhookAttempt decides the next state. A 2xx with no transport error
// is success; anything else is retried until the attempt cap, then terminal.
// attempts is the value AFTER the claim incremented it, so the cap counts the
// attempt that just failed.
func classifyWebhookAttempt(statusCode int, sendErr error, attempts int32) webhookOutcome {
	if sendErr == nil && statusCode >= 200 && statusCode < 300 {
		return webhookOutcome{success: true}
	}
	if attempts >= webhookDeliveryMaxAttempts {
		return webhookOutcome{terminal: true}
	}
	return webhookOutcome{retryIn: webhookRetryDelay(attempts)}
}

// webhookRetryDelay is the wait before the next attempt, capped at the last
// entry (a retry loop must not keep growing forever).
func webhookRetryDelay(attempts int32) time.Duration {
	idx := int(attempts) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(webhookRetryDelays) {
		idx = len(webhookRetryDelays) - 1
	}
	return webhookRetryDelays[idx]
}

// deliverPendingWebhooks claims one batch of due deliveries and sends them.
// Runs on its own loop; every iteration is bounded by the batch size so one
// unreachable subscriber cannot own the worker.
func (a *App) deliverPendingWebhooks(ctx context.Context) {
	rows, err := a.DB.Query(ctx,
		`WITH due AS (
		     SELECT delivery_id
		       FROM webhook_deliveries
		      WHERE (status = 'pending' AND next_attempt_at <= NOW())
		         OR (status = 'delivering' AND locked_at IS NOT NULL AND locked_at < NOW() - $2::interval)
		      ORDER BY next_attempt_at, delivery_id
		      FOR UPDATE SKIP LOCKED
		      LIMIT $1
		 )
		 UPDATE webhook_deliveries d
		    SET status = 'delivering', locked_at = NOW(), attempts = d.attempts + 1, last_attempt_at = NOW()
		   FROM due
		  WHERE d.delivery_id = due.delivery_id
		 RETURNING d.delivery_id, d.subscription_id, d.user_id, d.event, COALESCE(d.event_id, ''), COALESCE(d.payload::text, '{}'), d.attempts`,
		webhookDeliveryBatch, webhookDeliveryStaleLock.String())
	if err != nil {
		if a.Logger != nil {
			a.Logger.Warn("webhook delivery claim failed", "error", err.Error())
		}
		return
	}
	claimed := make([]webhookDeliveryRow, 0, webhookDeliveryBatch)
	for rows.Next() {
		var row webhookDeliveryRow
		if err := rows.Scan(&row.deliveryID, &row.subscriptionID, &row.userID, &row.event, &row.eventID, &row.payload, &row.attempts); err != nil {
			if a.Logger != nil {
				a.Logger.Warn("webhook delivery row unreadable", "error", err.Error())
			}
			continue
		}
		claimed = append(claimed, row)
	}
	if err := rows.Err(); err != nil {
		if a.Logger != nil {
			a.Logger.Warn("webhook delivery claim read failed", "error", err.Error())
		}
	}
	rows.Close()
	for _, row := range claimed {
		a.deliverWebhook(ctx, row)
	}
}

// deliverWebhook sends one claimed delivery and records the outcome.
func (a *App) deliverWebhook(ctx context.Context, row webhookDeliveryRow) {
	var subURL, storedSecret string
	var active bool
	err := a.DB.QueryRow(ctx,
		"SELECT url, COALESCE(secret, ''), is_active FROM webhook_subscriptions WHERE subscription_id = $1 AND user_id = $2",
		row.subscriptionID, row.userID).Scan(&subURL, &storedSecret, &active)
	if err != nil {
		// The subscription was deleted after the event was queued (FK cascade
		// normally takes the row with it; a manual cleanup can still leave one).
		a.finishWebhookDelivery(ctx, row, webhookOutcome{terminal: true}, 0, "", "订阅已被删除")
		return
	}
	if !active {
		// Disabled means "stop sending", including events queued while it was on.
		a.finishWebhookDelivery(ctx, row, webhookOutcome{terminal: true}, 0, "", "订阅已停用")
		return
	}
	target, verr := webhookDeliveryTarget(subURL)
	if verr != nil {
		a.finishWebhookDelivery(ctx, row, webhookOutcome{terminal: true}, 0, "", "URL 被拒绝: "+verr.Error())
		return
	}
	statusCode, respBody, sendErr := a.sendWebhook(ctx, row, target, a.openWebhookSecret(storedSecret))
	outcome := classifyWebhookAttempt(statusCode, sendErr, row.attempts)
	errMsg := ""
	switch {
	case sendErr != nil:
		errMsg = sendErr.Error()
	case !outcome.success:
		errMsg = fmt.Sprintf("HTTP %d: %s", statusCode, textutil.TruncateRunes(respBody, 300))
	}
	a.finishWebhookDelivery(ctx, row, outcome, statusCode, respBody, errMsg)
}

// sendWebhook performs one POST. statusCode is 0 when the request never
// produced a response (dial timeout, TLS failure, SSRF refusal).
func (a *App) sendWebhook(ctx context.Context, row webhookDeliveryRow, target *url.URL, secret string) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, webhookDeliveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(row.payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "MekongChat-Webhook/1.0")
	req.Header.Set("X-MekongChat-Event", row.event)
	req.Header.Set("X-MekongChat-Delivery", strconv.FormatInt(row.deliveryID, 10))
	if row.eventID != "" {
		req.Header.Set("X-MekongChat-Event-Id", row.eventID)
	}
	if secret != "" {
		ts := time.Now().Unix()
		req.Header.Set("X-MekongChat-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-MekongChat-Signature", webhookSignature(secret, ts, []byte(row.payload)))
	}
	resp, err := webhookDeliveryClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, webhookResponseBodyLimit))
	return resp.StatusCode, string(body), nil
}

// finishWebhookDelivery writes the row's next state. A failed status write is
// logged: leaving the row in 'delivering' means the stale-lock reclaim will
// retry it, so the event is not lost.
func (a *App) finishWebhookDelivery(ctx context.Context, row webhookDeliveryRow, outcome webhookOutcome, statusCode int, respBody, errMsg string) {
	respBody = textutil.TruncateRunes(respBody, webhookResponseBodyLimit)
	errMsg = textutil.TruncateRunes(errMsg, webhookResponseErrorLimit)
	var err error
	switch {
	case outcome.success:
		_, err = a.DB.Exec(ctx,
			`UPDATE webhook_deliveries
			    SET status = 'success', success = true, status_code = NULLIF($4, 0),
			        response_body = $5, error = '', delivered_at = NOW(), locked_at = NULL
			  WHERE delivery_id = $1 AND user_id = $2 AND subscription_id = $3`,
			row.deliveryID, row.userID, row.subscriptionID, statusCode, respBody)
	case outcome.terminal:
		_, err = a.DB.Exec(ctx,
			`UPDATE webhook_deliveries
			    SET status = 'failed', success = false, status_code = NULLIF($4, 0),
			        response_body = $5, error = $6, locked_at = NULL
			  WHERE delivery_id = $1 AND user_id = $2 AND subscription_id = $3`,
			row.deliveryID, row.userID, row.subscriptionID, statusCode, respBody, errMsg)
	default:
		_, err = a.DB.Exec(ctx,
			`UPDATE webhook_deliveries
			    SET status = 'pending', success = false, status_code = NULLIF($4, 0),
			        response_body = $5, error = $6, next_attempt_at = $7, locked_at = NULL
			  WHERE delivery_id = $1 AND user_id = $2 AND subscription_id = $3`,
			row.deliveryID, row.userID, row.subscriptionID, statusCode, respBody, errMsg, time.Now().Add(outcome.retryIn))
	}
	if err != nil && a.Logger != nil {
		a.Logger.Error("webhook delivery state update failed",
			"delivery_id", row.deliveryID, "subscription_id", row.subscriptionID, "error", err.Error())
	}
	if outcome.terminal && a.Logger != nil {
		a.Logger.Warn("webhook delivery failed permanently",
			"delivery_id", row.deliveryID, "subscription_id", row.subscriptionID,
			"event", row.event, "attempts", row.attempts, "error", errMsg)
	}
}

// webhookSignature signs timestamp + "." + body with the subscription secret.
// Signing the timestamp as well as the body is what lets the receiver reject a
// replayed request: the header is checked against both.
func webhookSignature(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.", timestamp)
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// webhookDeliveryTarget re-validates the stored URL before dialling. The
// subscription URL was checked when it was created, but a hostname can be
// re-pointed at a private address afterwards; the dialer's Control hook is the
// real guard and this early check keeps an obviously-bad row from consuming a
// retry cycle. HTTPS only: a webhook payload contains customer conversation
// data and must not travel in clear text.
func webhookDeliveryTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("无法解析的 URL")
	}
	if u.Scheme != "https" {
		return nil, errors.New("仅允许 https")
	}
	// rag.ValidateFetchURL enforces the public-host pre-check (it permits http,
	// hence the https check above).
	if _, err := rag.ValidateFetchURL(raw); err != nil {
		return nil, err
	}
	return u, nil
}

// validateWebhookURL is the create-time check: structural only. The DNS-based
// public-address check runs at delivery time (the host may not resolve yet when
// the operator saves it, and a failed DNS lookup must not reject a valid setup).
func validateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("url 不能为空")
	}
	if len(raw) > 500 {
		return errors.New("url 过长（最多 500 字符）")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("URL 必须是有效的 https 地址")
	}
	if u.User != nil {
		return errors.New("URL 不能包含用户名或密码")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("URL 不能指向本机")
	}
	if ip := net.ParseIP(host); ip != nil && !rag.IsPublicIP(ip) {
		return errors.New("URL 不能指向内网地址")
	}
	return nil
}

// sealWebhookSecret seals a subscription signing secret with the platform
// sealer, matching every other credential at rest. Empty secrets stay empty
// (the subscription simply signs nothing).
func (a *App) sealWebhookSecret(secret string) string {
	if a.Sealer == nil || secret == "" {
		return secret
	}
	enc, err := a.Sealer.Encrypt(secret)
	if err != nil {
		return secret
	}
	return enc
}

// openWebhookSecret decrypts a stored signing secret. Rows written before
// secrets were sealed hold plaintext, which passes through unchanged.
func (a *App) openWebhookSecret(stored string) string {
	if a.Sealer == nil || stored == "" {
		return stored
	}
	return a.Sealer.DecryptOrKeep(stored)
}

// emitWebhookEvent queues one outbound webhook event. Best effort by design: a
// merchant's integration must never fail the customer-facing write that
// produced the event, so the failure is logged (and the event is not retried —
// there is nothing to retry from; the queue row was never written).
func (a *App) emitWebhookEvent(ctx context.Context, tenantID int32, event string, data map[string]any) {
	if err := webhookout.Emit(ctx, a.DB, tenantID, event, platform.NewUUID(), data); err != nil && a.Logger != nil {
		a.Logger.Warn("webhook event not queued", "event", event, "tenant_id", tenantID, "error", err.Error())
	}
}

// listWebhookDeliveries — GET /api/v1/webhooks/subscriptions/{id}/deliveries.
// The queue is the audit trail an integrator reads when their endpoint "did not
// receive anything"; it is scoped to the owning tenant like every subscription
// call.
func (a *App) listWebhookDeliveries(w http.ResponseWriter, r *http.Request, subscriptionID int32) (any, error) {
	user, _ := UserFrom(r)
	var owner int32
	if err := a.DB.QueryRow(r.Context(),
		"SELECT user_id FROM webhook_subscriptions WHERE subscription_id = $1 AND user_id = $2",
		subscriptionID, user.Tenant()).Scan(&owner); err != nil {
		return nil, ErrNotFound("订阅不存在")
	}
	rows, err := a.DB.Query(r.Context(),
		`SELECT delivery_id, event, COALESCE(event_id, ''), status, COALESCE(status_code, 0),
		        attempts, COALESCE(error, ''), created_at, last_attempt_at, delivered_at
		   FROM webhook_deliveries
		  WHERE subscription_id = $1 AND user_id = $2
		  ORDER BY created_at DESC
		  LIMIT 100`,
		subscriptionID, user.Tenant())
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		var (
			deliveryID  int64
			event       string
			eventID     string
			status      string
			statusCode  int
			attempts    int32
			errText     string
			createdAt   time.Time
			lastAttempt *time.Time
			deliveredAt *time.Time
		)
		if err := rows.Scan(&deliveryID, &event, &eventID, &status, &statusCode, &attempts, &errText, &createdAt, &lastAttempt, &deliveredAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"delivery_id": deliveryID, "event": event, "event_id": eventID, "status": status,
			"status_code": statusCode, "attempts": attempts, "error": errText,
			"created_at": createdAt, "last_attempt_at": lastAttempt, "delivered_at": deliveredAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

// retryWebhookDelivery — POST /api/v1/webhooks/deliveries/{id}/retry. Queues a
// finished delivery again (the integrator fixed their endpoint and does not
// want to wait for a natural retry). Attempts restart so the manual retry gets
// the full backoff budget.
func (a *App) retryWebhookDelivery(w http.ResponseWriter, r *http.Request, deliveryID int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(),
		`UPDATE webhook_deliveries d
		    SET status = 'pending', success = false, attempts = 0,
		        next_attempt_at = NOW(), locked_at = NULL, error = ''
		   FROM webhook_subscriptions s
		  WHERE d.subscription_id = s.subscription_id
		    AND d.delivery_id = $1
		    AND s.user_id = $2
		    AND d.status <> 'delivering'`,
		deliveryID, user.Tenant())
	if err != nil {
		return nil, ErrInternal("重新排队失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("投递记录不存在或正在投递中")
	}
	return map[string]string{"message": "已重新排队"}, nil
}
