CREATE TABLE containers (
  org_id uuid NOT NULL REFERENCES orgs(id),
  host_id uuid NOT NULL REFERENCES hosts(id),
  container_id text NOT NULL,
  name text NOT NULL,
  stack text NOT NULL DEFAULT '',
  service text NOT NULL DEFAULT '',
  image text NOT NULL DEFAULT '',
  image_version text NOT NULL DEFAULT '',
  state text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT '',
  health text NOT NULL DEFAULT '',
  restart_count int NOT NULL DEFAULT 0,
  oom_killed boolean NOT NULL DEFAULT false,
  exit_code int NOT NULL DEFAULT 0,
  ports jsonb NOT NULL DEFAULT '[]',
  created_at timestamptz,
  started_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  removed_at timestamptz,
  PRIMARY KEY (host_id, container_id)
);
CREATE INDEX containers_org_idx ON containers (org_id, host_id);

CREATE TABLE host_metrics (
  org_id uuid NOT NULL, host_id uuid NOT NULL, ts timestamptz NOT NULL,
  cpu_pct real NOT NULL, load1 real NOT NULL, mem_total bigint NOT NULL, mem_avail bigint NOT NULL,
  disk_total bigint NOT NULL, disk_used bigint NOT NULL, net_rx_bps real NOT NULL, net_tx_bps real NOT NULL,
  PRIMARY KEY (host_id, ts)
) PARTITION BY RANGE (ts);

CREATE TABLE container_metrics (
  org_id uuid NOT NULL, host_id uuid NOT NULL, container_id text NOT NULL, ts timestamptz NOT NULL,
  cpu_pct real NOT NULL, mem_usage bigint NOT NULL, mem_limit bigint NOT NULL,
  net_rx_bps real NOT NULL, net_tx_bps real NOT NULL, blk_read_bps real NOT NULL, blk_write_bps real NOT NULL, pids int NOT NULL,
  PRIMARY KEY (host_id, container_id, ts)
) PARTITION BY RANGE (ts);

CREATE TABLE host_metrics_1m (
  org_id uuid NOT NULL,
  host_id uuid NOT NULL,
  ts timestamptz NOT NULL,
  cpu_pct_avg real NOT NULL,
  cpu_pct_max real NOT NULL,
  load1_avg real NOT NULL,
  load1_max real NOT NULL,
  mem_total_max bigint NOT NULL,
  mem_avail_avg real NOT NULL,
  mem_avail_max real NOT NULL,
  disk_total_max bigint NOT NULL,
  disk_used_avg real NOT NULL,
  disk_used_max real NOT NULL,
  net_rx_bps_avg real NOT NULL,
  net_rx_bps_max real NOT NULL,
  net_tx_bps_avg real NOT NULL,
  net_tx_bps_max real NOT NULL,
  samples int NOT NULL,
  PRIMARY KEY (host_id, ts)
);
CREATE INDEX host_metrics_1m_org_idx ON host_metrics_1m (org_id, host_id, ts);

CREATE TABLE host_metrics_1h (
  org_id uuid NOT NULL,
  host_id uuid NOT NULL,
  ts timestamptz NOT NULL,
  cpu_pct_avg real NOT NULL,
  cpu_pct_max real NOT NULL,
  load1_avg real NOT NULL,
  load1_max real NOT NULL,
  mem_total_max bigint NOT NULL,
  mem_avail_avg real NOT NULL,
  mem_avail_max real NOT NULL,
  disk_total_max bigint NOT NULL,
  disk_used_avg real NOT NULL,
  disk_used_max real NOT NULL,
  net_rx_bps_avg real NOT NULL,
  net_rx_bps_max real NOT NULL,
  net_tx_bps_avg real NOT NULL,
  net_tx_bps_max real NOT NULL,
  samples int NOT NULL,
  PRIMARY KEY (host_id, ts)
);
CREATE INDEX host_metrics_1h_org_idx ON host_metrics_1h (org_id, host_id, ts);

CREATE TABLE container_metrics_1m (
  org_id uuid NOT NULL,
  host_id uuid NOT NULL,
  container_id text NOT NULL,
  ts timestamptz NOT NULL,
  cpu_pct_avg real NOT NULL,
  cpu_pct_max real NOT NULL,
  mem_usage_avg real NOT NULL,
  mem_usage_max real NOT NULL,
  mem_limit_max bigint NOT NULL,
  net_rx_bps_avg real NOT NULL,
  net_rx_bps_max real NOT NULL,
  net_tx_bps_avg real NOT NULL,
  net_tx_bps_max real NOT NULL,
  blk_read_bps_avg real NOT NULL,
  blk_read_bps_max real NOT NULL,
  blk_write_bps_avg real NOT NULL,
  blk_write_bps_max real NOT NULL,
  pids_avg real NOT NULL,
  pids_max real NOT NULL,
  samples int NOT NULL,
  PRIMARY KEY (host_id, container_id, ts)
);
CREATE INDEX container_metrics_1m_org_idx ON container_metrics_1m (org_id, host_id, ts);

CREATE TABLE container_metrics_1h (
  org_id uuid NOT NULL,
  host_id uuid NOT NULL,
  container_id text NOT NULL,
  ts timestamptz NOT NULL,
  cpu_pct_avg real NOT NULL,
  cpu_pct_max real NOT NULL,
  mem_usage_avg real NOT NULL,
  mem_usage_max real NOT NULL,
  mem_limit_max bigint NOT NULL,
  net_rx_bps_avg real NOT NULL,
  net_rx_bps_max real NOT NULL,
  net_tx_bps_avg real NOT NULL,
  net_tx_bps_max real NOT NULL,
  blk_read_bps_avg real NOT NULL,
  blk_read_bps_max real NOT NULL,
  blk_write_bps_avg real NOT NULL,
  blk_write_bps_max real NOT NULL,
  pids_avg real NOT NULL,
  pids_max real NOT NULL,
  samples int NOT NULL,
  PRIMARY KEY (host_id, container_id, ts)
);
CREATE INDEX container_metrics_1h_org_idx ON container_metrics_1h (org_id, host_id, ts);
