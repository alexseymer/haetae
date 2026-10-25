-- Projects, deploy history and redacted deploy logs (see docs/design/m5-deploys.md).
--
-- Git tokens are stored sealed (AES-GCM) in git_token_sealed. The restricted
-- role haetae_app may not SELECT that column; has_git_token is a generated
-- mirror so the API can report token presence without reading the secret.
-- Deploy logs are written by the gateway (server role) and are read-only for
-- haetae_app. Project deletion uses the server role after the handler checked
-- permissions (no DELETE grant).

CREATE TABLE projects (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  name text NOT NULL,
  source_kind text NOT NULL CHECK (source_kind IN ('git', 'inline')),
  git_url text NOT NULL DEFAULT '',
  git_ref text NOT NULL DEFAULT '',
  compose_path text NOT NULL DEFAULT '',
  git_token_sealed bytea,
  has_git_token boolean GENERATED ALWAYS AS (git_token_sealed IS NOT NULL) STORED,
  inline_compose text NOT NULL DEFAULT '',
  domain text NOT NULL DEFAULT '',
  service text NOT NULL DEFAULT '',
  port int NOT NULL DEFAULT 0,
  auto_deploy boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (host_id, name)
);

CREATE TABLE deploys (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  status text NOT NULL CHECK (status IN (
    'queued', 'fetching', 'building', 'starting', 'checking',
    'healthy', 'failed', 'rolling_back', 'rolled_back', 'rollback_failed'
  )),
  commit text NOT NULL DEFAULT '',
  source_kind text NOT NULL CHECK (source_kind IN ('git', 'inline')),
  git_url text NOT NULL DEFAULT '',
  git_ref text NOT NULL DEFAULT '',
  compose_path text NOT NULL DEFAULT '',
  inline_compose text NOT NULL DEFAULT '',
  trigger text NOT NULL CHECK (trigger IN ('manual', 'push', 'rollback')),
  rollback_of uuid REFERENCES deploys(id),
  started_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz,
  url text NOT NULL DEFAULT '',
  error text NOT NULL DEFAULT '',
  message text NOT NULL DEFAULT '',
  last_event_at timestamptz
);

CREATE TABLE deploy_logs (
  deploy_id uuid NOT NULL REFERENCES deploys(id) ON DELETE CASCADE,
  seq int NOT NULL,
  stream text NOT NULL DEFAULT '',
  text text NOT NULL DEFAULT '',
  withheld boolean NOT NULL DEFAULT false,
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  PRIMARY KEY (deploy_id, seq)
);

CREATE INDEX projects_org_host_idx ON projects (org_id, host_id);
CREATE INDEX projects_org_idx ON projects (org_id);
CREATE INDEX deploys_project_created_idx ON deploys (project_id, created_at DESC);
CREATE INDEX deploys_org_host_idx ON deploys (org_id, host_id);
CREATE INDEX deploys_status_last_event_idx ON deploys (status, last_event_at)
  WHERE status IN ('queued', 'fetching', 'building', 'starting', 'checking', 'rolling_back');
CREATE UNIQUE INDEX deploys_one_active ON deploys (project_id)
  WHERE status NOT IN ('healthy', 'failed', 'rolled_back', 'rollback_failed');
CREATE INDEX deploy_logs_deploy_seq_idx ON deploy_logs (deploy_id, seq);

DO $$
DECLARE
  t text;
  cond text := 'nullif(current_setting(''app.org_id'', true), '''')::uuid';
BEGIN
  FOREACH t IN ARRAY ARRAY['projects', 'deploys', 'deploy_logs'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (org_id = %s) WITH CHECK (org_id = %s)',
      t, cond, cond);
  END LOOP;
END
$$;

-- deploys / deploy_logs: full-row SELECT (no secrets). projects: every column
-- except the sealed token.
GRANT SELECT ON deploys TO haetae_app;
GRANT SELECT ON deploy_logs TO haetae_app;
GRANT SELECT (
  id, org_id, host_id, name, source_kind, git_url, git_ref, compose_path,
  has_git_token, inline_compose, domain, service, port, auto_deploy,
  created_at, updated_at
) ON projects TO haetae_app;

GRANT INSERT (
  id, org_id, host_id, name, source_kind, git_url, git_ref, compose_path,
  git_token_sealed, inline_compose, domain, service, port, auto_deploy
) ON projects TO haetae_app;
GRANT UPDATE (
  source_kind, git_url, git_ref, compose_path, git_token_sealed, inline_compose,
  domain, service, port, auto_deploy, updated_at
) ON projects TO haetae_app;

GRANT INSERT (
  id, org_id, host_id, project_id, status, commit, source_kind, git_url,
  git_ref, compose_path, inline_compose, trigger, rollback_of, started_by,
  created_at, started_at, finished_at, url, error, message, last_event_at
) ON deploys TO haetae_app;

-- No DELETE on projects/deploys/deploy_logs for haetae_app.
-- No INSERT/UPDATE/DELETE on deploy_logs for haetae_app (gateway writes).
