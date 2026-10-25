# Deploy end-to-end tests (M5)

Real server, real agent, real Docker. No mocks.

## Requirements

- Docker daemon (compose plugin) and permission to talk to it
- Cached local images used by the scenarios: `busybox:1.37`, `caddy:2.10.2-alpine`
  (and `postgres:17-alpine` via the shared `storetest` helper)
- Go toolchain on `PATH` (see the Makefile / CI image)
- `git` for local `file://` repositories

The agent is started with:

- `HAETAE_AGENT_ALLOW_LOCAL_GIT=1` — accept `file://` git URLs
- `HAETAE_AGENT_CADDY_TLS=internal` — Caddy internal CA
- `HAETAE_AGENT_CADDY_NAME` / `HAETAE_AGENT_CADDY_VOLUME_PREFIX` prefixed `htest-`
- shortened health/watch/start intervals so the 30 s push-to-deploy budget is reachable

The server is started with `HAETAE_ALLOW_LOCAL_GIT=1` so the API accepts the same
`file://` URLs the agent can fetch.

All Docker containers, networks and volumes created by these tests use the
`htest-` name prefix and are removed in `t.Cleanup` (background context).
`TestMain` fails the package if any `htest-` resource remains.

## Run

```bash
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH
make test-deploy
# or:
go test -count=1 -tags e2e_deploy -timeout 20m -v ./test/deploy/
```

`go vet -tags e2e_deploy ./test/deploy/` and `gofmt` must stay clean.

## Scenarios

| Subtest | What it checks |
| --- | --- |
| `inline_compose_https` | Inline busybox httpd + domain → healthy ≤ 60 s; HTTPS 200 via Caddy |
| `bad_build_keeps_old` | Failed image build leaves the previous version serving |
| `crash_rolls_back` | Exiting container → `rolled_back`, v1 serving, error recorded |
| `secret_redacted` | AWS-shaped secret in build output never reaches API/DB/agent log |
| `volumes_survive` | Named volume marker survives rollback and project `DELETE` |
| `push_to_deploy` | `autoDeploy` + git push → healthy ≤ 30 s when layers are cached |
| `member_forbidden` | Member gets 403 on deploy/rollback; git token never in API or server log |

Look for `MEASURE` lines in the verbose output for timings.
