-- ============================================
-- 056 — unify merchant notifications on the platform bot.
--
-- 040 let every merchant bring their own notification bot: create one in
-- BotFather, copy the token, paste it, message the bot, then poll getUpdates to
-- discover the chat id. 054 added a one-tap alternative through the operator
-- bot and deliberately kept the per-merchant path alive alongside it.
--
-- Running both meant every merchant-facing feature had to be built twice — and
-- the second one never was. The inline 接管/解决 buttons are only handled by the
-- per-tenant getUpdates poll, so merchants who linked through the platform bot
-- got buttons that did nothing when pressed.
--
-- This migration removes the bring-your-own-bot path. Notifications always go
-- out through the platform bot.
--
-- Existing rows are CLEARED rather than migrated, deliberately: chat_id is a
-- chat with a DIFFERENT bot. A chat id is only addressable by the bot the user
-- started, so the platform bot cannot deliver to one established with the
-- merchant's own bot — the send fails with 403 "bot can't initiate
-- conversation with a user". There is nothing to migrate; each merchant
-- re-links once, in one tap.
--
-- chat_id is cleared too, not left behind, because a stale chat id is worse
-- than none: the settings page would report "connected" while delivery
-- silently failed, and nothing would ever surface the discrepancy. The three
-- notify_* columns are preferences rather than credentials, so they survive.
--
-- bot_token_enc is KEPT (nullable, now always NULL) instead of dropped so that
-- rolling the binary back to the previous release — which still reads the
-- column — degrades to "not configured" rather than erroring on a missing
-- column. The deploy runbook treats binary rollback as a supported operation.
-- ============================================

UPDATE telegram_notify_settings SET bot_token_enc = NULL, chat_id = '', chat_title = '';

COMMENT ON COLUMN telegram_notify_settings.bot_token_enc IS
    'DEPRECATED (056): per-merchant notification bot token. Always NULL — notifications are delivered through the platform bot. Retained only so a binary rollback does not fail on a missing column.';

COMMENT ON COLUMN telegram_notify_settings.chat_id IS
    'Telegram chat bound via the platform bot. Empty = not linked.';
