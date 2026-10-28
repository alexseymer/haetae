# Security scanning

Everything below appears on the host page, **Security** tab (`?tab=security`), with a badge on the Hosts list (count of critical plus high, `scan failed`, or `matching pending`). Members can read; admins can force a scan and change the update setting. The tab refreshes every 15 s while visible.

## What is scanned and when

- **Inventory:** for every running container image, the agent runs the helper `haetae-inventory` (Syft) to list installed packages with versions. It runs on container start, after a deploy turns healthy, once a day per running image (with jitter), and on **Scan now** (admins). One inventory at a time per host. The agent sends the package list to the server; no image content leaves the host.
- **CVE matching:** the server matches stored package lists with Grype, as a library, against the Grype vulnerability database (published by Anchore). Findings show CVE, severity, package, version and fixed version, with a link only when the advisory URL is https. After every database update all stored package lists are matched again.
- **Misconfigurations** (from Docker inspect, refreshed on every state sync): published ports (ports 80/443 owned by the managed Caddy are excluded; well-known database ports rate higher), Docker socket mounted (critical), runs as root, privileged, host network, host PID namespace, dangerous capabilities (`SYS_ADMIN`, `NET_ADMIN`, `SYS_PTRACE`, `ALL`), and expiring certificates (the agent probes the domains of the managed ingress and published TLS ports: under 14 days high, under 3 days or expired critical). Environment values are never read.
- **Outbound watch:** outbound connections per container, aggregated per destination and port in 60 s windows (at most 200 distinct destinations per window), kept 30 days.
- **Image updates:** the agent looks up the current registry digest of each running image daily.

## CVE matching

The shipped `compose.yaml` (and so the installer) sets `HAETAE_VULN_DB_URL` to Anchore's official listing, `https://grype.anchore.io/databases/v6/latest.json`, so a self-hosted stack matches CVEs without extra setup. To use a mirror, set `HAETAE_VULN_DB_URL` in `haetae.env` ([install.md](install.md)); `off` disables downloads (the Security tab then says **Vulnerability database not configured**). The **server binary itself** still defaults to no download when the variable is unset (empty or `off`), for example when run outside the shipped compose file.

The default is the listing used in the project's benchmark runs. The server checks it every 10 minutes (a small request), downloads and swaps in a newer database only when one exists, verifies the checksum, and keeps the old database if an update fails (the tab then shows **Vulnerability database update failed: &lt;error&gt;**). The database is stored in `HAETAE_VULN_DB_DIR` (default `<data dir>/vuln-db`). Per the design, a new advisory affecting a running container alerts within 15 minutes (target: 10 min listing check, re-match, 15 s rule evaluation); the `new_cve` rule works without the AI. It alerts for high and critical findings first seen after the container started; the backlog present at first evaluation is shown but not alerted.

## Reading the statuses

- **Scan failed: &lt;reason&gt;**: the inventory failed, timed out, ran out of memory, or the image is unsupported, or matching failed. Typical reasons: `inventory out of memory`, `inventory timed out`, `too many packages`, or the first line of the helper's error. The tab never shows a clean zero in this case, and the Hosts badge says `scan failed`.
- **Not scanned yet**: no inventory has been taken for that image.
- **Scan pending...**: an inventory is queued or running.
- **Matching pending**: the package list is stored but not yet matched (database not ready or the match queue is full). It is not clean; a periodic pass retries.
- **Scanned &lt;when&gt; (database &lt;date&gt;)**: success; counts show critical, high, medium and low.
- **Conntrack degraded/unavailable: &lt;reason&gt;** (Outbound connections): the outbound watch lost events or cannot run. **Not monitored (host network)** lists containers using `network_mode: host`, whose connections cannot be attributed.

## Outbound watch requirements

It listens to conntrack events through netlink in the host namespace, so the host needs the `nf_conntrack` kernel module loaded and the agent needs `CAP_NET_ADMIN` (the installer's systemd unit grants it, with `AF_NETLINK`). Without the module: `unavailable` ("no nf_conntrack"). Without the capability, or after netlink overflow or event loss: `degraded` with the reason, held for at least 5 minutes after the last loss. If you run the agent by hand rather than from the unit, grant the capability yourself. Measured agent memory with the listener live was about 17 MiB peak ([benchmarks-m7.md](benchmarks-m7.md)).

## Image updates: the 48 hour wait and auto_pull

Per container the server decides: **up to date**, **waiting 48 h** (candidate image younger than 48 h; the age counts from the later of the image's own creation time and when the server first saw that digest, because publishers can backdate), **eligible**, **eligible because it fixes a critical CVE** (younger than 48 h but a scan of the candidate shows a critical CVE of the running image fixed), or **unknown** (private or unauthenticated registry, locally built image, or no creation time; never pulled automatically).

On the tab, **Image updates**, an admin can tick **Automatically pull eligible image updates (never restarts containers; waits 48 hours unless a verified critical CVE fix)**. This is the organisation setting `auto_pull`, off by default. When on, the agent pulls the new image and re-checks digest and age itself; it never restarts or redeploys. Redeploy to use the new image. Registry lookups from the agent refuse loopback, private and metadata addresses, except the image's own self-hosted registry host.

## Known limits

- **Large images can fail with `inventory out of memory`.** The helper runs in a cgroup capped at **256 MiB** (`HAETAE_INVENTORY_MEM`). In [benchmarks-m7.md](benchmarks-m7.md), `alpine:3.20` peaked at about 179.5 MiB, but `node:22-bookworm` (about 1.1 GB) and `python:3.12` hit the cap and were killed; unconstrained `node:22-bookworm` needed about 456 MiB. On a 1 GiB VPS expect multi-GB app images to end as scan failed. Raise the cap in an agent unit drop-in, for example `Environment=HAETAE_INVENTORY_MEM=640M` (accepts `k`, `m`, `g` suffixes), and also raise `MemoryMax=400M` in the unit, which covers agent plus helper. `HAETAE_INVENTORY_TIMEOUT` (a duration, default `10m`) bounds one scan.
- If the cgroup cannot be used on the host, the helper falls back to a Go memory limit and an address-space limit and the scan report notes `limited=false`; it never silently skips a scan.
- A package list above 20,000 packages fails with "too many packages".
- Grype and Trivy agree closely on Debian-based and Alpine images after a source-package fix (Jaccard 0.97 to 1.0 on debian:12-slim, node and alpine), less on Ubuntu (0.35, Grype reports more, mostly unfixed), and severity mapping differs. The tool is one scanner, not a guarantee of completeness.
- Only running containers are scanned, not images on disk. Misconfiguration checks do not include network exposure beyond published ports.
