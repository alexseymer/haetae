-- M6 hardening: opaque chat action refs, alert eval persistence, briefing enqueue
-- tracking, delivery send receipt, mute dedupe, chat message idempotency.

CREATE TABLE chat_action_refs (
  org_id uuid NOT NULL REFERENCES orgs(id),
  message_id uuid NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
  action text NOT NULL CHECK (action IN ('later', 'mute30d', 'ack')),
  ref_hash bytea NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  PRIMARY KEY (message_id, action),
  UNIQUE (ref_hash)
);

CREATE INDEX chat_action_refs_expires_idx ON chat_action_refs (expires_at)
  WHERE used_at IS NULL;

-- Evaluator trackers that must survive process restart (unhealthy-since,
-- restart baselines, resolution streaks).
CREATE TABLE alert_eval_state (
  org_id uuid NOT NULL REFERENCES orgs(id),
  kind text NOT NULL CHECK (kind IN ('unhealthy', 'restart', 'false_streak')),
  key text NOT NULL,
  first_seen timestamptz,
  restart_baseline int,
  false_streak int NOT NULL DEFAULT 0,
  meta jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, kind, key)
);

ALTER TABLE briefings ADD COLUMN enqueued_at timestamptz;
ALTER TABLE briefings ADD COLUMN lease_until timestamptz;

-- Persist Send success across process restart (AC#6).
ALTER TABLE chat_messages ADD COLUMN send_started_at timestamptz;
ALTER TABLE chat_messages ADD COLUMN sent_at timestamptz;
-- UNIQUE allows multiple NULLs; non-null keys are exactly-once (briefing fan-out).
ALTER TABLE chat_messages ADD COLUMN idempotency_key text UNIQUE;

-- Pre-hardening mute replays could leave duplicate (org, host, rule) rows;
-- collapse them (keep GREATEST(until)) before the unique index. NULL host_id
-- is included via COALESCE so org-wide mutes also dedupe.
WITH ranked AS (
  SELECT id,
    ROW_NUMBER() OVER (
      PARTITION BY org_id, COALESCE(host_id, '00000000-0000-0000-0000-000000000000'::uuid), rule
      ORDER BY until DESC, created_at ASC, id ASC
    ) AS rn,
    MAX(until) OVER (
      PARTITION BY org_id, COALESCE(host_id, '00000000-0000-0000-0000-000000000000'::uuid), rule
    ) AS max_until
  FROM mutes
)
UPDATE mutes m
SET until = ranked.max_until
FROM ranked
WHERE m.id = ranked.id AND ranked.rn = 1;

DELETE FROM mutes
WHERE id IN (
  SELECT id FROM (
    SELECT id,
      ROW_NUMBER() OVER (
        PARTITION BY org_id, COALESCE(host_id, '00000000-0000-0000-0000-000000000000'::uuid), rule
        ORDER BY until DESC, created_at ASC, id ASC
      ) AS rn
    FROM mutes
  ) d
  WHERE d.rn > 1
);

-- Unique mute key so (org, host, rule) extends until instead of duplicating.
-- COALESCE makes NULL host_id collide with itself (org-wide mute).
CREATE UNIQUE INDEX mutes_org_host_rule_uidx ON mutes (
  org_id,
  (COALESCE(host_id, '00000000-0000-0000-0000-000000000000'::uuid)),
  rule
);

DO $$
DECLARE
  t text;
  cond text := 'nullif(current_setting(''app.org_id'', true), '''')::uuid';
BEGIN
  FOREACH t IN ARRAY ARRAY['chat_action_refs', 'alert_eval_state'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (org_id = %s) WITH CHECK (org_id = %s)',
      t, cond, cond);
  END LOOP;
END
$$;

GRANT SELECT ON chat_action_refs, alert_eval_state TO haetae_app;
