-- A removed (revoked) host keeps its row, but its name becomes free so the
-- same name can be enrolled again with a fresh token and certificate.
ALTER TABLE hosts DROP CONSTRAINT hosts_org_id_name_key;
CREATE UNIQUE INDEX hosts_org_name_active_idx ON hosts (org_id, name) WHERE revoked_at IS NULL;
