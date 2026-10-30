# Contributing to Haetae

Thanks for helping. Haetae is a small project with strict hard constraints, so please read this page and [docs/contributors/spec.md](docs/contributors/spec.md) (the source of truth for scope) before a larger change. By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). Security problems go to [SECURITY.md](SECURITY.md), not to public issues.

## Set up

You need Go (the version in `go.mod`), Docker with Compose v2 for most test suites, and Node.js with npm for the web UI (CI uses Node 20).

```sh
git clone https://github.com/alexseymer/haetae.git
cd haetae
make build    # bin/haetae-agent, bin/haetae-server (static, CGO_ENABLED=0)
make test     # go test -race ./...
```

`make build` embeds whatever is in `internal/webui/app/`; run `make web` first to build the UI into it (needs `npm ci`). The server image is built with `docker build -t haetae-server:local .` (see the README quick start).

## Make targets

| Target | What it runs | Needs |
|---|---|---|
| `make build` | Static binaries into `bin/` | Go |
| `make test` | Unit tests with `-race` (tests that need Docker or PostgreSQL are in `make test-integration`) | Go |
| `make lint` | golangci-lint (version pinned in the Makefile) | Go, network to fetch the tool |
| `make vuln` | govulncheck (version pinned) | Go, network |
| `make fmt` | golangci-lint formatter | Go |
| `make web` | Builds the UI into `internal/webui/app` | Node, npm |
| `make generate` | Regenerates `internal/gen` from `proto/` | `buf`, `protoc-gen-go`, `protoc-gen-connect-go` |
| `make test-integration` | Tests tagged `integration` | Docker, or `HAETAE_TEST_DATABASE_URL` pointing at a PostgreSQL admin connection |
| `make test-e2e` | Real-browser tests of the embedded UI | Docker, Chromium, Playwright |
| `make test-deploy` | Deploy acceptance (real server, agent, Docker) | Docker, cached busybox and caddy images |
| `make test-alerts` | Alerts and chat acceptance (fake Telegram) | Docker, busybox |
| `make test-security` | Security acceptance (crafted Grype database) | Docker |
| `make test-operator` | AI operator acceptance (scripted provider) | Docker |
| `make screenshots` | Regenerates `docs/images/*.png` for the README | Docker, Chromium, Playwright, cached `nginx:alpine`, `busybox:1.37`, `postgres:17-alpine` |
| `make test-load` | Agent memory with 20 noisy containers (`HAETAE_LOAD_DURATION`, default 2m) | Docker |
| `make test-selfhost` | Self-hosted install; builds the image once | Docker |
| `make bench-m7` | Scanner benchmarks, see `bench/m7/README.md` | Docker, about 4 GB free disk |
| `make release release-image test-release bench-release-rss` | Release artifacts, image, verification, agent RSS | Docker; see [docs/contributors/release.md](docs/contributors/release.md) |

CI (`.github/workflows/ci.yml`) runs formatting, `go vet`, `make test`, `make lint`, `make vuln`, `make build`, the integration, load and self-host suites, and the web typecheck, unit tests, build and `npm audit`. The Docker-heavy acceptance suites (`test-e2e`, `test-deploy`, `test-alerts`, `test-security`, `test-operator`) are run through the acceptance script below, not in CI.

## Regenerate the README screenshots

`make screenshots` starts a real server and agent with a small demo (a host named `hel1-prod` running `shop-web`, `shop-api`, `shop-db` and a crash-looping `worker`, a scripted AI provider and a crafted vulnerability database with synthetic CVEs), lets it collect metrics for a few minutes, and drives Chromium over it. It needs Docker, Chromium at `/usr/bin/chromium`, a global Playwright install (`npm root -g`) and the cached images above, takes about 5 minutes, and rewrites the seven PNGs in `docs/images/`. It removes the shared `haetae-test-postgres` container (the next integration test recreates it) and every demo container when it ends. Look at the images before committing them.

## Run the acceptance suite

```sh
scripts/acceptance.sh        # about 40 minutes
cat acceptance-results/summary.txt acceptance-results/measures.txt
```

It runs every suite in sequence (they share Docker and two cores, so never in parallel) and needs Docker, Chromium/Playwright, cached busybox, caddy, alpine and nginx images, and no other load. `acceptance-results/` is not committed. Results and caveats: [docs/contributors/acceptance.md](docs/contributors/acceptance.md).

## Code layout

| Path | Contents |
|---|---|
| `cmd/` | Entry points: `haetae-agent`, `haetae-server`, `haetae-inventory` |
| `internal/` | Everything else, one package per concern ([docs/contributors/architecture.md](docs/contributors/architecture.md)) |
| `proto/` | Protobuf schemas, the agent contract (generated code in `internal/gen`) |
| `web/` | React + Vite + TypeScript UI, embedded in the server binary |
| `test/` | Acceptance harnesses and corpora, one directory per suite |
| `deploy/` | Server `compose.yaml` and `install-server.sh` |
| `docs/` | User documentation (the published site); `docs/contributors/` holds the spec, roadmap, `design/` per milestone and `adr/` decisions |
| `bench/`, `scripts/` | Benchmarks, acceptance and release scripts |

## Conventions

- Go standard `regexp` (RE2) only; no backtracking regex engines, especially in the redaction path.
- Tests sit next to the code (`_test.go`). Tests that need Docker or PostgreSQL use the `integration` build tag.
- Commit messages: imperative subject under 72 characters; the body explains why.
- Work follows [docs/contributors/roadmap.md](docs/contributors/roadmap.md): one GitHub issue per step. A change is done when it meets the relevant "Done correctly" criteria in the spec and stays inside "Out of scope for v1".
- Do not weaken a hard constraint to make a test pass; fix the code or record the miss. The constraints:
  - the agent uses under 50 MB RAM, needs no inbound port and reconnects by itself;
  - with a non-local AI, personal data and secrets are removed on the host first, and if that fails nothing is sent;
  - the operator never deletes data, acts only at the configured autonomy level, and treats logs as untrusted text;
  - a failed scan reports "scan failed", never "no vulnerabilities";
  - rule-based alerts keep working when the AI is unreachable.
- Structural changes start from [docs/contributors/adr/0001-stack.md](docs/contributors/adr/0001-stack.md).

## Pull requests

1. Open or pick an issue; keep the PR to one step or fix.
2. Fill in [the PR template](.github/pull_request_template.md): summary, spec criteria, acceptance criteria ticked only with evidence (test names, commands, measured numbers), and review.
3. CI must be green. Run `make test` and `make lint` locally first.
4. An independent review is expected: a reviewer who did not write the code, with every blocking finding fixed or answered in the PR.
5. PRs are squash-merged.

Be honest in evidence: say what was verified against a fake and what against the real thing, as [docs/contributors/acceptance.md](docs/contributors/acceptance.md) does.
