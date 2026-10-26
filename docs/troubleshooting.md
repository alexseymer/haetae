# Troubleshooting

Server logs: `docker compose --project-directory /opt/haetae logs server` (add `-f` to follow). Agent logs on a host: `journalctl -u haetae-agent`. Strings in quotes below are the real messages from the code.

## A host is not online

1. Is the agent running? `systemctl status haetae-agent`. If the installer ended with "haetae-agent failed to become active; see: journalctl -u haetae-agent", read the journal.
2. Is it enrolled? `/var/lib/haetae-agent/identity.json` must exist. If the enrollment itself failed:
   - "enrollment token is invalid, expired or already used": tokens are single use and valid 1 to 168 hours. Create a new one (**Hosts**, **Add host**).
   - "server returned a CA that does not match the pin": the `--ca-pin` does not match this server's CA. Copy the command again from the UI; check you are talking to the right server.
   - "agent: already enrolled": the state directory already has an identity; delete it only if you really want to re-enroll.
   - `agent: server URL must look like https://host:port`: wrong `--server`.
3. Network: the host must reach the gateway (default `https://<HAETAE_HOST>:8443`) outbound. `curl -vk https://<host>:8443` from the host should connect. If `HAETAE_HOST` was a private address, set a reachable name (see [install.md](install.md)) and re-enroll.
4. Agent log lines: `connected` (with `heartbeat` and `autonomy`) means the session is up; `session ended, reconnecting` with `err` and `in` shows why it dropped and that it will retry (backoff 1 s to 30 s). "host revoked" means the host record was revoked.
5. Docker access: the agent needs the Docker socket (`--docker-socket`); the `haetae-agent` user must be in the `docker` group (restart the service after adding it).
6. The Hosts page marks a host Offline after **Offline after seconds** (default 120); a `host_offline` alert fires then.

## A deploy failed

Open the project, select the deploy: the result panel shows the raw error and the log viewer shows the build log. Status meanings: **Failed** (build or fetch failed, previous version untouched), **Rolled back** ("The new version failed to start. The previous version was restored."), **Rollback failed**.

- "deploys: host is not connected": the host is offline; fix that first.
- "deploys: a deploy is already running for this project": wait for it to finish.
- `git fetch` or `git checkout` errors: repository URL must be plain `https://`; for a private repository set the **Access token**. A wrong ref gives a fetch error.
- `service "<name>" not found in compose ps` or "ingress not available": the **Service** field does not match a service in the compose file, or Caddy could not be set up.
- "domain requires service and port": fill all three ingress fields.
- `service <name> state restarting|exited` or `health starting|unhealthy`: the container did not stay up/healthy during the 10 s check window; check its logs on the host Containers or Logs tab. A start longer than 180 s or a build longer than 15 minutes is cut off.
- Lines show as withheld: redaction could not process that output chunk, so it was not sent.

## No HTTPS

1. The project needs **Domain**, **Service** and **Port** all set, and the deploy must be Healthy; the result then reads `https://<domain>`.
2. DNS for the domain must point to the host, and ports 80 and 443 must be free and reachable from the internet (certificate issuance by the managed Caddy needs them). Check `docker ps` for `haetae-caddy` and `docker logs haetae-caddy` on the host.
3. If another process holds 80 or 443, the agent's Caddy cannot start; use `--ingress-http-port` and `--ingress-https-port` only if you front it yourself.
4. For the web UI itself (not deployed apps): the browser warning is expected with the server's own CA; see own certificate in [install.md](install.md).

## No chat messages

1. **Settings**, **Chat channels**: is the channel `enabled`? Server log at start: `chat service started` with `channels=[...]`. A channel needing two variables (Slack) stays off if one is missing.
2. Is an account linked (**Linked accounts**)? Alerts go to linked, verified accounts only. Link codes last 10 minutes; the web UI answers "channel is not configured" for an unconfigured channel and "too many link codes; try again later" when rate limited.
3. Only `high` and `critical` produce an immediate message; medium and low wait for the briefing. Muted alerts are stored but not sent (**Settings**, **Alerts**, **Mutes**).
4. Delivery errors: server log `chat delivery failed` with `channel`, `attempt` and `err`; delivery retries up to 8 times.
5. Rejected button: log `chat action rejected` with `reason` (`action_not_admin`, `action_unlinked`, `link_invalid`, ...). Only linked administrators can press buttons.
6. Email is linked with a code the server emails to your own address (**Link email**, **Send code**, then type it in); if no mail arrives check the SMTP settings and the server log. Slack linking takes `link CODE` as a direct message (no slash). Signal needs your own `signal-cli`; see [chat.md](chat.md). A `HAETAE_TELEGRAM_BASE_URL` override is ignored with a warning unless `HAETAE_TEST_HOOKS=1`.

## The AI is unavailable

**Settings**, **AI operator** shows the reason. Chat says "The operator is unavailable (<reason>); rule alerts are active."

- `not configured`: set `HAETAE_AI_PROVIDER`, `HAETAE_AI_MODEL` (and key or base URL); the server log has `AI operator configured` when it is read.
- The server refuses to start with `HAETAE_AI_*: ...` for bad values (for example `provider: model is required`, `provider: base URL must use https unless kind is local and the host is local`).
- `turned off`: tick **Enable the AI operator**.
- `daily usage cap reached`: raise **Daily token cap** or wait for the next UTC day.
- `temporarily unavailable`: provider error, timeout or rate limit; logs show `operator:` warnings.
- "redaction failed; nothing was sent": redaction fails closed for a non-local provider. This is deliberate.

Rule-based alerts and the briefing are unaffected.

## Scan failed

See the status list in [security.md](security.md). Quick map: `inventory out of memory` means the image needs more than the 256 MiB helper cap (raise `HAETAE_INVENTORY_MEM`); `inventory timed out` means the 10 minute limit; "Vulnerability database not configured" means `HAETAE_VULN_DB_URL` is empty or `off` (the shipped compose file sets Anchore's listing; check `haetae.env`); "Vulnerability database update failed" means the download failed (server log: `vuln db refresh failed; keeping previous`; also `vuln db check`, `no local vuln db yet`); **Matching pending** persists until the database is present and the match queue drains. Force a retry with **Scan now**.

## High memory

- **Agent:** target under 50 MB resident. Check `systemctl status haetae-agent` (Memory line) or `ps -o rss,cmd -C haetae-agent`. A short spike while the inventory helper runs is a separate process (`haetae-inventory`, capped at 256 MiB; the whole unit at `MemoryMax=400M`). On a very small host, large images may fail the scan rather than exhaust memory.
- **Server:** large inventories are matched two at a time. Check `docker stats`; Postgres is the other container.
- If the agent restarts repeatedly, `journalctl -u haetae-agent` shows the `fatal` error.

## Server will not start

- `HAETAE_DATABASE_URL is required`; `HAETAE_WEB_TLS_CERT and HAETAE_WEB_TLS_KEY must be set together`; "HAETAE_TRUST_PROXY was replaced by HAETAE_TRUSTED_PROXIES"; `HAETAE_TRUSTED_PROXIES: "<x>" is not an address or CIDR`; `HAETAE_PUBLIC_URL must be an absolute URL`.
- The installer reports "haetae: port <n> is already in use" or "haetae: timed out waiting for the server to become healthy" (it then prints the logs).
