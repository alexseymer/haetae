# Enrolling a host

A host is any Linux server (amd64 or arm64) with Docker that you want Haetae to watch and deploy to. The agent connects **outbound** to the server's gateway port (8443 by default). It opens no listening port and needs no inbound firewall rule, except ports 80 and 443 if you use HTTPS ingress for deployed apps.

## Enroll

1. In the web UI: **Hosts**, **Add host**. Enter a **Host name** and **Validity (hours)** (1 to 168, default 24), then **Create token**.
2. Copy the command shown (the token works once) and run it on the host as root. Its shape:

```sh
curl -fsS -k --pinnedpubkey '<web key pin>' 'https://haetae.example.com:8080/install.sh' | sh -s -- \
  --server 'https://haetae.example.com:8443' --token '<token>' --ca-pin 'sha256:<fingerprint>' \
  --web 'https://haetae.example.com:8080' --web-pin '<web key pin>'
```

(`-k` only skips the public CA check; `--pinnedpubkey` is what authenticates the server. If you configured your own web certificate, the pin parts are absent and normal certificate validation applies.)

The host shows **Online** on the Hosts page, normally within seconds, and metrics follow within minutes (spec targets: online within 30 s, metrics within 2 min; see [acceptance.md](acceptance.md) for what was measured).

Without the UI, an admin on the server can create a token:

```sh
docker compose --project-directory /opt/haetae exec server /usr/local/bin/haetae-server token create --name hel1-prod --ttl 24h
```

It prints the token and a `haetae-agent enroll --server ... --token ... --ca-pin ...` line. That line enrolls only; it does not install the binary or the systemd unit, so prefer the UI command for a new host. (`token create` uses the server's configuration, so run it inside the container as shown.)

## What the installer does

`install.sh` is served by your own server (`/install.sh`) and downloads the agent from the same server (`/download/`), so hosts need no access to GitHub. Steps, in order:

1. Requires Linux, root, `curl`, `sha256sum`, `mktemp`; picks `amd64` or `arm64`.
2. Downloads `haetae-agent-linux-<arch>`, `haetae-inventory-linux-<arch>` and `SHA256SUMS`, and verifies both binaries against it. A mismatch aborts with nothing installed.
3. Installs `/usr/local/bin/haetae-agent` and `/usr/local/bin/haetae-inventory`; creates `/var/lib/haetae-agent` (mode 0700).
4. Enrolls (token passed by environment, not on the command line). If `/var/lib/haetae-agent/identity.json` exists it skips enrollment, so re-running upgrades the binaries safely.
5. Creates the system user `haetae-agent` (no login shell) and, if a `docker` group exists, adds it to that group. **This is root-equivalent on the host** and the installer prints a warning. If Docker is missing it warns that the agent needs the Docker socket.
6. Writes `/etc/systemd/system/haetae-agent.service`, enables and restarts it, and waits up to 30 s for it to become active.

`HAETAE_SKIP_SYSTEMD=1` installs and enrolls but skips the user and unit. Use `journalctl -u haetae-agent` for logs.

## The systemd unit

Runs as `haetae-agent` with `Restart=always` and `RestartSec=5`, and hardening: `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `ProtectKernelModules`, `LockPersonality`, write access only to `/var/lib/haetae-agent`, address families limited to Unix, IPv4, IPv6 and netlink. `CapabilityBoundingSet=CAP_NET_ADMIN` and `AmbientCapabilities=CAP_NET_ADMIN` exist for the conntrack outbound watch; the agent drops the ambient capability at start so child processes do not inherit it. `Delegate=memory pids cpu` and `MemoryMax=400M` let the agent run the inventory helper in a separate memory-limited cgroup (agent and helper together stay under 400 MB).

The agent keeps its identity (`identity.json`, `client.crt`, `client.key`, `ca.crt`), deploy projects and the Caddy state in `/var/lib/haetae-agent`.

## Agent commands and flags

```
haetae-agent enroll --server URL --ca-pin PIN [--token TOKEN] [--state-dir DIR]
haetae-agent run [--state-dir DIR] [flags]
haetae-agent --version
```

`--token` wins over the `HAETAE_ENROLL_TOKEN` environment variable. Flags of `run` (defaults in brackets): `--state-dir` (`/var/lib/haetae-agent`), `--docker-socket` (`/var/run/docker.sock`), `--proc-root` (`/proc`), `--disk-path` (`/`, filesystem whose usage is reported), `--interval` (`10s`, metrics sampling), `--ingress-http-port` (`80`), `--ingress-https-port` (`443`), `--max-autonomy` (`act`).

`--max-autonomy report|suggest|act` is a local cap on what the AI operator may do on this host. The effective level is the lower of the organisation's setting and this cap; `report` makes the agent refuse operator restarts however the server is configured. See [ai-operator.md](ai-operator.md). The installer does not set it. To set it, add a drop-in (the installer rewrites the main unit on every run):

```sh
sudo systemctl edit haetae-agent
# in the editor:
# [Service]
# ExecStart=
# ExecStart=/usr/local/bin/haetae-agent run --state-dir /var/lib/haetae-agent --max-autonomy report
sudo systemctl restart haetae-agent
```

Agent environment variables for tuning the scan: `HAETAE_INVENTORY_MEM`, `HAETAE_INVENTORY_TIMEOUT` (see [security.md](security.md)); set them with `Environment=` in the same drop-in.

## Networking and reconnect

- Only outbound HTTP/2 with mutual TLS to the gateway URL. Heartbeats keep the stream alive; commands from the server (deploys, scans) travel back on that same connection.
- After a lost connection, server restart, agent restart or reboot the agent reconnects by itself with jittered exponential backoff (1 s growing to 30 s). It keeps its stored identity, so no new token is needed. Metrics batches (about five minutes' worth) are buffered while disconnected.
- The server marks a host offline after the org's **Offline after seconds** setting (default 120, **Settings**, **Alerts**) and raises a `host_offline` alert.
- Memory: the resident agent targets under 50 MB (Go heap soft limit 40 MiB). Measured values are in [acceptance.md](acceptance.md) and [benchmarks-m7.md](benchmarks-m7.md) (about 20 MiB peak in the 3-minute scan run); the inventory helper is a separate short-lived process with its own 256 MiB cap.

## Rotating and removing a host

Admins can remove a host: open it and press **Remove host** (API: `DELETE /api/hosts/{id}`). Confirming disconnects the agent at once and it cannot reconnect with its old certificate. The host disappears from the host list, alerts, incidents and briefings; its open incidents and alerts are resolved and nothing is deleted from the database. Containers on the host are not touched and keep running, and the agent software stays installed until you remove it (below). The action is logged as `audit: host removed` with the actor. The same host name can be enrolled again afterwards with a new token.

- There is still no certificate rotation: client certificates are issued for one year and the code has no renewal path, so plan to re-enroll a host before a year passes (remove it, then enroll again with a new token on that host after deleting its state directory, `/var/lib/haetae-agent`).
- To stop a host without removing it: `sudo systemctl disable --now haetae-agent`. It shows Offline and keeps its history. Deployed apps keep running; remove projects in the UI first if you want them stopped.
- To fully remove the agent from a host: stop and disable the service, delete `/etc/systemd/system/haetae-agent.service`, `/usr/local/bin/haetae-agent`, `/usr/local/bin/haetae-inventory` and `/var/lib/haetae-agent`, and remove the `haetae-agent` user. The `haetae-caddy` container and containers of deployed projects are not removed by this.
