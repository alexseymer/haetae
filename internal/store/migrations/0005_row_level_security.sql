-- Tenant isolation enforced by the database.
--
-- User-facing queries run as the role haetae_app (see Store.ForOrg) after
-- setting app.org_id for the transaction. That role is subject to Row-Level
-- Security: it can only see and change rows of its own organisation, even if a
-- query forgets its WHERE clause. The server's own connection role (owner or
-- superuser) bypasses RLS; it is used by the agent gateway and background jobs,
-- which always name the organisation explicitly.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'haetae_app') THEN
    CREATE ROLE haetae_app NOLOGIN NOINHERIT;
  END IF;
  -- Let the connecting role switch to it (superusers need no grant).
  EXECUTE format('GRANT haetae_app TO %I', current_user);
END
$$;

GRANT USAGE ON SCHEMA public TO haetae_app;

DO $$
DECLARE
  t text;
  cond text := 'nullif(current_setting(''app.org_id'', true), '''')::uuid';
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'hosts', 'enrollment_tokens', 'containers',
    'host_metrics', 'container_metrics',
    'host_metrics_1m', 'host_metrics_1h', 'container_metrics_1m', 'container_metrics_1h',
    'log_templates', 'log_events', 'log_stats',
    'users', 'sessions'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (org_id = %s) WITH CHECK (org_id = %s)',
      t, cond, cond);
    EXECUTE format('GRANT SELECT ON %I TO haetae_app', t);
  END LOOP;

  ALTER TABLE orgs ENABLE ROW LEVEL SECURITY;
  EXECUTE format('CREATE POLICY tenant_isolation ON orgs USING (id = %s) WITH CHECK (id = %s)', cond, cond);
  GRANT SELECT ON orgs TO haetae_app;
END
$$;

-- What signed-in users may write. Everything else is read-only for them;
-- agents' data arrives through the server's own role.
GRANT INSERT ON enrollment_tokens TO haetae_app;
GRANT INSERT, UPDATE ON users TO haetae_app;
