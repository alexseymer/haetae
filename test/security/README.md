# Security end-to-end tests (M7)

Real `haetae-server`, real `haetae-agent`, real `haetae-inventory` (Syft helper),
real Docker, and a crafted Grype v6 vulnerability database served from an
`httptest` listing. No AI is configured.

## Requirements

- Docker daemon and permission to talk to it
- Cached images: `nginx:alpine`, `busybox:1.37` (pulls `alpine:3.20` and
  `registry:2` if missing)
- Go toolchain on `PATH` (see the Makefile / CI image)

## Server / agent env

| Variable | Role |
| --- | --- |
| `HAETAE_VULN_DB_URL` | Points at the test listing server (`…/latest.json`) |
| `HAETAE_TEST_HOOKS` | Must be `1` for test-only overrides below |
| `HAETAE_VULN_CHECK_EVERY` | **Test-only.** Shortens the Grype listing check (production default **10m**). Ignored unless `HAETAE_TEST_HOOKS=1`. |
| `HAETAE_SCAN_DAILY_EVERY` | **Test-only.** Shortens the agent daily inventory interval (production default 24h). Ignored unless `HAETAE_TEST_HOOKS=1`. |
| `HAETAE_IMAGECHECK_EVERY` | **Test-only.** Shortens image candidate checks (production default 24h). Ignored unless `HAETAE_TEST_HOOKS=1`. |
| `HAETAE_INVENTORY_BIN` | Absolute path to the real `haetae-inventory` built into the test temp dir (beside the agent binary also works) |
| `HAETAE_INVENTORY_TIMEOUT` | Used in the failed-scan scenario to force a timeout |

All Docker resources use the `htest-` name prefix.
`TestMain` fails the package if any `htest-` leftover remains (`internal/testutil/dockerclean`).

### Local registry / Syft HTTP

Image-update scenarios use a `registry:2` on `127.0.0.1`. Docker already treats
`127.0.0.0/8` as an insecure registry CIDR. The agent registry client uses
**HTTP for loopback hosts**; the inventory helper enables Syft
`InsecureUseHTTP` for `localhost` / `127.0.0.1` / `::1` registry refs.

If a candidate pull through the registry source is still infeasible on a host,
the suite asserts policy decisions (`wait` / pull refusal via `imagecheck.Pull`)
and notes that auto-pull of `eligible` / `eligible_critical_fix` is covered by
`internal/agent/imagecheck` and `internal/updates` unit tests.

## Run

```bash
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH
make test-security
# or:
go test -count=1 -tags e2e_security -timeout 40m -v ./test/security/
```

Acceptance also requires:

```bash
go vet -tags e2e_security ./test/security/
gofmt -l .
make lint
```

## Scenarios

| Subtest | What it checks |
| --- | --- |
| `known_vulnerable_image` | Real Syft SBOM + crafted CVE → API returns CVE, severity, fixed version; counts match |
| `failed_scan` | Stub exit 1 / tiny timeout → failed/timeout with error; never ok+zero; prior findings kept in DB |
| `scan_triggers` | Deploy + start scans; POST scan; shortened daily each create `scans` rows |
| `new_cve_alert` | DB v2 critical advisory → finding + `new_cve` alert + web chat within 15m; no AI |
| `misconfigurations` | Privileged, docker.sock, root, db port, host net, SYS_ADMIN → all kinds; then resolved; cert_expiring |
| `outbound_conntrack` | unavailable/degraded + host-network not monitored; real attribution skipped without nf_conntrack/root |
| `image_update_policy` | Young candidate → `wait` + PullImage refused; registry/critical-fix/eligible as feasible |

Look for `MEASURE` lines in the verbose output for timings.
