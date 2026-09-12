package platform

import "strings"

// ============================================
// Merchant-facing platform-bot copy.
// ============================================
//
// The bot used to speak Chinese to everyone, which was wrong twice over: the
// product is Khmer-first, and the merchants are in Cambodia. The operator
// console stays Chinese on purpose — that audience is the RelayChat team — but
// everything a MERCHANT reads is resolved through merchantText.
//
// Language resolution order (see merchantLang):
//  1. the linked account's users.language preference
//  2. the Telegram client's own language_code, which is the only signal
//     available before an account is linked
//  3. English — same default the web dashboard uses (lib/i18n detectLang)
//
// NOTE: the Khmer strings are a first pass and deserve a native review before
// release. The English and Chinese ones are straightforward; the Khmer ones are
// written to be understood rather than to be idiomatic, and register matters for
// a message a merchant reads on their phone.

// merchantTextKeys is the full set of merchant-visible strings. Kept as a slice
// so a test can assert every language defines every key — a missing entry would
// otherwise surface as an empty Telegram message in production.
var merchantTextKeys = []string{
	"welcome",
	"link_invalid",
	"link_ok",
	"unlink_confirm",
	"unlink_done",
	"unlink_none",
	"support_received",
	"help_hint",
	"team_reply_prefix",
	"handoff_header",
	"btn_takeover",
	"btn_resolve",
	"btn_unlink",
	"cb_takeover_ok",
	"cb_resolve_ok",
	"cb_unlink_ok",
	"cb_gone",
	"digest_title",
	"digest_new",
	"digest_handoff",
	"digest_ai",
	"digest_top",
}

