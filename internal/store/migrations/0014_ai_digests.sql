-- AI digests: log summaries and the security digest. See docs/contributors/design/m8-operator.md.

-- Whether the scheduled daily digest may be posted to chat. On by default; a
-- quiet day posts nothing either way.
ALTER TABLE org_settings ADD COLUMN ai_digest_enabled boolean NOT NULL DEFAULT true;

-- The scheduled digest is a chat message of its own kind.
ALTER TABLE chat_messages DROP CONSTRAINT IF EXISTS chat_messages_kind_check;
ALTER TABLE chat_messages ADD CONSTRAINT chat_messages_kind_check
  CHECK (kind IN ('incident', 'briefing', 'answer', 'text', 'action', 'digest'));

-- Findings first seen after the watermark are "new" in the next security
-- digest. Only the scheduled digest moves it.
CREATE TABLE security_digest_state (
  org_id uuid PRIMARY KEY REFERENCES orgs(id),
  watermark timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row per (org, local day): the exactly-once claim of the daily digest,
-- with a lease so a crash between claim and post is retried.
CREATE TABLE digest_runs (
  org_id uuid NOT NULL REFERENCES orgs(id),
  local_date date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  lease_until timestamptz,
  done_at timestamptz,
  posted boolean NOT NULL DEFAULT false,
  PRIMARY KEY (org_id, local_date)
);

DO $$
DECLARE
  t text;
  cond text := 'nullif(current_setting(''app.org_id'', true), '''')::uuid';
BEGIN
  FOREACH t IN ARRAY ARRAY['security_digest_state', 'digest_runs'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (org_id = %s) WITH CHECK (org_id = %s)',
      t, cond, cond);
  END LOOP;
END
$$;
