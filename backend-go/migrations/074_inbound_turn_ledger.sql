-- 074_inbound_turn_ledger.sql — one row per inbound event, so a retry cannot
-- charge the tenant twice or answer the customer twice.
--
-- platform_inbound_events is retried up to maxAttempts times, and 057 already
-- made the *customer* message idempotent per event (idx_chat_messages_inbound_event).
-- Two effects still re-ran on every attempt:
--   * usage.ConsumeMessageQuota incremented messages_used again, so one customer
--     message consumed two (or five) from a paying tenant's monthly quota;
--   * stagePersistAndDeliver inserted a second model reply with a new
--     message_id. platform_outbox dedupes on chat_message_id, so the new id also
--     meant a new outbox row: the customer received the same answer again.
--
-- Retention: the row lives and dies with its inbound event (ON DELETE CASCADE),
-- and cmd/retention -inbound-events-days already deletes completed
-- platform_inbound_events older than 30 days, so the ledger needs no sweeper of
-- its own. It also cascades from users, so removing a tenant removes its rows.
--
-- The row is claimed by bill-message (quota_charged = true, after the quota
-- increment committed in the same transaction) and completed by
-- persist-and-deliver (model_message_id). persist-and-deliver reads it FOR
-- UPDATE, so the two attempts that can exist for one event — claimInbound
-- re-claims a lock older than five minutes while the original is still running —
-- serialise instead of racing.
--
-- quota_charged = false is the other half of the claim: it records that this
-- turn hit the quota wall and was handed to a human, so a retry of the same
-- event repeats the handoff instead of answering an over-quota tenant for free.
-- (The column was a TIMESTAMPTZ that nobody ever read, which is how a retry came
-- to treat a refusal as a completed charge.)
-- user_id is carried even though the row is keyed by event: the tenant gate
-- (internal/tenantscope) reads a tenant column to prove a statement scoped, and
-- a ledger of "this tenant was charged for this turn" should name the tenant it
-- charged. ON DELETE CASCADE keeps the row tied to its owner's lifetime.
CREATE TABLE IF NOT EXISTS inbound_turn_ledger (
    event_id BIGINT PRIMARY KEY REFERENCES platform_inbound_events(event_id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    quota_charged BOOLEAN NOT NULL,
    model_message_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The primary key is event_id, so the users cascade (deleting a tenant) had to
-- scan the whole ledger to find the rows to remove. One row per inbound event
-- makes that scan the size of platform_inbound_events for a busy tenant.
CREATE INDEX IF NOT EXISTS idx_inbound_turn_ledger_user ON inbound_turn_ledger (user_id);
