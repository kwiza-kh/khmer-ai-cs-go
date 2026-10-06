-- 071: invite history — per-link use cap, revoke-as-mark, and a durable log.
--
-- 070 made one invite equal one seat: used_by_user_id/used_at lived on the row
-- and DELETE destroyed the link, so an owner could not answer "which link did
-- Bopha use, and when does it stop working?". A multi-use link needs one row
-- per acceptance (team_invite_uses), which doubles as the history log:
--
--   * username/display_name are snapshotted at acceptance, so the log survives
--     a later rename or account deletion (user_id goes NULL, the name does not);
--   * max_uses caps how many people one link may seat (default 1 = the old
--     single-use behaviour);
--   * revoking marks revoked_at instead of deleting, so the row stays readable;
--   * expires_at already existed; the API now lets the owner choose it.
--
-- Acceptance remains all-or-nothing: the invite row and the uses are written in
-- the same transaction as the agent_teams edge, so a rejected accept (seat full,
-- already seated) leaves the link usable.

ALTER TABLE team_invites
    ADD COLUMN IF NOT EXISTS max_uses INT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'team_invites_max_uses_positive'
    ) THEN
        ALTER TABLE team_invites
            ADD CONSTRAINT team_invites_max_uses_positive CHECK (max_uses >= 1);
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS team_invite_uses (
    use_id       BIGSERIAL PRIMARY KEY,
    invite_id    BIGINT NOT NULL REFERENCES team_invites(invite_id) ON DELETE CASCADE,
    user_id      INT REFERENCES users(user_id) ON DELETE SET NULL,
    username     TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    used_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_team_invite_uses_invite
    ON team_invite_uses (invite_id, used_at);

-- One account cannot consume the same link twice. The agent_teams unique index
-- already blocks a second team; this keeps the history honest under a race too.
CREATE UNIQUE INDEX IF NOT EXISTS uq_team_invite_uses_invite_user
    ON team_invite_uses (invite_id, user_id) WHERE user_id IS NOT NULL;

-- Carry over anything 070 wrote (production is empty, a dev database may not be).
INSERT INTO team_invite_uses (invite_id, user_id, username, used_at)
SELECT i.invite_id, i.used_by_user_id, COALESCE(u.username, ''), COALESCE(i.used_at, i.created_at)
FROM team_invites i
JOIN users u ON u.user_id = i.used_by_user_id
WHERE i.used_by_user_id IS NOT NULL
ON CONFLICT DO NOTHING;

-- used_by_user_id / used_at are deliberately LEFT IN PLACE. Dropping them would
-- make this migration non-rollback-safe: the previous binary (7ec1cf9) writes
-- them on every acceptance, so a binary rollback would fail with "column does
-- not exist". They are dead weight for the new code and cost two nullable
-- columns; a later migration can drop them once this release is old enough that
-- nobody rolls back to it.
