# M6 design: alerts, incidents, chat, briefing

Binding for workers and reviewers. Scope is roadmap M6 (spec S3, S9). Schema is `internal/store/migrations/0007_alerts_chat.sql` (authoritative; do not change it without telling the coordinator).

## Packages and ownership

| Package | Owns | Depends on |
|---|---|---|
| `internal/alerts` + `internal/store/alerts.go` | rule engine, incident grouping, mutes, evaluator loop | store reads (hosts, containers, metrics), `Notifier` interface |
| `internal/chat` + `internal/store/chat.go` | message outbox delivery, channel bridges (web, Telegram, Slack, email, Signal), account linking, action handling | store |
| `internal/briefing` + `internal/store/briefing.go` | daily briefing text and scheduling | store reads, `Enqueuer` interface |
| `internal/api/chat.go`, `internal/api/alerts.go`, web UI | HTTP API and screens (later worker, after the three above) | all |

Cross-package contracts (defined by the consumer, so packages compile independently):

```go
// alerts.Notifier: called after an incident is opened, escalated or resolved.
type Notifier interface {
    IncidentChanged(ctx context.Context, orgID uuid.UUID, inc store.Incident, alerts []store.Alert) error
}
// briefing.Enqueuer and the chat package's implementation:
type Enqueuer interface {
    EnqueueMessage(ctx context.Context, orgID uuid.UUID, m store.ChatMessage) error
}
```
`chat.Service` implements both (`IncidentChanged` renders the incident message, `EnqueueMessage` writes `chat_messages`).

## Rules (S9: never depend on AI)

Evaluated every 15 s per org by a single background loop on the server role (not user requests), reading what the agent already reports. Each firing alert has a stable `dedupe_key` (`rule|container|...`); `alerts_one_firing` makes re-evaluation idempotent. An alert resolves when its condition has been false for 2 consecutive evaluations.

| Rule | Condition | Severity |
|---|---|---|
| `crash_loop` | container restarted >= 3 times in 10 min | high |
| `oom_kill` | container OOM-killed (any, in the last 10 min) | high |
| `unhealthy` | container health `unhealthy` for >= 60 s | medium |
| `host_offline` | no session for `offline_after_s` | critical |
| `cpu_high` / `mem_high` / `disk_high` | above org threshold for `sustained_s` (disk: critical at >= 97%) | medium (disk high) |
| `deploy_failed` | deploy ended `failed`, `rolled_back` or `rollback_failed` | high |
| `new_cve` | reserved; M7 | |

Thresholds and time zone come from `org_settings` (defaults in the migration). Mutes (`mutes`, org + optional host + optional rule) suppress notification, never evaluation: a muted alert is still stored and shown. No rule calls an AI or any network service.

## Incidents

One open incident per host (`incidents_one_open`). New alerts on a host join its open incident; severity is the max of firing alerts; title is built from the highest-severity alert ("shop: out of memory on hel1-prod"). When all alerts resolve, the incident resolves. `Notifier.IncidentChanged` is called on open, on severity escalation, on a new alert joining and on resolve; low and medium severity create/update the incident but only `high` and `critical` create an immediate chat message (others appear in the briefing).

## Chat

