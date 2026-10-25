-- M6: alerts, incidents, chat channels, briefings. See docs/design/m6-alerts-chat.md.

CREATE TABLE org_settings (
  org_id uuid PRIMARY KEY REFERENCES orgs(id),
  time_zone text NOT NULL DEFAULT 'UTC',
  briefing_hour int NOT NULL DEFAULT 8 CHECK (briefing_hour BETWEEN 0 AND 23),
  cpu_pct int NOT NULL DEFAULT 90 CHECK (cpu_pct BETWEEN 1 AND 100),
  mem_pct int NOT NULL DEFAULT 90 CHECK (mem_pct BETWEEN 1 AND 100),
  disk_pct int NOT NULL DEFAULT 90 CHECK (disk_pct BETWEEN 1 AND 100),
  sustained_s int NOT NULL DEFAULT 300 CHECK (sustained_s BETWEEN 10 AND 86400),
  offline_after_s int NOT NULL DEFAULT 120 CHECK (offline_after_s BETWEEN 30 AND 86400),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE incidents (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  title text NOT NULL,
  severity text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
  state text NOT NULL CHECK (state IN ('open', 'resolved')) DEFAULT 'open',
  opened_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);

CREATE TABLE alerts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  rule text NOT NULL CHECK (rule IN (
    'crash_loop', 'oom_kill', 'unhealthy', 'host_offline', 'cpu_high',
    'mem_high', 'disk_high', 'deploy_failed', 'new_cve'
  )),
  container text NOT NULL DEFAULT '',
  severity text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
  state text NOT NULL CHECK (state IN ('firing', 'resolved')) DEFAULT 'firing',
  dedupe_key text NOT NULL,
  summary text NOT NULL,
  evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
  first_at timestamptz NOT NULL DEFAULT now(),
  last_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);

CREATE TABLE mutes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid REFERENCES hosts(id),
  rule text NOT NULL DEFAULT '',
  until timestamptz NOT NULL,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE chat_accounts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  user_id uuid NOT NULL REFERENCES users(id),
  channel text NOT NULL CHECK (channel IN ('web', 'telegram', 'slack', 'email', 'signal')),
  external_id text NOT NULL,
  verified_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (channel, external_id)
);

CREATE TABLE chat_link_codes (
  code_hash bytea PRIMARY KEY,
  org_id uuid NOT NULL REFERENCES orgs(id),
  user_id uuid NOT NULL REFERENCES users(id),
  channel text NOT NULL CHECK (channel IN ('telegram', 'slack', 'email', 'signal')),
  expires_at timestamptz NOT NULL,
  used_at timestamptz
);

CREATE TABLE chat_messages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  channel text NOT NULL CHECK (channel IN ('web', 'telegram', 'slack', 'email', 'signal')),
  account_id uuid REFERENCES chat_accounts(id) ON DELETE SET NULL,
  direction text NOT NULL CHECK (direction IN ('in', 'out')),
  kind text NOT NULL CHECK (kind IN ('incident', 'briefing', 'answer', 'text', 'action')),
  body text NOT NULL,
  actions jsonb NOT NULL DEFAULT '[]'::jsonb,
  evidence jsonb NOT NULL DEFAULT '[]'::jsonb,
  incident_id uuid REFERENCES incidents(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz,
  attempts int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  delivery_error text NOT NULL DEFAULT ''
);

CREATE TABLE briefings (
  org_id uuid NOT NULL REFERENCES orgs(id),
  local_date date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, local_date)
);

CREATE UNIQUE INDEX alerts_one_firing ON alerts (org_id, dedupe_key) WHERE state = 'firing';
CREATE INDEX alerts_incident_idx ON alerts (incident_id);
CREATE INDEX alerts_org_state_idx ON alerts (org_id, state, last_at DESC);
CREATE INDEX incidents_org_state_idx ON incidents (org_id, state, updated_at DESC);
CREATE UNIQUE INDEX incidents_one_open ON incidents (host_id) WHERE state = 'open';
CREATE INDEX mutes_org_until_idx ON mutes (org_id, until);
CREATE INDEX chat_accounts_org_idx ON chat_accounts (org_id, user_id);
CREATE INDEX chat_messages_pending_idx ON chat_messages (next_attempt_at)
  WHERE direction = 'out' AND delivered_at IS NULL;
CREATE INDEX chat_messages_org_created_idx ON chat_messages (org_id, created_at DESC);

DO $$
DECLARE
  t text;
  cond text := 'nullif(current_setting(''app.org_id'', true), '''')::uuid';
BEGIN
  FOREACH t IN ARRAY ARRAY['org_settings', 'incidents', 'alerts', 'mutes',
      'chat_accounts', 'chat_link_codes', 'chat_messages', 'briefings'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (org_id = %s) WITH CHECK (org_id = %s)',
      t, cond, cond);
  END LOOP;
END
$$;

-- The request-scoped role reads everything it shows, and writes only what a
-- signed-in user may change. The background evaluator, notifier and channel
-- bridges run as the server role (they are not user requests).
GRANT SELECT ON org_settings, incidents, alerts, mutes, chat_accounts, chat_messages, briefings TO haetae_app;
GRANT INSERT, UPDATE ON org_settings TO haetae_app;
GRANT INSERT ON mutes TO haetae_app;
GRANT UPDATE (until) ON mutes TO haetae_app;
GRANT DELETE ON mutes TO haetae_app;
GRANT INSERT (id, org_id, channel, account_id, direction, kind, body, created_at) ON chat_messages TO haetae_app;
-- chat_link_codes (hash of a secret) is never readable by the request role;
-- codes are created and consumed by the server role only.