var merchantTexts = map[string]map[string]string{
	"en": {
		"welcome":           "👋 This is the RelayChat bot.\n\nTo connect your RelayChat account, open Settings → Telegram notifications and tap *Connect Telegram*.",
		"link_invalid":      "⚠️ That connect link is invalid or has expired. Generate a fresh one from Settings → Telegram notifications.",
		"link_ok":           "✅ Connected. RelayChat notifications for your account will arrive in this chat.",
		"unlink_confirm":    "Disconnect Telegram notifications from your RelayChat account?\n\nYou will stop receiving new-message and handoff alerts in this chat.",
		"unlink_done":       "✅ Disconnected. You will no longer receive notifications here.",
		"unlink_none":       "This Telegram account is not connected to any RelayChat account.",
		"support_received":  "📨 Got it — your message has been passed to the RelayChat team.",
		"help_hint":         "Commands: /start — connect your account, /unlink — disconnect.\nSend me anything else and it reaches the RelayChat team.",
		"team_reply_prefix": "💬 RelayChat team:\n\n",
		"handoff_header":    "🔔 A customer needs a human",
		"btn_takeover":      "🧑💼 Take over",
		"btn_resolve":       "✅ Resolve",
		"btn_unlink":        "Disconnect",
		"cb_takeover_ok":    "Taken over — the conversation is assigned to you",
		"cb_resolve_ok":     "Resolved ✅",
		"cb_unlink_ok":      "Disconnected",
		"cb_gone":           "That request no longer exists or was already handled",
		"digest_title":      "📊 Last 24 hours",
		"digest_new":        "New conversations",
		"digest_handoff":    "Handoffs",
		"digest_ai":         "Resolved by AI",
		"digest_top":        "Most asked:",
	},
	"zh": {
		"welcome":           "👋 这是 RelayChat 机器人。\n\n要连接你的 RelayChat 账号，请打开 设置 → Telegram 通知，点击 *连接 Telegram*。",
		"link_invalid":      "⚠️ 连接链接无效或已过期。请在 设置 → Telegram 通知 中重新生成。",
		"link_ok":           "✅ 已连接。你的 RelayChat 通知将发送到这个会话。",
		"unlink_confirm":    "确定要断开 RelayChat 的 Telegram 通知吗？\n\n断开后你将不再收到新消息和转人工提醒。",
		"unlink_done":       "✅ 已断开。你不会再在这里收到通知。",
		"unlink_none":       "这个 Telegram 账号尚未连接任何 RelayChat 账号。",
		"support_received":  "📨 已收到，消息已转达 RelayChat 团队。",
		"help_hint":         "命令：/start — 连接账号，/unlink — 断开连接。\n直接给我发消息也可以，会转达 RelayChat 团队。",
		"team_reply_prefix": "💬 RelayChat 团队：\n\n",
		"handoff_header":    "🔔 有客户需要人工",
		"btn_takeover":      "🧑💼 接管",
		"btn_resolve":       "✅ 解决",
		"btn_unlink":        "断开连接",
		"cb_takeover_ok":    "已接管 — 会话已分配给你",
		"cb_resolve_ok":     "已解决 ✅",
		"cb_unlink_ok":      "已断开",
		"cb_gone":           "请求不存在或已被处理",
		"digest_title":      "📊 过去 24 小时经营摘要",
		"digest_new":        "新会话",
		"digest_handoff":    "转人工",
		"digest_ai":         "AI 独立解决",
		"digest_top":        "客户最常问：",
	},
	"km": {
		"welcome":           "👋 នេះជា bot របស់ RelayChat។\n\nដើម្បីភ្ជាប់គណនី RelayChat សូមបើក ការកំណត់ → ការជូនដំណឹងតាម Telegram រួចចុច *ភ្ជាប់ Telegram*។",
		"link_invalid":      "⚠️ តំណភ្ជាប់នេះមិនត្រឹមត្រូវ ឬផុតកំណត់ហើយ។ សូមបង្កើតតំណថ្មីពី ការកំណត់ → ការជូនដំណឹងតាម Telegram។",
		"link_ok":           "✅ បានភ្ជាប់រួចរាល់។ ការជូនដំណឹង RelayChat នឹងមកដល់ក្នុងការសន្ទនានេះ។",
		"unlink_confirm":    "តើអ្នកចង់ផ្តាច់ការជូនដំណឹង Telegram ចេញពីគណនី RelayChat មែនទេ?\n\nអ្នកនឹងឈប់ទទួលការជូនដំណឹងនៅទីនេះទៀត។",
		"unlink_done":       "✅ បានផ្តាច់រួចរាល់។ អ្នកនឹងមិនទទួលការជូនដំណឹងនៅទីនេះទៀតទេ។",
		"unlink_none":       "គណនី Telegram នេះមិនទាន់ភ្ជាប់ជាមួយគណនី RelayChat ណាមួយទេ។",
		"support_received":  "📨 បានទទួលហើយ — សាររបស់អ្នកត្រូវបានផ្ញើទៅក្រុម RelayChat។",
		"help_hint":         "ពាក្យបញ្ជា៖ /start — ភ្ជាប់គណនី, /unlink — ផ្តាច់។\nផ្ញើសារធម្មតាមកក៏បាន វានឹងទៅដល់ក្រុម RelayChat។",
		"team_reply_prefix": "💬 ក្រុម RelayChat៖\n\n",
		"handoff_header":    "🔔 មានអតិថិជនត្រូវការមនុស្ស",
		"btn_takeover":      "🧑💼 ទទួលយក",
		"btn_resolve":       "✅ ដោះស្រាយរួច",
		"btn_unlink":        "ផ្តាច់",
		"cb_takeover_ok":    "បានទទួលយក — ការសន្ទនាត្រូវបានចាត់ឲ្យអ្នក",
		"cb_resolve_ok":     "ដោះស្រាយរួច ✅",
		"cb_unlink_ok":      "បានផ្តាច់រួចរាល់",
		"cb_gone":           "សំណើនេះលែងមាន ឬត្រូវបានដោះស្រាយរួចហើយ",
		"digest_title":      "📊 សង្ខេប 24 ម៉ោងចុងក្រោយ",
		"digest_new":        "ការសន្ទនាថ្មី",
		"digest_handoff":    "ផ្ទេរទៅមនុស្ស",
		"digest_ai":         "AI ដោះស្រាយដោយខ្លួនឯង",
		"digest_top":        "សំណួរដែលគេសួរច្រើន៖",
	},
}

// botLang normalizes any language tag to one of the three supported codes.
// Telegram sends RFC 5646 tags such as "km", "zh-hans" or "en-US".
func botLang(tag string) string {
	t := strings.ToLower(strings.TrimSpace(tag))
	switch {
	case strings.HasPrefix(t, "km"):
		return "km"
	case strings.HasPrefix(t, "zh"):
		return "zh"
	case strings.HasPrefix(t, "en"):
		return "en"
	}
	return "en"
}

// merchantText resolves one merchant-facing string. Falls back to English and
// then to the key itself, so a missing translation shows up as a visible token
// in the chat instead of an empty message nobody can explain.
func merchantText(lang, key string) string {
	if m, ok := merchantTexts[botLang(lang)]; ok {
		if s, ok := m[key]; ok && s != "" {
			return s
		}
	}
	if s, ok := merchantTexts["en"][key]; ok {
		return s
	}
	return key
}
