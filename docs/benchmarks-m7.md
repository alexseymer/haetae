# M7 benchmarks (ADR risks 2–3, agent RSS)

Measured on **2026-10-25** for evidence item 8 in `docs/design/m7-security.md` and ADR 0001 risks 2 (inventory memory), 3 (Grype vs Trivy), plus agent resident RSS with scan + security active. Scripts: `bench/m7/` (see `bench/m7/README.md`). Go helpers: `test/bench` (`//go:build bench`). Makefile: `make bench-m7`.

## Environment

| Item | Value |
|---|---|
| Date (UTC) | 2026-10-25 |
| Host | `dev-haetae` Proxmox VM |
| Kernel | `Linux 7.0.14-25-pve #1 SMP PREEMPT_DYNAMIC PMX 7.0.14-25 (2026-10-17T20:42Z) x86_64` |
| RAM | **4 GiB** total (`MemTotal` 4294967296 bytes); ~2 GiB swap |
| Docker | 26.1.5+dfsg1 |
| Free disk at start | ~14 GiB on `/` (dropped to ~8 GiB while Grype/Trivy DBs and images were cached) |
| Binaries | `bin/haetae-inventory`, `bin/haetae-agent` built with `CGO_ENABLED=0` |
| Grype DB | Anchore v6 via `grypedb.Refresh` → listing `https://grype.anchore.io/databases/v6/latest.json`, built **2026-10-25T06:27:34Z** |
| Trivy | `aquasec/trivy:latest` (pulled for the run, removed afterwards), DB downloaded inside the container cache |
| Parallel load | An e2e security worker was also present on the host; benches were run **one at a time** |

## Reproduce

```bash
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH
cd /path/to/haetae-m7
make build
make bench-m7
# or: bash bench/m7/run-all.sh
```

Individual steps and env vars: `bench/m7/README.md`. Raw JSON under `bench/m7/results/` and `bench/m7/.cache/` (gitignored).

---

## 1. Syft / `haetae-inventory` peak memory (ADR risk 2)

**Method:** Run the real static helper inside a throwaway container that mirrors the production cgroup cap:

```bash
docker run --rm --memory <256m|128m> --memory-swap <same> --cpus 1 \
  -v "$PWD/bin/haetae-inventory:/haetae-inventory:ro" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  alpine:3.20 /haetae-inventory --docker-image <image id>
```

Record exit code, `OOMKilled`, wall time, helper `peakRssBytes` when JSON is produced, and cgroup `memory.peak` when readable. On OOM the agent launcher sets status `oom` with error `inventory out of memory`; the UI line is **Scan failed: inventory out of memory** (evidence shorthand: **scan failed: oom**).

### Results

| Image | Limit | Exit | OOMKilled | Wall (s) | peakRssBytes (helper) | cgroup memory.peak | Packages | Agent would report |
|---|---|---|---|---|---|---|---|---|
| `alpine:3.20` | 256m | 0 | false | 2.050 | **188 256 256** (~179.5 MiB) | (not read) | 14 | ok |
| `alpine:3.20` | 128m | 137 | **true** | 2.061 | — | — | — | **scan failed: oom** |
| `node:22-bookworm` (~1.1 GB image) | 256m | 137 | **true** | 16.169 | — | **268 439 552** (= limit) | — | **scan failed: oom** |
| `node:22-bookworm` | 128m | 137 | **true** | 12.234 | — | 134 217 728 | — | **scan failed: oom** |
| `python:3.12` | 256m | 137 | **true** | 10.187 | — | 268 435 456 | — | **scan failed: oom** |
| `python:3.12` | 128m | 137 | **true** | 38.364 | — | 134 221 824 | — | **scan failed: oom** |

### Margin and unconstrained peaks

- **256 MiB margin on alpine:** peak ≈ 179.5 MiB → about **76 MiB** headroom under the default cgroup. **128 MiB has no margin** (OOM on alpine).
- **Large images:** `node:22-bookworm` and `python:3.12` **hit the 256 MiB limit** (OOMKilled). A later host-side (unlimited) inventory of the same `node:22-bookworm` image reported `peakRssBytes` **477 999 104** (~456 MiB) and **612** packages — consistent with OOM under 256 MiB.
- Host-side unlimited peaks for the agreement images (not cgroup-capped; for context only): alpine ≈ 175 MiB (14 pkgs), debian:12-slim ≈ 203 MiB (88), ubuntu:24.04 ≈ 200 MiB (92), node ≈ 456 MiB (612).

