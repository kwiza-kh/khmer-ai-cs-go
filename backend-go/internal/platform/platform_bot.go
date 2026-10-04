// Package platform — the operator-owned Telegram bot.
//
// Distinct from every other bot in the system. The ones in platform_configs
// and telegram_notify_settings belong to individual merchants; this one is the
// operator's, there is exactly one, and it serves the whole installation:
// operational alerts, merchant account-linking, the admin command console and
// the merchant support inbox all arrive here.
//
// Its token can message every linked merchant, so it is a platform credential
// on the order of the JWT secret.
package platform

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/textutil"
)

// alertDedupTTL — a persistent condition (Redis down, quota exhausted) must not
// page once per check. One alert per key per window, then silence until the
// condition clears and recurs.
const alertDedupTTL = 15 * time.Minute

// PlatformBotEnabled reports whether the operator bot is configured enough to
// send anything. Every platform-bot feature degrades to a no-op without it, so
// a deployment that does not want them simply leaves the token blank.
func (p *Pipeline) PlatformBotEnabled() bool {
	return strings.TrimSpace(p.Cfg.PlatformBot.Token) != ""
}

// platformAlertChat resolves where operator alerts go: the configured chat, or
// the first admin id as a fallback (a DM to that admin).
func (p *Pipeline) platformAlertChat() string {
	if c := strings.TrimSpace(p.Cfg.PlatformBot.AlertChatID); c != "" {
		return c
	}
	if len(p.Cfg.PlatformBot.AdminIDs) > 0 {
		return strconv.FormatInt(p.Cfg.PlatformBot.AdminIDs[0], 10)
	}
	return ""
}

// SendPlatformMessage delivers one message from the platform bot. Best-effort:
// a failed notification must never break the pipeline that triggered it.
func (p *Pipeline) SendPlatformMessage(ctx context.Context, chatID, text string, buttons [][2]string) error {
	_, err := p.SendPlatformMessageID(ctx, chatID, text, buttons)
	return err
}

