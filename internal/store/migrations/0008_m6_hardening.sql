-- M6 hardening: opaque chat action refs, alert eval persistence, briefing enqueue tracking.

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

-- Unique mute key so (org, host, rule) extends until instead of duplicating.
CREATE UNIQUE INDEX mutes_org_host_rule_uidx ON mutes (org_id, host_id, rule) NULLS NOT DISTINCT;

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
