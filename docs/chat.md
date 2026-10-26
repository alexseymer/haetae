# Chat, alerts and the daily briefing

Haetae reports through chat channels. A channel that is not configured is off and makes no network connection. Check which are active in **Settings**, **Chat channels**, **Channels** (each shows `enabled` or `not configured` with the variable to set).

Set variables in `/opt/haetae/haetae.env` (see "Configuring the server" in [install.md](install.md)) and re-run the installer (or `docker compose up -d`) to apply them.

| Channel | Variables | Notes |
|---|---|---|
| Web | none | Always on. **Chat** in the top bar. |
| Telegram | `HAETAE_TELEGRAM_TOKEN` | Bot API with long polling (outbound only) |
| Slack | `HAETAE_SLACK_APP_TOKEN` and `HAETAE_SLACK_BOT_TOKEN` (both required) | Socket Mode over WebSocket (outbound only) |
| Email | `HAETAE_SMTP_ADDR` (`host:port`, default port 587) and `HAETAE_SMTP_FROM`; optional `HAETAE_SMTP_USER`, `HAETAE_SMTP_PASS` | Send only. STARTTLS is required; `HAETAE_SMTP_INSECURE=1` skips it (tests only) |
| Signal | `HAETAE_SIGNAL_URL` | URL of a `signal-cli` JSON-RPC HTTP endpoint you run yourself |

`HAETAE_PUBLIC_URL` (default `HAETAE_WEB_URL`) is used for links in emails; it must be `https://` (or `http://` for localhost). Bot tokens are read from the environment only and never stored in the database.

**Verification status:** the channels were exercised against fake Telegram, Slack, Signal and SMTP servers in tests, not against the live services. Expect to fix small integration details (bot setup, Slack app permissions) on first real use.

## Web chat

**Chat** shows incident and briefing messages and lets you type questions to the operator. Messages can carry buttons. With the AI operator off, a question gets the fixed reply "The operator is not enabled yet; rule alerts are active." (or, when the AI is configured but unavailable, "The operator is unavailable (<reason>); rule alerts are active."). See [ai-operator.md](ai-operator.md).

## Telegram

1. Create a bot with Telegram's BotFather and copy its token into `HAETAE_TELEGRAM_TOKEN`.
2. Restart the server. **Settings**, **Chat channels**, **Link a channel**, **Link Telegram** shows a one-time code (valid 10 minutes, single use).
3. In Telegram, send `/link CODE` to your bot in a private chat. The bot answers "Linked. You will receive alerts here."

A wrong or used code gets "That link code is not valid." `/start` from an unlinked user gets a hint to link; any other text from an unlinked account is ignored.

## Slack

Create a Slack app with Socket Mode enabled, an app-level token (`HAETAE_SLACK_APP_TOKEN`) and a bot token (`HAETAE_SLACK_BOT_TOKEN`), interactivity on (for buttons), and direct-message events subscribed. The bot posts to the Slack user id of each linked account and reads direct messages; the exact scopes you need were not verified against Slack. Then **Link Slack** and send `link CODE` (no slash) as a **direct message** to the bot. Slack reserves messages starting with `/` for slash commands and the bridge registers none, so `/link` does not reach Haetae; `link CODE` in a direct message does.

## Email

Haetae sends incident and briefing emails (subject "Haetae alert" or "Haetae briefing") with a link to the web UI. Email is **outbound only**, so linking is confirmed in the web UI instead of by reply: **Settings**, **Chat channels**, **Link email**, **Send code**. The server emails a one-time code to the address you signed in with (never to an address you type; the page shows it masked) and you enter it under **Code from the email**. The code works once and expires after 10 minutes; at most 5 wrong guesses are allowed per 10 minutes and at most 5 codes may be active per user. The linked email account receives alerts and briefings. Email messages carry no buttons and never Approve/Decline for AI proposals; use the web interface, Telegram or Slack for those.

## Signal

Signal needs a `signal-cli` daemon that you register and run yourself; this release ships no compose file for it. Point `HAETAE_SIGNAL_URL` at its JSON-RPC HTTP endpoint (Haetae POSTs JSON-RPC `send` and `receive` calls to exactly that URL; single-account mode is assumed, no account name is sent). Then **Link Signal** and send `/link CODE` to the Haetae contact. Signal has no buttons: an incident message ends with `Ref: <8 chars> (reply: later|mute|ack <ref>)`; reply with `later`, `mute` or `ack` and the ref. Signal accounts cannot approve AI proposals; approve those in the web UI or another channel.

## Who can press buttons

Buttons (**Later**, **Mute 30 days**, **Acknowledge**, and **Approve** / **Decline** for operator proposals) are accepted only from a chat account linked through a one-time code to a user who is an **administrator** of the organisation. Anything else (unlinked account, linked member, forged or expired token, another organisation's message) is rejected with "Action not allowed." and logged as `chat action rejected` with a reason. Each button works once per message; a second press says "Already handled." **Mute 30 days** answers "Muted for 30 days."; **Later** "Snoozed."; **Acknowledge** "Acknowledged." Remove a linked account under **Linked accounts**, **Unlink**.

## What gets sent, and when

Rules run on the server every 15 s and do not use AI:

| Rule | Fires when | Severity |
|---|---|---|
| `crash_loop` | container restarted 3 or more times in 10 min | high |
| `oom_kill` | container OOM-killed in the last 10 min | high |
| `unhealthy` | health `unhealthy` for 60 s or more | medium |
| `host_offline` | no session for **Offline after seconds** | critical |
| `cpu_high`, `mem_high`, `disk_high` | above threshold for **Sustained seconds** (disk critical at 97% or more) | medium (critical for disk 97%) |
| `deploy_failed` | deploy ended failed, rolled back or rollback failed | high |
| `new_cve` | new high or critical CVE in a running container | see [security.md](security.md) |

Alerts group into one open incident per host. `high` and `critical` produce an immediate chat message; `low` and `medium` appear in the briefing and under **Incidents**. Delivery is retried with backoff (8 attempts) and is at-least-once.

### Thresholds, time zone, briefing time

**Settings**, **Alerts** (admins edit): **Time zone** (IANA name, default `UTC`), **Briefing hour (0-23)** (default 8), **CPU %** / **Memory %** / **Disk %** (default 90), **Sustained seconds** (default 300), **Offline after seconds** (default 120). Save with **Save alert settings**. The briefing is sent once per local day at the briefing hour in that zone; a quiet day is one line like `08:00 briefing, 3 hosts, 24 containers, all running.`; at most 3 lines and 300 characters. Open incidents are always included. If the server was down it catches up at most 6 hours late, otherwise skips that day.

### Mutes

A mute suppresses notifications, never evaluation: muted alerts are still stored and shown. Create one with **Mute 30 days** on a message. **Settings**, **Alerts**, **Mutes** lists them (host, rule, until); admins can **Remove** one.
