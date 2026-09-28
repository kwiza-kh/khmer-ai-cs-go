-- Export the turn CSV that cmd/jeveval consumes (`-csv turns.csv`).
--
-- A "turn" is one customer message plus the model reply that answered it, with
-- the handoff ground truth attached. The ground truth is deliberately the old
-- system's behaviour, not a correctness oracle: a handoff request created
-- within 30 minutes of the customer message counts as escalated. That is what
-- makes the numbers useful for picking thresholds and useless for claiming
-- accuracy (see docs/DEVELOPMENT.md, "Jev 校准").
--
-- Column order is load-bearing: loadTurns reads fields 3..7, so field 3 must
-- stay customer_msg, 4 reply, 5 has_match, 6 trigger, 7 user_id. Field 6 being
-- non-empty is what marks a turn escalated.
--
-- Run it with the prod DSN, e.g. over an ssh tunnel:
--   ssh -N -L 15432:127.0.0.1:5432 root@<host> &
--   psql "$DATABASE_URL" -q -f turns.sql > turns.csv
COPY (
WITH t AS (
  SELECT
    u.message_id,
    u.session_id,
    u.created_at,
    u.content AS customer_msg,
    r.content AS reply,
    -- has_match approximates "retrieval grounded this reply": the pipeline
    -- records its sources on the model row, so a non-empty sources_json is the
    -- closest thing the database still holds.
    (r.sources_json IS NOT NULL
      AND jsonb_typeof(r.sources_json) = 'array'
      AND jsonb_array_length(r.sources_json) > 0) AS has_match,
    h.trigger::text AS trigger,
    s.user_id
  FROM chat_messages u
  JOIN sessions s ON s.session_id = u.session_id
  JOIN LATERAL (
    SELECT m.content, m.sources_json
    FROM chat_messages m
    WHERE m.session_id = u.session_id AND m.role = 'model'
      AND m.created_at >= u.created_at
    ORDER BY m.created_at
    LIMIT 1
  ) r ON true
  LEFT JOIN LATERAL (
    SELECT hr.trigger
    FROM human_handoff_requests hr
    WHERE hr.session_id = u.session_id
      AND hr.created_at >= u.created_at
      AND hr.created_at <= u.created_at + INTERVAL '30 minutes'
    ORDER BY hr.created_at
    LIMIT 1
  ) h ON true
  WHERE u.role = 'user'
    AND btrim(u.content) <> ''
    AND btrim(r.content) <> ''
)
SELECT message_id, session_id, created_at, customer_msg, reply,
       CASE WHEN has_match THEN 'true' ELSE 'false' END,
       COALESCE(trigger, ''), user_id
FROM t
ORDER BY created_at
) TO STDOUT WITH (FORMAT csv, HEADER true);
