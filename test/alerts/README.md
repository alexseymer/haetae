# Alerts / chat end-to-end tests (M6)

Real `haetae-server`, real `haetae-agent`, real Docker, and a fake Telegram Bot API.
No AI is configured. Rule alerts and the daily briefing must work without it (S9).

## Requirements

- Docker daemon and permission to talk to it
- Cached image: `busybox:1.37` (and `postgres:17-alpine` via `storetest`)
- Go toolchain on `PATH` (see the Makefile / CI image)
- `ss` (iproute2) for the outbound-peer assertion

## Server / agent env

| Variable | Role |
| --- | --- |
| `HAETAE_TELEGRAM_TOKEN` | Enables the Telegram channel |
| `HAETAE_TELEGRAM_BASE_URL` | Points the Bot API client at the fake HTTP server (default is `https://api.telegram.org`) |
| `HAETAE_BRIEFING_NOW` | **Test-only.** When set (any non-empty value), the briefing scheduler treats every org as due on each tick. Exactly-once delivery is still enforced by the `briefings` primary key. Do not set in production. |

Org settings are tuned via `PUT /api/settings/alerts` for fast runs: `offlineAfterS=30`, `sustainedS=10`.

All Docker resources created by these tests use the `htest-` name prefix.
`TestMain` fails the package if any `htest-` leftover remains (`internal/testutil/dockerclean`).

## Run

```bash
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH
make test-alerts
# or:
go test -count=1 -tags e2e_alerts -timeout 20m -v ./test/alerts/
```

Acceptance also requires:

```bash
go vet -tags e2e_alerts ./test/alerts/
gofmt -l .
make lint
```

## Scenarios

| Subtest | What it checks |
| --- | --- |
| `no_ai_outbound` | Server env has no AI variables; `ss` shows only `127.0.0.1`/`::1` peers for the server pid |
| `quiet_day_briefing` | Forced briefing → exactly one ≤3-line / ≤300-char message in web + Telegram; second trigger + restart still one |
| `crash_loop` | Crash-looping busybox → `crash_loop` high + delivery; remove container → resolve after 2 evals, one resolve message |
| `host_offline` | Stop agent → `host_offline` critical; web + Telegram (inline keyboard) within 60 s of `lastSeen` (MEASURE) |
| `buttons_mute` | Mute 30 days from linked admin; unlinked / tampered / member rejected; later alerts stored without new messages |
| `tenant_isolation` | Second org session cannot read first org incidents / messages / accounts |

Look for `MEASURE` lines in the verbose output for timings.
