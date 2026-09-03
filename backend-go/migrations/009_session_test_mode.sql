-- Internal AI trials use the same RAG path as customer chats, but must not
-- appear in the operator Inbox.
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS is_test BOOLEAN NOT NULL DEFAULT FALSE;