// SendPlatformMessageID is SendPlatformMessage but returns the provider message
// id. The support relay needs it: the id of the copy we send the operator is
// the key that recognises their reply to it.
func (p *Pipeline) SendPlatformMessageID(ctx context.Context, chatID, text string, buttons [][2]string) (string, error) {
	if !p.PlatformBotEnabled() || strings.TrimSpace(chatID) == "" {
		return "", nil
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	id, err := NewTelegramClient(p.Cfg.PlatformBot.Token).SendMessage(sendCtx, chatID, text, buttons)
	if err != nil {
		p.Logger.Warn("platform bot send failed", "chat_id", chatID, "error", err.Error())
		return "", err
	}
	return id, nil
}

// PlatformAlert pages the operator about an operational problem.
//
// key identifies the CONDITION, not the occurrence — repeated alerts with the
// same key inside alertDedupTTL are dropped, so a condition that stays broken
// produces one page rather than one per check. Pass a stable key such as
// "redis-down" or "gemini-quota" and put the varying part in detail.
//
// This is called from failure paths that previously wrote only a log line
// nobody reads — the whole point being that an unmonitored service announces
// itself by going quiet.
func (p *Pipeline) PlatformAlert(ctx context.Context, key, title, detail string) {
	if !p.PlatformBotEnabled() {
		return
	}
	chatID := p.platformAlertChat()
	if chatID == "" {
		return
	}
	// Dedup rides on the rate-limit counter: IncrWindow(key, 1, ttl) reports
	// true only for the first increment inside the window. On a Redis error we
	// send anyway — a duplicate alert beats a missed one.
	//
	// Note the key this ends up under: IncrWindow adds the shared rate-limit
	// namespace itself, so the real key is "ratelimit:platform-alert:<key>".
	// PlatformAlertResolved must delete THAT name — see it for the full story.
	if p.Redis != nil {
		dedupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		first, err := p.Redis.IncrWindow(dedupCtx, "platform-alert:"+key, 1, alertDedupTTL)
		cancel()
		if err == nil && !first {
			return
		}
	}
	text := "🔴 " + title
	if detail != "" {
		text += "\n" + detail
	}
	text += "\n\nRelayChat · " + time.Now().In(PhnomPenhLoc()).Format("2006-01-02 15:04")
	if err := p.SendPlatformMessage(ctx, chatID, text, nil); err != nil {
		p.Logger.Warn("platform alert delivery failed", "key", key, "error", err.Error())
	}
}

// isQuotaExhausted reports whether a Gemini error means "stop, not retry".
//
// The API returns 429 RESOURCE_EXHAUSTED for both a rate limit and a spend or
// balance stop, and the two want opposite handling: a rate limit clears in
// seconds, an exhausted prepaid balance does not clear at all until someone
// tops it up. Anything mentioning quota, billing or an unpaid balance is
// treated as the latter.
func isQuotaExhausted(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"resource_exhausted", "quota", "billing", "prepay", "balance", "429",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// PlatformAlertResolved clears a condition's dedup key so it can page again if
// it recurs. Call from the recovery path.
func (p *Pipeline) PlatformAlertResolved(ctx context.Context, key string) {
	if p.Redis == nil {
		return
	}
	clearCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The prefix must match what PlatformAlert's IncrWindow actually created.
	// IncrWindow prefixes its own argument with "ratelimit:" (redisstore/redis.go,
	// the shared fixed-window counter namespace), so IncrWindow("platform-alert:"+key)
	// writes "ratelimit:platform-alert:<key>" — while Del, which takes a raw key,
	// was deleting the unprefixed "platform-alert:<key>". That key never existed,
	// so every recovery path left the counter in place: the condition cleared,
	// the dedup window did not, and the NEXT time the same thing broke the
	// operator was told nothing for the rest of the TTL. The asymmetry is
	// invisible at the call site because both functions take a bare string.
	_ = p.Redis.Del(clearCtx, "ratelimit:platform-alert:"+key)
}

// ============================================
// Merchant account linking
// ============================================

// linkTokenTTL — long enough for a merchant to tap through Telegram, short
// enough that a leaked link is useless.
const linkTokenTTL = 10 * time.Minute

// IssueLinkToken mints a single-use token that binds a Telegram chat to userID.
// The merchant opens t.me/<bot>?start=<token>; the bot receives
// "/start <token>" and calls RedeemLinkToken.
//
// The token proves which account started the flow, so it is minted only behind
// an authenticated request and dies on first use — the same shape as the
// one-time login codes in the SSO flows.
func (p *Pipeline) IssueLinkToken(ctx context.Context, userID int32) (string, error) {
	if p.Redis == nil {
		return "", fmt.Errorf("redis unavailable")
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	if err := p.Redis.SetString(ctx, "tg-link:"+token, strconv.Itoa(int(userID)), linkTokenTTL); err != nil {
		return "", err
	}
	return token, nil
}

// RedeemLinkToken resolves a /start token and binds chatID to its owner.
// Returns the user id on success; ok=false when the token is unknown, expired
// or already used.
func (p *Pipeline) RedeemLinkToken(ctx context.Context, token, chatID, chatTitle string) (int32, bool) {
	if p.Redis == nil || token == "" || chatID == "" {
		return 0, false
	}
	raw, err := p.Redis.GetString(ctx, "tg-link:"+token)
	if err != nil || raw == "" {
		return 0, false
	}
	// Single use: consume before doing anything else, so a replay cannot race
	// the first redemption.
	_ = p.Redis.Del(ctx, "tg-link:"+token)

	uid, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
	if err != nil {
		return 0, false
	}
	userID := int32(uid)
	// Binding switches the tenant to the platform bot: bot_token_enc is set to
	// NULL, which loadTelegramNotifyUncached reads as "use the platform bot".
	//
	// bot_token_enc = NULL must ALSO appear in the DO UPDATE branch, not just in
	// the VALUES list. A merchant who already had their own bot is exactly the
	// case this feature exists for, and they take the conflict path — omitting
	// it there made the binding report success while delivery silently kept
	// going out through the old per-tenant bot.
	if _, err := p.DB.Exec(ctx,
		"INSERT INTO telegram_notify_settings (user_id, chat_id, chat_title, bot_token_enc) "+
			"VALUES ($1,$2,$3,NULL) "+
			"ON CONFLICT (user_id) DO UPDATE SET chat_id = EXCLUDED.chat_id, "+
			"chat_title = CASE WHEN EXCLUDED.chat_title = '' THEN telegram_notify_settings.chat_title "+
			"ELSE EXCLUDED.chat_title END, "+
			"bot_token_enc = NULL, updated_at = NOW()",
		userID, chatID, chatTitle); err != nil {
		p.Logger.Warn("telegram link persist failed", "user_id", userID, "error", err.Error())
		return 0, false
	}
	InvalidateTelegramNotify(userID)
	return userID, true
}

// PlatformBotLink returns the t.me deep link a merchant taps to bind their
// chat, or "" when the bot's @handle is not configured.
func (p *Pipeline) PlatformBotLink(token string) string {
	handle := strings.TrimPrefix(strings.TrimSpace(p.Cfg.PlatformBot.PublicBotUsername), "@")
	if handle == "" || token == "" {
		return ""
	}
	return "https://t.me/" + handle + "?start=" + token
}

// BroadcastToMerchants sends an announcement to every merchant with a Telegram
// chat bound, returning how many accepted it.
//
// Runs synchronously but bounded: Telegram tolerates roughly 30 messages a
// second, and this is driven by a human typing a command, so a few hundred
// recipients finish in seconds. Pacing is deliberate — a tight loop over a
// large list would trip the bot API's flood limits and silently drop the tail.
func (p *Pipeline) BroadcastToMerchants(ctx context.Context, text string) (sent, failed int) {
	if !p.PlatformBotEnabled() || strings.TrimSpace(text) == "" {
		return 0, 0
	}
	rows, err := p.DB.Query(ctx,
		"SELECT chat_id FROM telegram_notify_settings WHERE chat_id <> '' AND notify_announcements")
	if err != nil {
		return 0, 0
	}
	var chats []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			chats = append(chats, c)
		}
	}
	readErr := rows.Err()
	rows.Close()
	// A truncated list would report "已发送 N 个商家" for an announcement whose
	// tail never received anything — and the operator cannot tell a short list
	// from a short audience. Send nothing and let them run it again.
	if readErr != nil {
		p.Logger.Warn("broadcast recipient list unreadable; sending nothing", "error", readErr.Error())
		return 0, 0
	}

	for i, chat := range chats {
		if i > 0 {
			select {
			case <-ctx.Done():
				return sent, failed
			case <-time.After(60 * time.Millisecond):
			}
		}
		if err := p.SendPlatformMessage(ctx, chat, "📣 "+text, nil); err != nil {
			failed++
			continue
		}
		sent++
	}
	return sent, failed
}

// IsPlatformAdminChat reports whether a Telegram user id is on the operator
// allow-list. Telegram asserts from.id on every update, so this doubles as an
// authentication check — but callers additionally verify the linked account
// still holds platform_admin, so revoking the role revokes the console.
func (p *Pipeline) IsPlatformAdminChat(telegramUserID int64) bool {
	for _, id := range p.Cfg.PlatformBot.AdminIDs {
		if id == telegramUserID {
			return true
		}
	}
	return false
}

// auditPlatformCommand records an operator command. The console reads across
// every tenant, so it writes to the same audit_logs trail the admin API uses.
func (p *Pipeline) auditPlatformCommand(ctx context.Context, telegramUserID int64, command, detail string) {
	_, _ = p.DB.Exec(ctx,
		"INSERT INTO audit_logs (admin_id, action, target_type, target_id, details) "+
			"VALUES (0,$1,'platform_bot',$2,$3::jsonb)",
		"telegram:"+command, strconv.FormatInt(telegramUserID, 10),
		fmt.Sprintf(`{"command":%q,"detail":%q}`, command, detail))
}

// outboundAlertThreshold — final delivery failures within alertDedupTTL before
// the operator is paged. One failure is noise; a run of them means a channel is
// broken (token revoked, provider down) and customers are not receiving replies.
const outboundAlertThreshold = 5

// alertOutboundFailures pages the operator when deliveries start failing for
// good (policy error or attempts exhausted). The count rides the rate-limit
// window; PlatformAlert's own key dedup keeps it to one page per window.
func (p *Pipeline) alertOutboundFailures(ctx context.Context, platform, detail string) {
	if p.Redis == nil {
		return
	}
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ok, err := p.Redis.IncrWindow(c, "platform-alert:outbound:"+platform, outboundAlertThreshold-1, alertDedupTTL)
	if err != nil {
		return
	}
	if ok {
		return
	}
	p.PlatformAlert(ctx, "outbound-failures-"+platform, "出站消息连续投递失败",
		fmt.Sprintf("平台 %s 在 15 分钟内失败消息数已达 %d 条阈值，客户可能收不到回复。最近错误: %s",
			platform, outboundAlertThreshold, textutil.TruncateRunes(detail, 300)))
}
