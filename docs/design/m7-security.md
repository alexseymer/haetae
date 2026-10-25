# M7 design: security scanning

Binding for workers and reviewers. Scope: roadmap M7 (spec S8, S9). Schema: `internal/store/migrations/0009_security.sql` (authoritative). Wire: the M7 section at the end of `proto/haetae/agent/v1/agent.proto`. Tell the coordinator before changing either.

## Ownership

| Part | Owns |
|---|---|
| Inventory (agent) | `cmd/haetae-inventory`, `internal/inventory`, `internal/agent/scan` (triggers, cgroup launcher, daily rescan, reports) |
| Matching (server) | `internal/vuln` (Grype library wrapper, database manager, rematch), `internal/store/vuln.go`, gateway handler `internal/gateway/security_sbom.go`, `new_cve` rule in `internal/alerts/rule_newcve.go` |
| Host security (agent + server) | `internal/agent/secfacts` (misconfigurations, certificates), `internal/agent/conntrack`, `internal/agent/imagecheck` (registry candidate lookup, pull), `internal/updates` (server policy), `internal/store/security.go`, gateway handler `internal/gateway/security_host.go` |
| API + UI | `internal/api/security.go`, `web/` Security pages |

Gateway dispatch for the new messages is stubbed in `internal/gateway/security_stub.go` (method values on a `SecurityHandlers` struct); each owner replaces its stub in its own file.

## Inventory (S8: inventory on deploy and container start, daily rescan)

- `haetae-inventory` is a separate short-lived Go binary using Syft as a library. Input: `--image <id-or-ref>` using the local Docker daemon source, or `--registry <ref@digest>` for a candidate (no local pull). Output: one JSON document on stdout (distro, packages with name/version/type/purl, tool version, peak RSS) limited to 8 MB; it never writes outside its temp dir and cleans it up.
- The agent runs it in its own cgroup v2 child (`memory.max` default 256 MB, `HAETAE_INVENTORY_MEM`, `cpu.weight` low, `pids.max`), with a wall-clock timeout (default 10 min). If cgroup v2 is not writable the agent falls back to `GOMEMLIMIT`+`RLIMIT_AS` on the child and records `limited=false` in the report `tool_version` suffix; it never skips the scan silently. Exit by the OOM killer or timeout becomes status `oom` / `timeout`; any other failure `failed`; an image Syft cannot read (scratch with no packages is valid and OK with zero packages; unreadable layers) `unsupported`/`failed` with the reason. A failed scan reports "scan failed", never "no vulnerabilities".
- Triggers: container start (state monitor sees a new running container), deploy healthy, daily rescan per running image with jitter, forced `ScanContainer` command, `ScanImage` for candidates. One inventory at a time per host (queue, dedupe by image id), no scan while the agent's RSS budget would be exceeded (helper is a child, resident agent stays under 50 MB).
- Report: `SbomReport` (packages capped at 20,000; larger sets fail with a clear error). The server dedupes by `(org, image_id)`.

## Matching

- `internal/vuln` wraps Grype as a library: database directory `HAETAE_VULN_DB_DIR` (default `<data>/vuln-db`), listing URL `HAETAE_VULN_DB_URL`. **The binary defaults to no download** (S11: no unconfigured outbound call); `deploy/compose.yaml` and the installer set the official Anchore URL explicitly so the shipped self-host stack scans out of the box. `HAETAE_VULN_DB_URL=off` disables; the UI then shows "vulnerability database not configured".
- Refresh: check the listing every 10 minutes (tiny request), download and atomically swap only on a newer build, verify checksum, keep the old database on failure and record `vuln_db.status='failed'` with the error. After a successful swap, re-match every stored SBOM (bounded concurrency 2, memory-bounded) and set `image_match_state`. A matching error sets status `failed` (shown as "scan failed").
- Findings: CVE id, severity, package, version, fixed version, description, urls. `first_seen` is kept across rematches, findings that disappear are deleted.
- Rule `new_cve` (alerts engine, no AI): a finding with severity high or critical on an image used by a running container whose `first_seen` is after the container started or after the previous evaluation creates an alert (dedupe `new_cve|<container>|<vuln>`, collapsed into one alert per container per evaluation with the top CVE in the summary and the count, fixed version if known). It resolves when the finding is gone or the container no longer runs that image. **A newly added advisory alerts within 15 minutes** (listing check 10 min + rematch + 15 s evaluator); proven with a crafted database update.

