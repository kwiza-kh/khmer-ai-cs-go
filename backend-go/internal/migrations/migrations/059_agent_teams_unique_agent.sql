-- Enforce the agent_teams one-owner invariant at the database layer. The
-- application check in addTeamAgent is a check-then-act pair, so two
-- concurrent claims could both commit and place one account inside two
-- tenants (both owners would then hold userInCallerTenant management rights
-- over the same user). The partial index blocks any second owner going
-- forward; the dedupe first keeps the index build safe if a race already
-- produced duplicate rows (the lowest team_id wins, matching "first claim").
DELETE FROM agent_teams a
USING agent_teams b
WHERE a.agent_user_id = b.agent_user_id
  AND a.team_id > b.team_id;

CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_teams_agent_user_id
  ON agent_teams (agent_user_id);
