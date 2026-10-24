# Roadmap to v1

`docs/spec.md` defines the outcome; `docs/adr/0001-stack.md` defines the stack. This file splits the work into steps. Each step is one GitHub issue and one or more pull requests. A PR is merged only when:

1. its acceptance criteria are met and shown in the PR description (test names, commands, measurements),
2. CI is green,
3. an independent review-only agent (agy, or a Claude reviewer that did not write the code) has reviewed it and every blocking finding is fixed or answered.

Spec criteria are referred to as **S1** to **S11** ("Done correctly", in order).

## Architecture in one picture

```
 customer host (one per server)                          control plane (self-hosted or hosted)
 ┌──────────────────────────────────────┐   outbound     ┌──────────────────────────────────────────┐
 │ haetae-agent (Go, <50 MB)            │   HTTP/2 +     │ haetae-server (Go)                        │
 │  docker state, metrics, log pipeline │   mTLS stream  │  agent gateway  ─ hosts, metrics, events  │
 │  redaction (fail-closed)             │ ─────────────▶ │  rules engine   ─ alerts without AI       │
 │  deploys, rollbacks, Caddy ingress   │ ◀───────────── │  deploys        ─ git polling, history    │
 │  conntrack watch, misconfig checks   │   commands     │  vuln matching  ─ Grype on agent SBOMs    │
 │ haetae-inventory (Syft, cgroup-capped)│               │  AI operator    ─ tools + evidence        │
 │ Caddy container (HTTPS)              │                │  chat adapters  ─ web, Telegram, Slack,   │
 └──────────────────────────────────────┘                │                   email, Signal (opt.)    │
                                                         │  web UI (React) ─ embedded                │
                                                         │  PostgreSQL                               │
                                                         └──────────────────────────────────────────┘
```

## Steps

### M0 Foundation
- Go module, `cmd/haetae-agent`, `cmd/haetae-server`, Makefile, CI (vet, test with race detector, lint, govulncheck), PR template, roadmap.
- **Accept:** `make test` and `make lint` pass locally and in CI; both binaries print `--version`.

### M1 Agent connection and enrollment (S1, S10)
- Protobuf contract in `proto/`, generated code committed.
- Server: Postgres schema and migrations, single-use enrollment tokens, client certificate issued at enrollment (internal CA), bidirectional stream gateway, host online/offline state.
- Agent: enroll with token, store identity, connect outbound only, heartbeat, reconnect with jittered backoff after network loss or reboot.
- **Accept:** integration test enrolls an agent and sees it online in under 30 s; a second use of the same token is rejected; killing the server and restarting it brings the agent back without intervention; the agent opens no listening port (checked in test).

### M2 State and metrics (S1, S10)
- Agent: Docker state (stacks from compose labels, containers, image versions, ports, restarts, health), host and container CPU, memory, disk and network every 10 s.
- Server: time-partitioned metrics tables with rollups (10 s / 1 min / 1 h), retention job, query API.
- Memory harness: `test/load` starts 20 noisy containers in a Docker-in-Docker host and records agent RSS.
- **Accept:** metrics for a newly enrolled host are queryable within 2 minutes; agent RSS stays under 50 MB in the harness (number recorded in the PR).

### M3 Log pipeline and redaction (S5, S6)
- Agent: follows container logs, detects level (text and JSON), turns lines into templates, merges identical lines with counts, keeps a ring buffer for on-demand queries, skips templates marked green.
- Redaction package: secrets (gitleaks rules) and personal data (email, IP, phone, card number with Luhn, JWT, URL credentials), RE2 only, per-batch time budget, fail-closed.
- Server: stores events, judges new info/debug templates (rule heuristic now, AI in M8), pushes green labels back to agents.
- **Accept:** on the recorded corpus in `test/corpus`, every warning, error and fatal line reaches the server; at least 95% of lines are never sent after warm-up; a forced redaction failure sends nothing (test); redaction recall measured on a labelled sample and recorded.

