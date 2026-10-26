# Installing the Haetae server

The control plane is two containers (the Haetae server and PostgreSQL) started with Docker Compose. Self-hosted Haetae has every feature and sends no data to us; the server makes outbound connections only to the vulnerability database listing (on by default in the shipped stack; set `HAETAE_VULN_DB_URL=off` in `haetae.env` to disable it) and to the chat channels and AI provider you configure.

## Requirements

- A Linux host with Docker and Compose v2 (`docker compose version`), and `curl`.
- Free TCP ports **8080** (web UI, TLS) and **8443** (agent gateway, mutual TLS), or choose others with `HAETAE_WEB_PORT` and `HAETAE_GATEWAY_PORT`.
- Hosts you manage must be able to reach both ports of this server. The server never connects to them.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/alexseymer/haetae/main/deploy/install-server.sh | sh
```

That trusts GitHub and TLS to deliver the script. For stricter setups, download `install-server.sh`, read it, then run `sh install-server.sh`. From a checkout: `sh deploy/install-server.sh`.

### What the installer does

1. Checks for `docker` and Compose v2, validates its settings, and refuses to start if the web or gateway port is held by something else (checked with `ss` when available).
2. Creates `/opt/haetae` (override with `HAETAE_DIR`) and writes `compose.yaml` there.
3. Creates `haetae.env` (see below) if it is missing, and writes `.env` (mode 0600) with `HAETAE_IMAGE`, `HAETAE_HOST`, `HAETAE_WEB_PORT`, `HAETAE_GATEWAY_PORT` and a generated 32-character database password. On re-runs the password is kept.
4. Runs `compose pull` (skip with `HAETAE_NO_PULL=1`) and `compose up -d`.
5. Waits up to 180 s for the server container to become healthy, then prints the URL and the first-run setup code.

Overrides (environment variables of the installer): `HAETAE_DIR`, `HAETAE_IMAGE` (default `ghcr.io/alexseymer/haetae-server:latest`), `HAETAE_HOST` (default: first address from `hostname -I`, else the hostname; it becomes the name in the server certificate and in the URLs agents use), `HAETAE_WEB_PORT`, `HAETAE_GATEWAY_PORT`, `COMPOSE_PROJECT_NAME` (default `haetae`), `HAETAE_NO_PULL`.

Use a DNS name or address that your managed hosts can reach as `HAETAE_HOST`, for example `HAETAE_HOST=haetae.example.com sh install-server.sh`.

The server container runs as a non-root user with a read-only root filesystem and no capabilities; the Postgres image is pinned by digest. State lives in two named volumes, `db` (database) and `data` (mounted at `/var/lib/haetae`).

## First login

Open the printed URL. The certificate is issued by the server's own CA, so the browser warns until you supply your own. Enter the setup code, then the organisation name, email and password of the first administrator. The setup code file is removed afterwards. To print it again before setup is done:

```sh
docker compose --project-directory /opt/haetae exec -T server /usr/local/bin/haetae-server setup-code
```

Afterwards it answers "no setup code: setup is already done". Add more users under **Settings**, **Account**, **Add user** (roles `member` or `admin`). Only admins can enroll hosts, deploy, change settings and press chat buttons.

## Configuring the server

Your settings live in **`/opt/haetae/haetae.env`**. The installer creates it once, as a commented template (mode 0600, because it can hold tokens), and prints one line saying so. It **never overwrites it**, and `compose.yaml` passes it to the server (`env_file`), so chat, AI, TLS and other settings survive every re-run and upgrade. The installer rewrites `compose.yaml` and `.env` on each run, so do not edit those.

Edit `haetae.env` (one `KEY=value` per line, remove the `#` of the variables you want), then apply it:

```sh
sh install-server.sh        # or: cd /opt/haetae && docker compose up -d
```

If you run Compose by hand, name both settings files so `HAETAE_VULN_DB_URL` and `HAETAE_GATEWAY_NAMES` in `haetae.env` take effect: `docker compose --env-file .env --env-file haetae.env up -d` (the installer does this for you). The template lists the supported variables: chat (`HAETAE_TELEGRAM_TOKEN`, `HAETAE_SLACK_*`, `HAETAE_SMTP_*`, `HAETAE_SIGNAL_URL`), AI (`HAETAE_AI_*`), web TLS (`HAETAE_WEB_TLS_CERT`, `HAETAE_WEB_TLS_KEY`), `HAETAE_TRUSTED_PROXIES`, `HAETAE_VULN_DB_URL` and `HAETAE_GATEWAY_NAMES`. The variables compose.yaml itself sets (database, listen addresses, URLs) cannot be changed in this file, except `HAETAE_VULN_DB_URL` and `HAETAE_GATEWAY_NAMES`, which compose.yaml only defaults.

