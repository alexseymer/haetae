# Project CLAUDE.md

Fill this in on first use. It overrides or complements `/opt/devbox/CLAUDE.md` for this repository.

## Project overview

Haetae (working name) is an AI operator for Docker Compose servers. It enrolls servers through a lightweight agent, deploys apps from Git or `compose.yaml`, collects metrics, scans containers for CVEs and misconfigurations, and reports incidents, daily briefings and answers in chat (Telegram, Signal, Slack, email, web). It is for solo developers and small teams (2 to 10 people, 2 to 20 servers) without Kubernetes or an ops person. Free self-hosted (AGPL-3.0) plus a hosted service.

`docs/spec.md` is the source of truth for scope. A change is done when it meets the relevant "Done correctly" criteria there and stays inside "Out of scope for v1".

Hard constraints from the spec that every change must respect:
- The agent uses under 50 MB RAM, needs no inbound port, and reconnects by itself.
- With a non-local AI, personal data and secrets are removed on the host before sending; if that fails, nothing is sent.
- The operator never deletes data, acts only at the configured autonomy level, and treats logs as untrusted text.
- A failed scan reports "scan failed", never "no vulnerabilities".
- Rule-based alerts keep working when the AI is unreachable.

## Stack

Decided in `docs/adr/0001-stack.md` (accepted). Read it before changing anything structural.

- **Host agent:** Go. `GOMEMLIMIT` and bounded buffers; resident RSS under 50 MB. Inventory helper (Go + Syft library) runs in its own memory-limited cgroup. Outbound monitoring via conntrack netlink events.
- **Control plane:** Go, Connect-RPC / gRPC; vulnerability matching with Grype as a library.
- **Web UI:** React + Vite + TypeScript SPA, embedded in the server binary.
- **Storage:** PostgreSQL only (relational data and time-partitioned metrics; `tenant_id` + Row-Level Security). No TimescaleDB (licensing).
- **Contract:** protobuf schemas in `proto/`.
- **On hosts:** Caddy container for HTTPS ingress, managed by the agent.
- **Self-hosted:** `docker compose` with server + Postgres; Signal (`signal-cli`) is an optional add-on.
- **Regex:** Go standard `regexp` only (RE2). No backtracking regex engines anywhere in the redaction path.

## How to build and test

TODO: no code yet. Fill in once the first module exists (Go toolchain via `devbox-setup-stack`, `go test ./...`, web UI `npm ci && npm test`).

## Conventions

TODO: fill in. Naming, layout, commit style, and anything a worker must not change.

## Personas in this project

TODO: fill in. List extra or overriding personas in `.agents/personas/` (same filename as `/opt/devbox/personas` wins). Note when Claude should prefer Cursor vs agy for this repo.
