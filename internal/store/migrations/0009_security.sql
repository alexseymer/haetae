-- M7: inventory, vulnerability findings, misconfigurations, outbound
-- connections and image update decisions. See docs/design/m7-security.md.

CREATE TABLE scans (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  container_id text NOT NULL DEFAULT '',
  image text NOT NULL DEFAULT '',
  image_id text NOT NULL DEFAULT '',
  trigger text NOT NULL CHECK (trigger IN ('start', 'deploy', 'daily', 'forced', 'candidate')),
  status text NOT NULL CHECK (status IN ('pending', 'ok', 'failed', 'timeout', 'oom', 'unsupported')),
  error text NOT NULL DEFAULT '',
  tool_version text NOT NULL DEFAULT '',
  package_count int NOT NULL DEFAULT 0,
  peak_memory_bytes bigint NOT NULL DEFAULT 0,
  source_ref text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz
);

-- One SBOM per (org, image id), shared by every container running that image.
CREATE TABLE sboms (
  org_id uuid NOT NULL REFERENCES orgs(id),
  image_id text NOT NULL,
  distro_name text NOT NULL DEFAULT '',
  distro_version text NOT NULL DEFAULT '',
  distro_id_like text NOT NULL DEFAULT '',
  package_count int NOT NULL DEFAULT 0,
  last_scan_id uuid REFERENCES scans(id) ON DELETE SET NULL,
  scanned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, image_id)
);

CREATE TABLE sbom_packages (
  org_id uuid NOT NULL,
  image_id text NOT NULL,
  name text NOT NULL,
  version text NOT NULL,
  type text NOT NULL DEFAULT '',
  purl text NOT NULL DEFAULT '',
  PRIMARY KEY (org_id, image_id, name, version, type),
  FOREIGN KEY (org_id, image_id) REFERENCES sboms(org_id, image_id) ON DELETE CASCADE
);

-- Matching state per SBOM: which database build it was last matched against.
-- A matching failure is recorded as 'failed' and shown as "scan failed".
-- 'pending' means the SBOM is stored but not matched yet (queue full, database
-- not ready); the periodic re-match picks up pending and missing rows.
CREATE TABLE image_match_state (
  org_id uuid NOT NULL,
  image_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('ok', 'failed', 'pending')),
  error text NOT NULL DEFAULT '',
  db_built_at timestamptz,
  matched_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, image_id),
  FOREIGN KEY (org_id, image_id) REFERENCES sboms(org_id, image_id) ON DELETE CASCADE
);

CREATE TABLE image_findings (
  org_id uuid NOT NULL,
  image_id text NOT NULL,
  vuln_id text NOT NULL,
  package text NOT NULL,
  version text NOT NULL,
  pkg_type text NOT NULL DEFAULT '',
  fixed_version text NOT NULL DEFAULT '',
  severity text NOT NULL CHECK (severity IN ('unknown', 'negligible', 'low', 'medium', 'high', 'critical')),
  description text NOT NULL DEFAULT '',
  urls jsonb NOT NULL DEFAULT '[]'::jsonb,
  first_seen timestamptz NOT NULL DEFAULT now(),
  last_seen timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, image_id, vuln_id, package, version, pkg_type),
  FOREIGN KEY (org_id, image_id) REFERENCES sboms(org_id, image_id) ON DELETE CASCADE
);

-- Server-wide vulnerability database state (single row).
CREATE TABLE vuln_db (
  id int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  source_url text NOT NULL DEFAULT '',
  built_at timestamptz,
  checked_at timestamptz,
  updated_at timestamptz,
  status text NOT NULL DEFAULT 'none' CHECK (status IN ('none', 'ok', 'failed', 'disabled')),
  error text NOT NULL DEFAULT ''
);
INSERT INTO vuln_db (id) VALUES (1);

