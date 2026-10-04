package api

import "khmer-ai-cs-go/internal/gemini"

// replyLanguage resolves the language of a customer-facing reply for the two
// surfaces that receive a language from the caller: the embeddable widget and
// the console chat. The channel pipeline resolves its own (it has no interface
// hint to weigh), so it does not call this.
//
// Order, highest first:
//
//  1. preference — the merchant's saved AI language (`users.language`, where
//     "auto" is stored as "" and means "follow the customer"). A deliberate
//     setting outranks everything: that is what the console's 语言偏好 offers.
//  2. the script of the message itself. This is the case that was wrong: a
//     message written in Chinese was answered entirely in Khmer, because the
//     widget's own ?lang= (default "km") was threaded through as the reply
//     language — an interface setting deciding the language the customer is
//     answered in. Observed in production 2026-10-04: "你好" → "សូមអរគុណ!
//     ប្រសិនបើមានសំណួរ សូមប្រាប់ខ្ញុំ។" (the Khmer small-talk template), and
//     "hello" → that same Khmer reply.
//  3. hint — the widget's ?lang=, used only for messages that carry no script
//     at all ("??", "123", emoji-only), where the visitor's interface language
//     is still the best available guess.
//  4. "km" — the product default.
//
// A hint never outranks the message: it describes the widget's chrome, not the
// customer. The result is always one of "km", "en", "zh" — the three languages
// HandoffAcknowledgement, JunkAcknowledgement and SmallTalkReply define, and
// the same set the widget's ?lang= validates against.
func replyLanguage(content, preference, hint string) string {
	if isReplyLanguage(preference) {
		return preference
	}
	if detected := gemini.DetectLanguage(content); detected != "" {
		return detected
	}
	if isReplyLanguage(hint) {
		return hint
	}
	return "km"
}

// isReplyLanguage reports whether lang is one of the three supported reply
// languages. Everything else (empty, "auto" from an older row, a bogus query
// parameter) means "no choice".
func isReplyLanguage(lang string) bool {
	switch lang {
	case "km", "en", "zh":
		return true
	default:
		return false
	}
}
