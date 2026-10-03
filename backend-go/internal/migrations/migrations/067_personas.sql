-- 067: declarative personas.
--
-- Instructions for the model were a per-tenant system prompt plus the versioned
-- prompt history from 063. That cannot express what a customer-service deployment
-- actually needs: several named personalities per tenant, an opening line, a tool
-- whitelist per personality, and a different one for one conversation or one
-- session without editing the tenant default.
--
-- Borrowed from AstrBot's Persona (astrbot/core/db/po.py: system_prompt,
-- begin_dialogs, tools (None = all, [] = none, otherwise a whitelist), skills,
-- custom_error_message) and its three-level resolution
-- (astrbot/core/persona/persona_mgr.py: session override, then conversation, then
-- the global default).

CREATE TABLE IF NOT EXISTS personas (
    persona_id   TEXT PRIMARY KEY,
    user_id      INT REFERENCES users(user_id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    system_prompt TEXT NOT NULL,
    -- Opening turns placed ahead of the real history (they are NOT part of the
    -- system prompt: the model must see them as things already said).
    begin_dialogs JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- NULL = every tool, [] = no tools, otherwise a whitelist of tool names.
    tools        JSONB,
    -- Shown when the turn fails and the customer still needs an answer.
    error_reply  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_personas_user ON personas (user_id);

-- Which persona applies where. The most specific binding wins.
CREATE TABLE IF NOT EXISTS persona_bindings (
    binding_id SERIAL PRIMARY KEY,
    user_id    INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    persona_id TEXT NOT NULL REFERENCES personas(persona_id) ON DELETE CASCADE,
    scope      TEXT NOT NULL CHECK (scope IN ('global', 'conversation', 'session')),
    -- conversation_id / session_id for the narrower scopes, '' for global.
    target     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, scope, target)
);

CREATE INDEX IF NOT EXISTS idx_persona_bindings_lookup ON persona_bindings (user_id, scope, target);