CREATE TABLE misconfigs (
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  container_id text NOT NULL DEFAULT '',
  kind text NOT NULL,
  severity text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
  detail text NOT NULL DEFAULT '',
  first_seen timestamptz NOT NULL DEFAULT now(),
  last_seen timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  PRIMARY KEY (host_id, container_id, kind, detail)
);

CREATE TABLE host_security (
  host_id uuid PRIMARY KEY REFERENCES hosts(id),
  org_id uuid NOT NULL REFERENCES orgs(id),
  conntrack_state text NOT NULL DEFAULT 'unknown' CHECK (conntrack_state IN ('unknown', 'ok', 'degraded', 'unavailable')),
  conntrack_reason text NOT NULL DEFAULT '',
  not_monitored jsonb NOT NULL DEFAULT '[]'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE outbound_conns (
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  container_id text NOT NULL,
  dst_ip text NOT NULL,
  dst_port int NOT NULL,
  proto text NOT NULL,
  first_seen timestamptz NOT NULL DEFAULT now(),
  last_seen timestamptz NOT NULL DEFAULT now(),
  count bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (host_id, container_id, dst_ip, dst_port, proto)
);

CREATE TABLE image_updates (
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  container_id text NOT NULL,
  image text NOT NULL,
  running_digest text NOT NULL DEFAULT '',
  candidate_digest text NOT NULL DEFAULT '',
  candidate_created timestamptz,
  -- When the server first saw candidate_digest (the publisher controls
  -- 'created', so the age gate counts from the later of the two).
  candidate_first_seen timestamptz,
  decision text NOT NULL CHECK (decision IN ('current', 'wait', 'eligible', 'eligible_critical_fix', 'unknown')),
  reason text NOT NULL DEFAULT '',
  decided_at timestamptz NOT NULL DEFAULT now(),
  -- Pull bookkeeping: the loop sends PullImage once per candidate digest and
  -- retries a failed send at most every 6 hours.
  pulled_digest text NOT NULL DEFAULT '',
  pulled_at timestamptz,
  pull_attempt_digest text NOT NULL DEFAULT '',
  pull_attempted_at timestamptz,
  PRIMARY KEY (host_id, container_id)
);

-- Previous new_cve evaluation time per org: only findings first seen after it
-- (and after the container started) alert. Survives restarts.
CREATE TABLE cve_eval_watermark (
  org_id uuid PRIMARY KEY REFERENCES orgs(id),
  evaluated_at timestamptz NOT NULL
);

ALTER TABLE org_settings ADD COLUMN auto_pull boolean NOT NULL DEFAULT false;

CREATE INDEX scans_org_created_idx ON scans (org_id, created_at DESC);
CREATE INDEX scans_host_container_idx ON scans (host_id, container_id, created_at DESC);
CREATE INDEX image_findings_sev_idx ON image_findings (org_id, severity, image_id);
CREATE INDEX misconfigs_org_open_idx ON misconfigs (org_id, host_id) WHERE resolved_at IS NULL;
CREATE INDEX outbound_conns_last_idx ON outbound_conns (host_id, last_seen DESC);

DO $$
DECLARE
  t text;
  cond text := 'nullif(current_setting(''app.org_id'', true), '''')::uuid';
BEGIN
  FOREACH t IN ARRAY ARRAY['scans', 'sboms', 'sbom_packages', 'image_match_state',
      'image_findings', 'misconfigs', 'host_security', 'outbound_conns',
      'image_updates', 'cve_eval_watermark'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (org_id = %s) WITH CHECK (org_id = %s)',
      t, cond, cond);
  END LOOP;
END
$$;

-- The request-scoped role only reads (the dashboard); all writes come from the
-- gateway, the matcher and the database refresher running as the server role.
GRANT SELECT ON scans, sboms, sbom_packages, image_match_state, image_findings,
  misconfigs, host_security, outbound_conns, image_updates, vuln_db, cve_eval_watermark TO haetae_app;