Variables read by the server:

| Variable | Meaning |
|---|---|
| `HAETAE_DATABASE_URL` | PostgreSQL URL (set by compose.yaml) |
| `HAETAE_DATA_DIR` | State directory, default `/var/lib/haetae` |
| `HAETAE_WEB_ADDR`, `HAETAE_GATEWAY_ADDR` | Listen addresses inside the container (`:8080`, `:8443`) |
| `HAETAE_WEB_URL`, `HAETAE_GATEWAY_URL` | URLs users and agents use; shown in enrollment commands |
| `HAETAE_GATEWAY_NAMES` | Comma-separated names/IPs for the server certificates (default: `HAETAE_HOST`) |
| `HAETAE_WEB_TLS_CERT`, `HAETAE_WEB_TLS_KEY` | Your own web certificate and key (set both) |
| `HAETAE_TRUSTED_PROXIES` | Reverse proxy addresses/CIDRs whose `X-Forwarded-For` is trusted |
| `HAETAE_VULN_DB_URL`, `HAETAE_VULN_DB_DIR` | Vulnerability database; compose.yaml defaults the URL to Anchore's listing, `off` disables it, see [security.md](security.md) |
| `HAETAE_PUBLIC_URL` | URL put in chat and email links (default: `HAETAE_WEB_URL`) |
| `HAETAE_CHAT_SECRET` | Optional chat HMAC secret, at least 32 bytes; otherwise generated once in `chat.secret` in the data dir |
| `HAETAE_TELEGRAM_TOKEN`, `HAETAE_SLACK_*`, `HAETAE_SMTP_*`, `HAETAE_SIGNAL_URL` | Chat channels, see [chat.md](chat.md) |
| `HAETAE_AI_*` | AI operator, see [ai-operator.md](ai-operator.md) |

## Ports and TLS

- **8080** web UI and API, always TLS. Also serves `/install.sh` and `/download/` (agent binaries and `SHA256SUMS`) to hosts being enrolled.
- **8443** agent gateway. Agents authenticate with client certificates issued by the server's CA, so this port must reach the Haetae server itself, not a TLS-terminating proxy.

Own certificate: mount it into the server container and set both variables in `haetae.env`:

```
HAETAE_WEB_TLS_CERT=/certs/fullchain.pem
HAETAE_WEB_TLS_KEY=/certs/privkey.pem
```

With your own certificate the enrollment command no longer pins the web certificate key, so hosts must trust it through their normal CA store. Public certificate issuance was not tested; only the server's own CA and a supplied certificate file path are covered.

### Reverse proxy

The web listener speaks only TLS, so a proxy must connect to `https://<server>:8080` upstream (accepting the server certificate, or using your own certificate there). Set `HAETAE_WEB_URL` to the public address users type, and `HAETAE_TRUSTED_PROXIES` to the proxy's address or CIDR (for example `172.18.0.0/16`, comma-separated list) so login rate limiting sees the real client from `X-Forwarded-For`. Pass port **8443** through as plain TCP. Setting the old `HAETAE_TRUST_PROXY` makes the server refuse to start.

## Data, backups, retention

- Database: `docker compose --project-directory /opt/haetae exec -T db pg_dump -U haetae haetae > haetae.sql`
- Data volume (`haetae_data` for project name `haetae`): holds the agent CA, `secret.key` (encrypts Git tokens), `chat.secret`, the web certificate and the vulnerability database. **Losing the CA or `secret.key` means hosts must re-enroll and Git tokens must be re-entered.** Back it up with the database, for example `docker run --rm -v haetae_data:/data -v "$PWD":/backup alpine tar czf /backup/haetae-data.tgz -C /data .` (not run in this project's tests).
- Metrics retention: 48 h at 10 s, 30 days at 1 min, 395 days at 1 h.

## Upgrade

Re-run the installer with the same `HAETAE_DIR`. It keeps the database password and volumes and recreates containers from the current image (database migrations run at server start). Agents are upgraded by re-running the host install command; see [enroll.md](enroll.md).

## Uninstall

```sh
docker compose --project-directory /opt/haetae down -v   # -v deletes database and data volume
rm -rf /opt/haetae
```

Omit `-v` to keep the volumes.

## Trust model

1. The server generates an agent CA in its data directory; agents pin its fingerprint at enrollment.
2. The web UI uses a certificate from that CA unless you supply one.
3. The one-line host command pins the web certificate public key, so a network attacker cannot substitute another server.
4. Enrollment tokens are single use and time limited; only an admin can create them.
5. Sessions, enrollment and metric intake need no outbound network access from the server.
