-- Migration 003 — grounding + ops (sources_json, whatsapp enum, tags,
-- business_hours, canned_responses).
--
-- HISTORICAL NOTE: as of the dockerisation pass, all of these declarations
-- were folded into 001_init.sql (which now creates them with IF NOT EXISTS).
-- This file is intentionally a no-op so the migration runner records it as
-- applied without re-executing statements that 001 already covered.
--
-- No statements to execute.
SELECT 1;
