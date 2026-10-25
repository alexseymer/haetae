# Self-hosting Haetae

## Install

On a host with Docker (Compose v2):

```sh
curl -fsSL https://raw.githubusercontent.com/alexseymer/haetae/main/deploy/install-server.sh | sh
```

That one-liner trusts GitHub and TLS to deliver the script. For stricter setups, download `install-server.sh`, read it, verify it against the SHA256SUMS published with each release, then run it (`sh install-server.sh`). Releases will publish `install-server.sh` alongside SHA256SUMS.

Or run the script from a checkout: `sh deploy/install-server.sh`.

### Requirements

- Docker with Compose v2 (`docker compose version`)
- Free TCP ports **8080** (web) and **8443** (agent gateway), or set `HAETAE_WEB_PORT` / `HAETAE_GATEWAY_PORT`

### What the installer does

It creates `/opt/haetae` (override with `HAETAE_DIR`), writes `compose.yaml` and a mode-`0600` `.env` (Postgres password generated once and kept on re-runs), pulls the server image unless `HAETAE_NO_PULL=1`, starts Postgres and the server, waits until the server is healthy, and prints the URL and first-run setup code.

Useful overrides: `HAETAE_IMAGE`, `HAETAE_HOST`, `COMPOSE_PROJECT_NAME`.

Base images in `Dockerfile` and `deploy/compose.yaml` (Postgres) are pinned by digest (`image:tag@sha256:…`). Dependabot's `docker` ecosystem entry updates those digests.

## First login

Open the printed URL. The browser will warn about the certificate: it is issued by the server's own CA unless you supply one. Enter the setup code, then create the first administrator (organisation name, email, password). The setup code file is removed after that; only someone with access to the server can have read it.

## Adding a host

In the web UI, create an enrollment token. The UI shows a one-line command that downloads the agent install script from this server (with certificate pinning) and enrolls the host. Run that command on the host.

The agent installer adds the `haetae-agent` user to the `docker` group when that group exists, and prints a warning: membership in the docker group is equivalent to root on the host, and the agent can run containers with any privileges.

## Your own certificate

Set `HAETAE_WEB_TLS_CERT` and `HAETAE_WEB_TLS_KEY` on the server service (paths inside the container, usually via a bind mount) so browsers trust the web UI.

If you terminate TLS on a reverse proxy for the web port, pass the **gateway** port **8443** through as plain TCP: agents use mutual TLS there and must reach the server certificate, not the proxy's. Set `HAETAE_TRUSTED_PROXIES` to the proxy addresses/CIDRs whose `X-Forwarded-For` you trust.

## Backups

- Database: `docker compose --project-directory /opt/haetae exec -T db pg_dump -U haetae haetae > haetae.sql`
- Data volume: holds the agent CA and related state; back it up with the rest of Docker volumes for the Compose project

## Upgrading

Re-run the install script (same `HAETAE_DIR`). It keeps the database password and data volumes and recreates containers from the current image.

## Uninstalling

```sh
docker compose --project-directory /opt/haetae down -v
rm -rf /opt/haetae
```

`down -v` deletes the database and the data volume (agent CA). Omit `-v` to keep volumes.

## Trust model

1. The server generates an agent CA in its data directory; agents pin that CA at enrollment.
2. The web UI uses a certificate from that CA by default; browsers warn until you replace it.
3. The one-line host install command pins the web certificate public key so a network attacker cannot substitute another server.
4. Enrollment tokens are single-use and time-limited; only an admin can mint them.
5. Rule-based operation (sessions, enrollment, metrics intake) does not require outbound network access from the server.