- Every outgoing message is a `chat_messages` row (`direction='out'`, `delivered_at IS NULL`). A delivery loop claims due rows (`FOR UPDATE SKIP LOCKED`), sends via the channel, sets `delivered_at`, or backs off exponentially (`attempts`, `next_attempt_at`, `delivery_error`, give up after 8 attempts but keep the row visible). **At-least-once delivery** (Send success + MarkDelivered failure may reclaim; `sent_at` is persisted on Send success and an in-process delivered-set with TTL/size bounds suppresses resend while MarkDelivered is retried). The web channel is the row itself.
- **High-severity events reach the web chat and a Telegram endpoint within 60 s** (measured against a fake endpoint): enqueue is immediate, delivery loop wakes every 1 s or on a Postgres `LISTEN/NOTIFY` hint.
- Fan-out: an incident message is written once for the web channel (org wide, `account_id NULL`) and once per verified linked account of each other channel.
- **Channels**: web (UI polls `GET /api/chat/messages?after=`, posts via API); Telegram (Bot API long polling, base URL override via `HAETAE_TELEGRAM_BASE_URL` only when `HAETAE_TEST_HOOKS=1` — otherwise ignored with a warning; production leaves it empty for `api.telegram.org`; token from env `HAETAE_TELEGRAM_TOKEN`); Slack (Socket Mode over WebSocket, app token from env; one small vetted dependency is allowed for WebSocket, pin it and run `make vuln`); email (SMTP via `net/smtp` with STARTTLS required unless `HAETAE_SMTP_INSECURE=1`; outbound only plus a link to the web UI; inbound email is out of scope); Signal (talks to a `signal-cli` JSON-RPC endpoint when `HAETAE_SIGNAL_URL` is set, otherwise disabled). A channel with no configuration is off and makes no outbound connection (S11: no unconfigured outbound calls).
- **Linking**: signed-in user requests a code for a channel (`chat_link_codes`, 8 chars from a 32-symbol alphabet, rejection sampling, stored as SHA-256, valid 10 min, single use, per-user rate limit). The user sends `/link CODE` to the bot (or the Slack app / email reply token); the bridge consumes the code and creates the `chat_accounts` row binding `(channel, external_id)` to the user in one transaction (wrong channel or insert failure leaves the code unused). `UNIQUE(channel, external_id)` prevents taking over a linked account.
- **Actions** (buttons): `Later`, `Mute 30 days`, `Acknowledge`. Telegram `callback_data` carries a short opaque ref (`a:` + 16-char base64url from `chat_action_refs`, ≤64 bytes). Slack/web keep an HMAC token where size allows. Each `(message_id, action)` is single-use; a second press replies "Already handled". Mutes dedupe on `(org, host, rule)` by extending `until`. Accepted only if the sender's `(channel, external_id)` is a verified `chat_accounts` row whose user is an admin of the message's org. Anything else is rejected with a short reply and an audit log line (no information about the incident). Actions that change servers (update and redeploy) are not offered until M8.
- Inbound free text from linked accounts is stored as `direction='in'` messages (the AI operator in M8 answers them; until then the reply is a fixed "The operator is not enabled yet; rule alerts are active."). Text from unlinked accounts is ignored except `/link` and `/start` (those replies are rate-limited per external id).
- Message text is built from our own templates and values; log lines are never copied into a message unescaped (treat as untrusted): any value from containers (names, images, log excerpts) is escaped for the channel's markup (Telegram HTML / Slack mrkdwn / plain text) and length-limited.
- Secrets: bot tokens only from environment; never logged or stored in the DB. Chat HMAC secret from `HAETAE_CHAT_SECRET` (≥32 bytes) or a once-generated `chat.secret` in the data dir (0600); never a hardcoded fallback.
- **Test-only hooks**: `HAETAE_BRIEFING_NOW` skips the briefing hour window and is ignored unless `HAETAE_TEST_HOOKS=1` is also set. `HAETAE_TELEGRAM_BASE_URL` points the Telegram bridge at a fake Bot API and is likewise ignored unless `HAETAE_TEST_HOOKS=1`.

## Briefing

Per org, once per local day at `briefing_hour` in `time_zone` (tolerate a missed minute; catch up at most 6 h late, otherwise skip). `briefings(org_id, local_date)` primary key plus an exclusive `lease_until` and deterministic chat `idempotency_key` make it exactly-once even with several server instances or a crash between enqueue and `enqueued_at`. Text rules: at most 3 lines and 300 characters total (hard-truncate with a visible `…` and an `all incidents` link); line 1 is `08:00 briefing, N hosts, M containers, all running.` (or the count of problems); then at most 2 lines for the most severe items, ordered by severity then recency. **Open incidents are always included regardless of age** (an unresolved incident remains briefing-relevant); resolved incidents and firing alerts are limited to the last 24 h. A quiet day is exactly one line. Send through the same enqueue path to the web chat and all verified accounts. DST: use `time.LoadLocation`; a non-existent local time runs at the next valid minute; a repeated hour sends once.

## Acceptance (evidence required in the PR)

1. A high-severity event reaches the web chat and a fake Telegram endpoint within 60 s (measured, MEASURE line).
2. A quiet day produces exactly one briefing of at most 3 lines and 300 characters (test with a fake clock, also run twice and after a restart: still one).
3. With the AI disabled (no AI configured at all) every rule alert still fires (test per rule).
4. A button press from an unlinked account, from a linked non-admin account, and with a forged or expired token is rejected and changes nothing.
5. Tenant isolation: another org's incidents, messages, accounts and mutes are invisible and cannot be acted on (integration tests via RLS).
6. Delivery retry: a failing channel is retried with backoff, never duplicated after success, and never blocks other channels.
7. No outbound connection is made by a channel that is not configured (test with a dialer that fails the test).
