# ADR 0001: Technology stack

Status: Accepted · 2026-10-24 · revised after a second-opinion review (see "Review notes")

## Context

The spec (`docs/spec.md`) implies two very different programs:

1. **Host agent**, one per server. Resident memory under 50 MB, outbound connection only, reconnects by itself. It reads Docker state and metrics, tails logs, filters and deduplicates them (target: 95% of lines never sent), removes personal data and secrets *before* anything leaves the host, inventories container software, watches outbound connections, and runs deploys and rollbacks. In hosted mode raw logs never leave the host, so the agent must also answer log queries on demand.
2. **Control plane**. Enrollment, auth, deploy orchestration, metrics storage, a rule-based alert engine that works without the AI, vulnerability matching, chat adapters (Telegram, Signal, Slack, email, web), the AI operator, and the web UI. Self-hosted: a working login within 5 minutes of one command. Hosted: multi-tenant.

Constraints that shape the choice:
- Everything must be compatible with AGPL-3.0 distribution.
- Self-hosted ships every feature, so the hosted and self-hosted control planes are one codebase.
- Typical hosts are small VPSs (1 to 4 GB RAM). Haetae must never starve the customer's workload.
- The team is small. Every extra language costs build, CI, release and review effort.

## Agent design (same in every option)

- **Language: Go.** The reasons are specific to this agent: the official Docker Engine SDK, mature libraries for conntrack (`ti-mo/conntrack`) and eBPF (`cilium/ebpf`), Syft usable as a library inside the inventory helper, static cross-compiled binaries, and the same language as the recommended control plane. Rust (with `bollard` and `aya`) would use less memory but means a second backend language and no Syft/Grype libraries. Rust is the fallback if risk 1 below fails.
- **Memory discipline:** `GOMEMLIMIT` set below the 50 MB target, bounded ring buffers for logs, a capped deduplication table, pooled buffers on the log path.
- **Software inventory** runs as a separate short-lived helper (our own Go binary using Syft as a library) inside its own cgroup with a hard memory limit (default 256 MB, configurable) and low CPU priority. If it hits the limit or fails, the result is "scan failed", never "no vulnerabilities". The 50 MB target counts the resident agent only.
- **Outbound connection monitoring (v1):** one conntrack netlink event listener in the host network namespace. Docker bridge traffic is NATed there, so container source IPs are visible and are mapped to containers through the Docker API. Containers using `network_mode: host` are a known gap in v1, shown as "not monitored". eBPF (`cilium/ebpf`) is the later upgrade for exact per-process attribution.
- **Redaction pipeline (fail-closed):** gitleaks rule set (MIT) for secrets, plus our own patterns for personal data: email, IP address, phone number, payment card number (with Luhn check), JWT, and URL credentials. Only Go's standard `regexp` (RE2, linear time; no backtracking engines), with a per-batch time budget. If a batch errors or exceeds the budget, it is not sent. The same package runs on the control plane as a second line of defence.
- **Transport:** one outbound HTTP/2 stream (Connect-RPC / gRPC over TLS), authenticated with a client certificate issued when the host enrolls with its single-use token. Commands from the server come back over that stream, so no inbound port is needed. Protobuf schemas in `proto/` are the contract.
- **Ingress on hosts:** Caddy as a container the agent manages (through the Docker API and Caddy's admin API), for HTTPS addresses and automatic certificates.
- **Raw logs stay on the host.** The control plane stores only filtered events, merged duplicates with counts, and the excerpts quoted as evidence.

## Options for the control plane

### A. Go control plane, TypeScript web UI (recommended)

- Go server (standard library HTTP plus Connect-RPC) and PostgreSQL for everything stored (see Storage).
- Vulnerability matching: Grype as a library against the SBOMs from the agents. Grype's database combines distro feeds (Debian, Ubuntu, Alpine, Red Hat) with GHSA, NVD and others, and does distro-aware version comparison (dpkg, rpm, apk) that raw OSV data would force us to write ourselves.
- Web UI: React + Vite + TypeScript SPA, built into the server binary.
- AI operator: official Anthropic Go SDK behind a provider interface, so OpenAI-compatible and local endpoints (later: Laya) can be plugged in.
- Self-hosted install: `docker compose` with two containers (server, Postgres). Signal is an optional add-on container (`signal-cli`, Java) and not part of the default install.

Pros:
- One backend language for agent and server: shared protobuf code, rule logic and the redaction package, one toolchain, one CI.
- Syft (agent side) and Grype (server side) are both Go libraries, Apache-2.0.
- Light self-hosted install; easy single-binary releases.

Cons:
- The LLM and agent-framework ecosystem moves fastest in TypeScript and Python. Go SDKs exist, but convenience libraries (structured output helpers, evaluation tools) will partly have to be written.
- The UI is a second language anyway (TypeScript).

### B. Go agent, TypeScript control plane

- Node.js (or Bun) server with Hono or Fastify, PostgreSQL via Drizzle, React UI sharing types with the server. Vulnerability matching through the Grype CLI.

Pros:
- Best LLM tooling (Vercel AI SDK, MCP, evaluation tools); fastest iteration on the operator and the UI.
- Types shared end to end between server and UI.

Cons:
- Two backend languages; protobuf is the only shared contract, and the redaction package lives only in Go.
- Grype only as a CLI subprocess; heavier self-hosted footprint; npm supply-chain surface on a security product.

### C. Go agent, Elixir/Phoenix control plane

- Phoenix with LiveView for the UI, PostgreSQL.

Pros:
- The BEAM is excellent at many long-lived connections and supervised background jobs; LiveView avoids a separate SPA.

Cons:
- Smallest pool of contributors and the weakest LLM SDK support; coding agents are least reliable in Elixir.
- Still two backend languages; Grype only as a CLI.

## Storage (applies to every option)

| Data | Choice | Why | Alternative considered |
|---|---|---|---|
| Accounts, hosts, deploys, findings, events | PostgreSQL | One engine for both editions; hosted tenants isolated with a `tenant_id` on every row plus Row-Level Security | SQLite: simpler self-host, but two engines doubles testing |
| Metrics | PostgreSQL, time-partitioned tables plus our own rollups (raw 10 s for 48 h, 1 min for 30 days, 1 h for 13 months) | At 2 to 20 hosts per tenant the volume is small; same tenant isolation and backups as everything else; one service less | VictoriaMetrics single-node: better compression and PromQL, but no multi-tenancy (cluster edition needed). TimescaleDB: compression and continuous aggregates are under the Timescale License, not open source, a risk for AGPL distribution |
| Vulnerability data | Grype database, refreshed on the control plane | Distro-aware matching; fixed versions included | Raw OSV/GHSA in Postgres: requires writing dpkg/rpm/apk version logic |
| Raw logs | Stay on the host (Docker log files + agent ring buffer) | Spec requirement for hosted mode | none |

Revisit metrics storage if hosted volume makes Postgres the bottleneck; VictoriaMetrics cluster (which has tenants) is the planned next step.

## Recommendation

**Option A.** Haetae's hardest problems are on the host (filtering, redaction, inventory, connection monitoring, deploys), and those are Go regardless. A Go control plane keeps one backend language, shares the redaction and rule code, uses Syft and Grype as libraries, and gives self-hosters a two-container install. The cost is writing some LLM plumbing ourselves, which is acceptable because the operator's core is a tool-calling loop over our own evidence APIs, not a framework.

Revisit if prototyping the AI operator in Go turns out much slower than expected: the operator can then move into a separate TypeScript service behind an internal API without changing the agent or the data model.

## Risks to validate early (spikes before v1 work)

1. **Agent memory:** resident agent stays under 50 MB RSS with 20 containers, log bursts through redaction and deduplication, metrics, and the conntrack listener. If Go cannot hold this with `GOMEMLIMIT`, reconsider Rust for the agent only.
2. **Inventory on small hosts:** Syft peak memory and time on large images (multi-GB, ML images) inside the 256 MB cgroup; how often it ends in "scan failed" on a 1 GB VPS.
3. **Matching accuracy:** compare Grype results on `debian:12-slim`, `ubuntu:24.04`, `alpine:3.20` and `node:22-bookworm` with a second scanner (Trivy) to check false positives from distro backports.
4. **Connection monitoring:** conntrack events capture short-lived outbound connections from bridge-network containers at realistic rates, within the memory budget.
5. **Deploy time:** push-to-HTTPS within 30 seconds is only reachable for cached or prebuilt images; measure a cold `docker compose build`.
6. **Log filtering:** reaches the 95% target on real logs without dropping warnings or errors.
7. **Redaction:** recall on a labelled sample of real logs, and that the fail-closed path actually blocks sending.

## Review notes

A second-opinion review (2026-10-24) changed the first draft:
- Metrics moved from VictoriaMetrics single-node to Postgres (no multi-tenancy in single-node VictoriaMetrics). TimescaleDB, which the review suggested, was rejected on licensing.
- Vulnerability matching moved from raw OSV/GHSA to Grype (distro version handling).
- Added: inventory cgroup limit, conntrack-based connection monitoring, the redaction pipeline, memory discipline, Signal as an optional add-on.
- Corrected: gitleaks is MIT; Syft and Caddy are not linked into the resident agent.
- Not adopted: the claim that conntrack needs one listener per network namespace (bridge traffic is NATed in the host namespace); a Rust agent with a TypeScript control plane (two backend languages; kept only as a fallback for the agent).
