-- M8 AI operator: per-organisation settings, evidence, usage and action audit.

ALTER TABLE org_settings
  ADD COLUMN ai_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN autonomy text NOT NULL DEFAULT 'report' CHECK (autonomy IN ('report', 'suggest', 'act')),
  ADD COLUMN ai_daily_token_cap bigint NOT NULL DEFAULT 200000 CHECK (ai_daily_token_cap >= 0);

-- One row per tool result an AI conversation used. Statements cite these ids.
CREATE TABLE ai_evidence (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  conversation_id uuid NOT NULL,
  seq int NOT NULL,
  kind text NOT NULL CHECK (kind IN ('hosts', 'containers', 'metrics', 'events', 'deploys', 'findings', 'logs')),
  trusted boolean NOT NULL,
  subject_type text NOT NULL DEFAULT '',
  subject_id text NOT NULL DEFAULT '',
  title text NOT NULL,
  summary text NOT NULL DEFAULT '',
  data jsonb NOT NULL DEFAULT '{}',
  link text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (conversation_id, seq)
);
CREATE INDEX ai_evidence_org_created_idx ON ai_evidence (org_id, created_at DESC);

-- Token usage per organisation and UTC day, for the daily cap.
CREATE TABLE ai_usage (
  org_id uuid NOT NULL REFERENCES orgs(id),
  day date NOT NULL,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  calls int NOT NULL DEFAULT 0,
  PRIMARY KEY (org_id, day)
);

-- Audit of every action the operator proposed or took.
CREATE TABLE ai_actions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid REFERENCES hosts(id),
  container_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('restart_container')),
  level text NOT NULL CHECK (level IN ('report', 'suggest', 'act')),
  origin text NOT NULL CHECK (origin IN ('proposed', 'approved', 'auto')),
  outcome text NOT NULL CHECK (outcome IN ('refused', 'awaiting_approval', 'sent', 'succeeded', 'failed')),
  reason text NOT NULL DEFAULT '',
  evidence_ids uuid[] NOT NULL DEFAULT '{}',
  detail text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_actions_org_created_idx ON ai_actions (org_id, created_at DESC);
CREATE INDEX ai_actions_container_idx ON ai_actions (container_id, created_at DESC);

DO $$
DECLARE
  t text;
  cond text := 'nullif(current_setting(''app.org_id'', true), '''')::uuid';
BEGIN
  FOREACH t IN ARRAY ARRAY['ai_evidence', 'ai_usage', 'ai_actions'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (org_id = %s) WITH CHECK (org_id = %s)',
      t, cond, cond);
  END LOOP;
END
$$;

GRANT SELECT ON ai_evidence, ai_usage, ai_actions TO haetae_app;
