# M7 ADR risk benchmarks

Reproducible measurements for ADR 0001 risks **2** (Syft / inventory peak memory), **3** (Grype vs Trivy agreement), and agent resident RSS with scan + security active (evidence item 8 in `docs/design/m7-security.md`).

Results are summarized in [`docs/benchmarks-m7.md`](../../docs/benchmarks-m7.md). Raw artifacts land in `.cache/` and `results/` (gitignored).

## Constraints

- Host is a **4 GB** VM with limited free disk. Scripts pull **one heavy image at a time** and remove non-keep images afterwards.
- Do **not** run these in parallel with other heavy Docker/Go jobs on the same machine.
- Do **not** edit product code from this harness; only `bench/m7/`, `docs/benchmarks-m7.md`, `test/bench` (`//go:build bench`), and the `bench-m7` Makefile target.

## Prerequisites

```bash
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH
cd /path/to/haetae-m7
make build   # bin/haetae-inventory, bin/haetae-agent, bin/haetae-server
docker info  # Engine available; alpine:3.20 should already be present
```

## Run everything

```bash
make bench-m7
# equivalent: bash bench/m7/run-all.sh
```

Steps (sequential):

1. `01-syft-peak.sh` — `haetae-inventory` inside `docker run --memory 256m` (and `128m`) for `alpine:3.20`, `node:22-bookworm`, and `python:3.12` if disk allows.
2. `02-grype-trivy.sh` — download real Anchore Grype v6 DB via `grypedb.Refresh`, SBOM with the helper, match with `internal/vuln/grypedb`, compare to Trivy JSON on `debian:12-slim`, `ubuntu:24.04`, `alpine:3.20`, `node:22-bookworm`.
3. `03-agent-rss.sh` — real agent vs real gateway for 3 minutes (few containers, scan + security started); conntrack **fake-source** heap bench at ~20k events/s. Real conntrack RSS is recorded as not measurable on this VM.

## Individual steps

```bash
bash bench/m7/01-syft-peak.sh
KEEP_NODE_FOR_COMPARE=1 bash bench/m7/01-syft-peak.sh   # leave node for step 2
bash bench/m7/02-grype-trivy.sh
bash bench/m7/03-agent-rss.sh
```

Go helpers (`test/bench`, tag `bench`):

```bash
go test -tags bench -count=1 -timeout 20m -v -run TestFetchGrypeDB ./test/bench/
go test -tags bench -count=1 -timeout 20m -v -run TestMatchSbom ./test/bench/
go test -tags bench -count=1 -timeout 30m -v -run TestAgentRSSWithScanSecurity ./test/bench/
go test -count=1 -bench=BenchmarkHighRateHeap -benchtime=1x -run=^$ -v ./internal/agent/conntrack/
```

## Image hygiene

Previously present images that must remain include: `htest-*-web`, `haetae-server:dev`, `nginx:alpine`, `postgres:17-alpine`, `alpine:3.20`, `hello-world:latest`, `caddy:2.10.2-alpine`, `curlimages/curl:8.11.1`, `busybox:1.37`, `gcr.io/distroless/static-debian12:nonroot`. Scripts remove only images they pulled for the bench.
