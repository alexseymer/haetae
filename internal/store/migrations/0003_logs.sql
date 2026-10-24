-- Log templates are shapes of log lines ("GET /api/items/<NUM> <NUM> <DUR>").
-- The control plane judges info and debug templates: routine ones become green
-- and agents stop sending them. Events hold merged, redacted lines with counts;
-- no raw log stream is stored (hosted mode keeps raw logs on the customer host).
--
-- Judgements are per HOST: a host can only make its own lines skippable, so a
-- compromised host cannot silence other hosts' logs by emitting look-alike lines.
CREATE TABLE log_templates (
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  template_id text NOT NULL,
  level smallint NOT NULL,
  template text NOT NULL,
  sample text NOT NULL,
  first_seen timestamptz NOT NULL DEFAULT now(),
  last_seen timestamptz NOT NULL DEFAULT now(),
  occurrences bigint NOT NULL DEFAULT 0,
  windows int NOT NULL DEFAULT 0,
  verdict text NOT NULL DEFAULT 'new' CHECK (verdict IN ('new', 'green', 'watch')),
  judged_at timestamptz,
  PRIMARY KEY (host_id, template_id)
);

CREATE TABLE log_events (
  id bigserial PRIMARY KEY,
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  container_id text NOT NULL,
  template_id text NOT NULL,
  level smallint NOT NULL,
  sample text NOT NULL,
  count int NOT NULL,
  first_ts timestamptz NOT NULL,
  last_ts timestamptz NOT NULL
);
CREATE INDEX log_events_host_ts ON log_events (org_id, host_id, last_ts DESC);
CREATE INDEX log_events_problems ON log_events (org_id, last_ts DESC) WHERE level >= 4;

CREATE TABLE log_stats (
  host_id uuid PRIMARY KEY REFERENCES hosts(id),
  org_id uuid NOT NULL REFERENCES orgs(id),
  lines_in bigint NOT NULL DEFAULT 0,
  lines_skipped bigint NOT NULL DEFAULT 0,
  events_out bigint NOT NULL DEFAULT 0,
  redaction_failures bigint NOT NULL DEFAULT 0,
  lines_dropped bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);