### Caveats

- Limiting via `docker run --memory` is the portable stand-in for the agent’s cgroup v2 child; it is not identical to every host’s systemd/docker cgroup layout.
- `docker stats` peak sampling often missed short runs; prefer helper `peakRssBytes` and cgroup `memory.peak`.
- On a **1 GiB VPS**, a successful alpine inventory (~180 MiB helper) plus the resident agent and workload is tight; **multi-GB app images will frequently end as “scan failed: oom”** under the default 256 MiB cap (operator can raise `HAETAE_INVENTORY_MEM`).

---

## 2. Grype vs Trivy agreement (ADR risk 3)

**Method:**

1. Download the real Anchore Grype v6 DB with `grypedb.Refresh` (`test/bench.TestFetchGrypeDB`) into `bench/m7/.cache/grype-db`.
2. Produce an SBOM with `haetae-inventory --docker-image <id>`.
3. Match with `internal/vuln/grypedb` (`test/bench.TestMatchSbom`).
4. Scan the same image with Trivy (`docker run … aquasec/trivy:latest image --format json --scanners vuln`).
5. Compare sets of `(CVE id, package)` at **all** severities and at **≥ medium**; Jaccard = \|∩\| / \|∪\|. Sample up to 10 disagreements per image.

Grype DB built-at: **2026-10-25T06:27:34Z**. Comparison does **not** hide poor agreement.

### Summary table (after the source-package fix, 2026-10-26)

The first run (table further below) exposed a matcher bug: the compact SBOM handed Grype only binary package names, but Debian, Ubuntu, Alpine and RHEL advisories are keyed by **source** package (`pam`, not `libpam-modules`). `internal/vuln/grypedb` now reads Syft's `upstream=` PURL qualifier into `Package.Upstreams` (regression test `TestMatchUsesUpstreamSourcePackage`). Same SBOMs, same Grype DB, same Trivy results, re-matched:

| Image | Band | Grype \|set\| | Trivy \|set\| | ∩ | Jaccard (before → after) |
|---|---|---|---|---|---|
| `debian:12-slim` | all | 222 | 229 | 222 | 0.127 → **0.969** |
| | ≥ medium | 144 | 144 | 139 | 0.097 → **0.933** |
| `ubuntu:24.04` | all | 74 | 26 | 26 | 0.129 → **0.351** |
| | ≥ medium | 65 | 22 | 22 | 0.154 → **0.339** |
| `alpine:3.20` | all | 2 | 2 | 2 | 0.500 → **1.000** |
| | ≥ medium | 0 | 0 | 0 | 1.000 → **1.000** |
| `node:22-bookworm` | all | 4574 | 4609 | 4553 | 0.059 → **0.983** |
| | ≥ medium | 2531 | 2679 | 2193 | 0.036 → **0.727** |

**Reading (after):** Debian-based images and Alpine now agree closely. On Ubuntu every Trivy finding is also found by Grype; Grype reports more: every sampled Grype-only finding has no fixed version yet (unfixed in Ubuntu, e.g. CVE-2024-10041 in the pam packages), which this Trivy run does not list. The remaining ≥ medium gap on Node is mostly severity mapping (Grype "unknown"/"low" vs Trivy "medium" for kernel headers in `linux-libc-dev`), not missing matches. Raw data: `bench/m7/results/grype-trivy-v2.json`.

### Summary table (first run, before the fix)

