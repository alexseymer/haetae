CREATE TABLE orgs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE hosts (
  id uuid PRIMARY KEY,
  org_id uuid NOT NULL REFERENCES orgs(id),
  name text NOT NULL,
  hostname text NOT NULL DEFAULT '',
  os text NOT NULL DEFAULT '',
  kernel text NOT NULL DEFAULT '',
  arch text NOT NULL DEFAULT '',
  docker_version text NOT NULL DEFAULT '',
  agent_version text NOT NULL DEFAULT '',
  cert_serial text NOT NULL,
  enrolled_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz,
  connected boolean NOT NULL DEFAULT false,
  revoked_at timestamptz,
  UNIQUE (org_id, name)
);
CREATE TABLE enrollment_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES orgs(id),
  token_hash bytea NOT NULL UNIQUE,
  host_name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  host_id uuid REFERENCES hosts(id)
);
