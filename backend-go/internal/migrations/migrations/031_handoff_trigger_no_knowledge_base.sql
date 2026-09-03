-- Empty-knowledge-base auto-escalation reuses the handoff request table with
-- a dedicated trigger so agents/analytics can tell these apart from
-- classifier or customer-initiated handoffs.
ALTER TYPE human_handoff_trigger ADD VALUE IF NOT EXISTS 'no_knowledge_base';
