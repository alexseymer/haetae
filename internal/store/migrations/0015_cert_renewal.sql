-- Client certificate renewal. A host's certificate used to be issued once at
-- enrollment and never renewed. The agent now renews it over its live
-- session, so the server records when the current one expires (for the
-- cert_expiring alert and the host API) and briefly remembers the previous
-- serial so a renewal reply that never reached the agent cannot lock it out.

ALTER TABLE hosts
  ADD COLUMN cert_expires_at timestamptz,
  ADD COLUMN prev_cert_serial text NOT NULL DEFAULT '',
  ADD COLUMN prev_cert_valid_until timestamptz;

-- Certificates issued before this migration have a 365-day lifetime from
-- enrollment (the only issue point so far) and their expiry was not stored;
-- enrolled_at + 365 days is the best known value (a few minutes late at most).
UPDATE hosts SET cert_expires_at = enrolled_at + interval '365 days';

-- New alert rule: a certificate that is about to expire has not been renewed.
ALTER TABLE alerts DROP CONSTRAINT alerts_rule_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_rule_check CHECK (rule IN (
  'crash_loop', 'oom_kill', 'unhealthy', 'host_offline', 'cpu_high',
  'mem_high', 'disk_high', 'deploy_failed', 'new_cve', 'cert_expiring'
));