| Image | Grype raw | Trivy raw | Band | Grype \|set\| | Trivy \|set\| | ∩ | only Grype | only Trivy | Jaccard |
|---|---|---|---|---|---|---|---|---|---|
| `debian:12-slim` | 29 | 229 | all | 29 | 229 | 29 | 0 | 200 | **0.127** |
| | | | ≥ medium | 15 | 144 | 14 | 1 | 130 | **0.097** |
| `ubuntu:24.04` | 9 | 26 | all | 9 | 26 | 4 | 5 | 22 | **0.129** |
| | | | ≥ medium | 8 | 22 | 4 | 4 | 18 | **0.154** |
| `alpine:3.20` | 1 | 2 | all | 1 | 2 | 1 | 0 | 1 | **0.500** |
| | | | ≥ medium | 0 | 0 | 0 | 0 | 0 | **1.000** |
| `node:22-bookworm` | 293 | 4610 | all | 292 | 4609 | 271 | 21 | 4338 | **0.059** |
| | | | ≥ medium | 139 | 2679 | 97 | 42 | 2582 | **0.036** |

**Reading (first run, superseded):** the gap below was mostly the missing source-package mapping, not a scanner policy difference. Original note: On Debian/Ubuntu/Node, Trivy’s `(CVE, package)` set is much larger than Grype’s; almost every Grype finding on debian:12-slim appears in Trivy, but Trivy adds hundreds of extras (often unfixed / `will_not_fix`, severity mapping, or kernel meta-packages like `linux-libc-dev` on Node). Alpine ≥ medium agrees vacuously (both empty). **Do not treat the two scanners as interchangeable for counts or “clean” images.**

### Disagreement samples (≤10 per image)

**debian:12-slim**

| Side | CVE | Package | Likely reason |
|---|---|---|---|
| only-in-grype | CVE-2025-13151 | libtasn1-6 | Severity / threshold vs Trivy |
| only-in-trivy | CVE-2023-45853 | zlib1g | Unfixed / `will_not_fix` policy |
| only-in-trivy | CVE-2023-50495 | libtinfo6 | Severity (Grype unknown vs Trivy medium) |
| only-in-trivy | CVE-2023-50495 | ncurses-base | Same |
| only-in-trivy | CVE-2023-50495 | ncurses-bin | Same |
| only-in-trivy | CVE-2024-10041 | libpam-modules | Unfixed / `will_not_fix` |
| only-in-trivy | CVE-2007-5686 | login | Severity / old advisory handling |
| only-in-trivy | CVE-2007-5686 | passwd | Same |
| only-in-trivy | CVE-2010-4756 | libc-bin | Severity / old advisory handling |
| only-in-trivy | CVE-2010-4756 | libc6 | Same |

**ubuntu:24.04**

| Side | CVE | Package | Likely reason |
|---|---|---|---|
| only-in-grype | CVE-2026-76642 | util-linux | Grype-only / severity policy |
| only-in-grype | CVE-2026-78408 | util-linux | Same |
| only-in-grype | CVE-2026-78409 | util-linux | Same |
| only-in-grype | CVE-2026-78410 | util-linux | Same |
| only-in-trivy | CVE-2026-103111 | libpcre2-8-0 | Severity (unknown vs medium) |
| only-in-trivy | CVE-2026-18374 | libc-bin | Severity (unknown vs medium) |
| only-in-trivy | CVE-2026-18374 | libc6 | Same |
| only-in-trivy | CVE-2026-67693 | libgnutls30t64 | Severity (unknown vs medium) |
| only-in-trivy | CVE-2026-85091 | zlib1g | Fixed / unfixed policy |
| only-in-grype | CVE-2016-2781 | coreutils | Grype-only / severity policy |

**alpine:3.20**

| Side | CVE | Package | Likely reason |
|---|---|---|---|
| only-in-trivy | CVE-2026-107891 | musl-utils | Fixed / unfixed policy (only all-severity disagreement; ≥ medium empty both sides) |

**node:22-bookworm**

| Side | CVE | Package | Likely reason |
|---|---|---|---|
| only-in-grype | CVE-2025-13151 | libtasn1-6 | Severity / threshold |
| only-in-grype | CVE-2026-109260 | imagemagick | Grype-only / severity |
| only-in-grype | CVE-2026-109261 | imagemagick | Same |
| only-in-grype | CVE-2026-109263 | imagemagick | Same |
| only-in-grype | CVE-2026-109264 | imagemagick | Same |
| only-in-trivy | CVE-2013-7445 | linux-libc-dev | Unfixed / `will_not_fix` on kernel headers package |
| only-in-trivy | CVE-2019-15213 | linux-libc-dev | Severity + kernel-headers noise |
| only-in-trivy | CVE-2019-16089 | linux-libc-dev | Same |
| only-in-trivy | CVE-2019-19449 | linux-libc-dev | Same |
| only-in-trivy | CVE-2019-19814 | linux-libc-dev | Same |

