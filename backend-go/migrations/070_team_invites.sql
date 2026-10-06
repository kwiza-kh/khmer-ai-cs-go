-- 070: team invites — consent instead of claiming an account by user_id.
--
-- The seat flow used to be "the owner types the agent's user_id". user_id is a
-- sequential integer that no screen shows the invitee (the profile card renders
-- username/email only) and the owner's user list contains just the people
-- already seated, so the flow was unusable in-product; worse, any owner could
-- claim a freshly registered account by guessing a nearby id, because the only
-- refusals were platform_admin / already-claimed / "has tenant state". Once
-- claimed, agent_teams membership is what userInCallerTenant answers, so the
-- owner could then change that stranger's role or disable the account while the
-- victim simultaneously stopped being their own tenant's owner.
--
-- An invite is consent: the owner mints a one-time token, the invitee accepts it
-- while logged in, and only the caller's own user_id is ever bound. Only the
-- SHA-256 of the token is stored, so a leaked database does not yield usable
-- invite links — the link is shown once at creation, the same "save it now"
-- contract as the API-key surface.
--
-- A pending invite holds no seat: checkSeatLimit runs again at acceptance, and
-- acceptance rolls back the invite if the INSERT cannot land, so a full team
-- never burns the link.
CREATE TABLE IF NOT EXISTS team_invites (
    invite_id       BIGSERIAL PRIMARY KEY,
    owner_user_id   INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    token_hash      TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL DEFAULT '',
    skills          TEXT[] NOT NULL DEFAULT '{}',
    expires_at      TIMESTAMPTZ NOT NULL,
    used_by_user_id INT REFERENCES users(user_id) ON DELETE SET NULL,
    used_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_team_invites_owner
    ON team_invites (owner_user_id, created_at DESC);