### M4 Control plane basics and self-hosted install (S11)
- Accounts, organisations (tenant id on every row, Row-Level Security), sessions, first-run setup.
- Web UI: login, setup, hosts and enrollment command, host and stack views with charts.
- `install.sh` for agents (systemd unit, binary download from the server) and `install-server.sh` for self-hosting (writes compose file, starts server and Postgres).
- **Accept:** on a clean Docker host, one command gives a working login in under 5 minutes (timed in CI or recorded); the server makes no outbound calls except those configured by the user (test with network isolation); tenant isolation test: one organisation cannot read another's rows even with a crafted query.

### M5 Deploys (S2)
- Agent: Caddy ingress container, deploy from a pasted `compose.yaml` or Git repo and branch, build log streaming, health check, automatic rollback on failure, one-click rollback to any previous deploy, branch polling for push-to-deploy.
- Server and UI: deploy form, deploy history, build log, rollback button, result with HTTPS address or failing step and raw error.
- **Accept:** e2e test deploys a sample app and gets an HTTPS response (internal CA in tests); a broken deploy leaves the previous version serving; a push to the branch is deployed within 30 s for a cached build (measured); deploy result is reported within 60 s.

### M6 Alerts, incidents, chat and briefing (S3, S9)
- Rule engine: crash and restart loops, OOM kills, unhealthy containers, host offline, CPU, memory and disk thresholds, deploy failures, new critical CVEs.
- Incidents (grouped alerts), notifier, daily briefing at 08:00 in the organisation's time zone.
- Chat: web chat in the UI; Telegram (long polling), Slack (Socket Mode), email (SMTP), Signal (`signal-cli` add-on). Account linking with a one-time code; action buttons accepted only from linked, verified accounts.
- **Accept:** a high-severity event reaches the web chat and a fake Telegram endpoint within 60 s (measured); a quiet day produces exactly one short briefing; with the AI disabled every rule alert still fires; a button press from an unlinked account is rejected.

### M7 Security scanning (S8)
- `haetae-inventory` helper (Syft) in a memory-limited cgroup; inventory on deploy and container start, daily rescan.
- Server: Grype database refresh, matching of stored SBOMs, re-matching after each database update, findings with CVE, severity and fixed version.
- Agent: misconfiguration checks (published ports, mounted Docker socket, root user, privileged mode, expiring certificates), conntrack-based outbound connection watch with fallback and a visible "degraded" state, image update policy (48 h wait unless a verified critical fix).
- **Accept:** a known vulnerable image produces the expected CVE with fixed version; a failing scan shows "scan failed"; a newly added advisory alerts within 15 minutes (test with a crafted database update); misconfigurations in a test stack are all reported; an image published less than 48 h ago is not auto-pulled unless it fixes a critical CVE present in the running image.

### M8 AI operator (S4, S6, S7, S9)
- Provider interface: Anthropic, OpenAI-compatible endpoint, local endpoint, deterministic fake for tests. Usage cap per organisation.
- Tools over our own APIs: hosts, containers, metrics, events, logs on demand (redacted on the host), deploys, findings. Every tool result becomes an evidence record with a link.
- Answer format is structured: each statement must cite evidence ids, otherwise it is dropped; no supported statement means "cause not found".
- Autonomy levels enforced on the server and the agent (report only by default, suggest with approval buttons, act on safe actions). The agent's command allowlist contains no data-deleting command. Log text is passed as quoted, untrusted data.
- AI-written incident reports, daily briefing text and log-template judging.
- **Accept:** test suite with the fake provider proves: statements without evidence are removed; "cause not found" when evidence is empty; a prompt injection in a log line cannot trigger an action; actions above the autonomy level are refused; a deleting command is refused by the agent even if requested; with the AI unreachable or capped, rule alerts keep working (S9). Recorded run with a real provider when an API key is available.

### M9 Acceptance and release
- End-to-end acceptance suite covering S1 to S11 with measured numbers, release builds for linux/amd64 and linux/arm64, container images, user documentation (install, enroll, deploy, chat setup).
- **Accept:** the acceptance report `docs/acceptance.md` lists every spec criterion with its test, the measured value and pass/fail.

## Known limits of the build environment

- Hosts are simulated with Docker-in-Docker containers on one machine (2 cores, 4 GB RAM). Timing results are recorded as measured there.
- Live Telegram, Slack, Signal, email, a real AI provider, public certificates and the hosted service need credentials or accounts; without them those parts are verified against fakes and marked as such in `docs/acceptance.md`.