### Caveats

- Sets are `(CVE, package name)` after normalizing CVE tokens out of IDs; GHSA-only IDs without a CVE token remain as-is and may reduce intersection.
- Trivy and Grype use different DB cutovers and severity schemes; “unknown” in Grype vs “medium” in Trivy moves many rows across the ≥ medium boundary.
- Node’s Trivy set is dominated by `linux-libc-dev` and similar OS packages; that inflates Trivy-only counts relative to our compact SBOM + Grype match path.

---

## 3. Agent resident memory + conntrack

### Agent RSS (scan scheduler + security active)

**Method:** Reuse the M2 pattern from `test/integration.TestAgentMemoryUnderLoad` in `test/bench.TestAgentRSSWithScanSecurity`: real gateway (`storetest` + `ListenGateway`), enroll, run the real `haetae-agent` binary with `haetae-inventory` beside it (so the scan launcher can find the helper), start **4** busybox containers, sample `/proc/<pid>/status` **VmRSS** every second for **3 minutes**, then read **VmHWM**. The agent process starts the scan scheduler and `startSecurity` (secfacts + conntrack listener + imagecheck) as in production.

| Metric | Value |
|---|---|
| Duration | 3m0s (180 samples) |
| Containers started | 4 (server listed 8 hosts’ containers including unrelated ones on the Docker daemon) |
| max VmRSS | **21 389 312** bytes (**20.40 MiB**) |
| mean VmRSS | **20 544 534** bytes (**19.59 MiB**) |
| peak VmHWM | **21 192 704** bytes (**20.21 MiB**) |
| Budget (spec S10) | 50 MiB — **pass** |

### Conntrack

| Item | Result |
|---|---|
| Real conntrack RSS | **Measured 2026-10-26, agent run as root with the netlink listener live** (`/proc/net/nf_conntrack` is absent here, but the `nf_conntrack` sysctls and netlink work; the first run wrongly treated that as unavailable): max **17.29 MiB**, mean 16.51 MiB, peak (VmHWM) **17.41 MiB**, 180 samples over 3 min, 4 busybox containers, budget 50 MiB. Without conntrack (non-root run): peak 20.21 MiB. |
| Fake-source heap bench | `go test -bench=BenchmarkHighRateHeap -benchtime=1x -run=^$ -v ./internal/agent/conntrack/` |
| Measured | ~**195 200** events over 10 s (~**19 520**/s); heap in-use growth **24 576** bytes (**0.02 MiB**); target ≪ 10 MiB — **pass** |

Real conntrack RSS with a live netlink listener was measured as root on 2026-10-26 (table row above); the first run (2026-10-25) could not, because availability detection relied on `/proc/net/nf_conntrack`.

---

## Image hygiene

Bench pulls (`node:22-bookworm`, `python:3.12`, `debian:12-slim`, `ubuntu:24.04`, `aquasec/trivy:latest`) were removed after use. `alpine:3.20` and other pre-existing images were kept by the scripts.

**Note for coordinator:** During this run a parallel `test/security` e2e worker removed the pre-existing `htest-*-web` images from the daemon. Our keep-list would have preserved them; they were not deleted by `bench/m7` scripts.

---

## Acceptance checklist

- [x] Measured Syft peak / OOM under 256m and 128m (alpine, node, python)
- [x] Measured Grype vs Trivy counts, Jaccard, and disagreement samples for all four images
- [x] Measured agent VmRSS/VmHWM over 3 minutes with scan + security started
- [x] Conntrack: fake-source heap measured; real RSS measured as root (17.41 MiB peak)
- [x] Scripts under `bench/m7/`; Go under `test/bench` with tag `bench`; `make bench-m7`
- [x] Host noted as **4 GB** VM