## Misconfigurations (agent, S8)

Derived from Docker inspect on every state sync and sent as a full-replacement `SecurityReport`: `published_port` (bound to 0.0.0.0/:: and not 80/443 owned by the managed Caddy; severity medium, high for well-known database ports), `docker_socket` (bind mount of docker.sock, critical), `root_user` (user empty/0, low), `privileged` (high), `host_network`, `host_pid` (medium), `dangerous_capability` (SYS_ADMIN, NET_ADMIN, SYS_PTRACE, ALL; medium/high), `cert_expiring` (TLS probe of the managed ingress domains and published TLS ports from the host itself: < 14 days high, < 3 days critical, expired critical). Findings carry short non-secret detail. Absent in a later report = resolved. Container env values are never read into reports.

## Outbound connections

`internal/agent/conntrack` listens to conntrack netlink NEW events (ti-mo/conntrack) in the host namespace, maps NATed source IPs to containers through the Docker API (refreshed on container events), aggregates per (container, dst ip, dst port, proto) in 60 s windows, keeps at most 200 distinct destinations per window (counts the rest in `dropped`), and sends `OutboundBatch`. State machine: `ok`; `degraded` (permission denied, netlink overflow ENOBUFS, listener restarts, event loss) with the reason; `unavailable` (no nf_conntrack, no CAP_NET_ADMIN). `network_mode: host` containers are listed in `not_monitored_container_ids` and shown as "not monitored". The resident agent stays under 50 MB RSS with the listener running (measure and record).
Server keeps the last 30 days (upsert on the primary key, daily purge).

## Image update policy (S8)

Agent `imagecheck` looks up, per running container image reference, the current registry digest and creation time (OCI registry API v2, anonymous token flow for public registries; private/unauthenticated registries report status `unknown`) and sends `ImageCandidates` daily. Server `internal/updates` decides per container: `current` (same digest), `wait` (candidate younger than 48 h), `eligible` (>= 48 h), `eligible_critical_fix` (younger than 48 h but a `ScanImage` of the candidate shows at least one critical finding of the running image fixed, "verified": the CVE id is present in the running image's findings and absent in the candidate's matching), `unknown`. `PullImage` is sent only when the org setting `auto_pull` is on and the decision is eligible/eligible_critical_fix; the agent re-checks digest and age and refuses otherwise; a pull never restarts or removes anything (restart/redeploy belongs to the M8 operator at its autonomy level).

## Evidence requested in the PR (roadmap M7 accept)

1. A known vulnerable image yields the expected CVE with fixed version. 2. A failing scan shows "scan failed". 3. Deploy and container start each produce an inventory; a forced daily-rescan run produces a new one. 4. A newly added advisory (crafted database update) alerts within 15 minutes, with no AI configured. 5. Misconfigurations in a test stack are all reported. 6. Outbound connection attributed to its container; conntrack unavailable shows "degraded"/"unavailable"; `network_mode: host` shows "not monitored" (conntrack paths tested against a fake netlink source where the VM has no nf_conntrack, real if available). 7. Image younger than 48 h is not pulled unless it fixes a critical CVE present in the running image. 8. Recorded: Syft peak memory on a large image (inside the 256 MB cgroup) and Grype-vs-Trivy agreement on debian:12-slim, ubuntu:24.04, alpine:3.20, node:22-bookworm (ADR risks 2 and 3; if a reference image cannot be pulled on this host record that honestly), agent RSS with the conntrack listener running.
