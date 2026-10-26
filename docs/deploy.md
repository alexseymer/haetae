# Deploying apps

Haetae deploys a Docker Compose app to one of your enrolled hosts, either from a **Git repository** or from a **pasted compose file**. The build and `docker compose up` run on the host; the server never sees your source code. Only administrators can deploy, and a deploy runs with whatever privileges the compose file asks for (a compose file can mount `/` or run privileged), which is root-equivalent on that host. The UI says so.

## Create a project

Hosts, open the host, **Deploy** tab, **New project**.

| Field | Meaning |
|---|---|
| **Name** | Lowercase letters, digits, dashes, at most 40, starting with a letter or digit; unique per host. Also the Compose project name. |
| **Source** | **Git repository** or **Pasted compose file** |
| **Repository URL** | Plain `https://host/path`. No credentials in the URL, no `ssh://`, no `git@`. |
| **Ref** | Branch name or a 40-character commit id |
| **Compose path** | Path inside the repository, ending `.yml` or `.yaml`; empty means `compose.yaml` |
| **Access token** | For private repositories. Stored encrypted on the server (AES-256-GCM, key `secret.key` in the data directory), never returned by the API or logged, and handed to `git` through the process environment. Edit the project to replace it or tick **Remove token**. |
| **Auto-deploy on push** | Git projects only, see below |
| **Compose file** | For pasted compose, up to 256 KiB of UTF-8 text |
| **Domain**, **Service**, **Port** | Optional ingress; all three are required together |

Then **Create project**. On the project page press **Deploy now**. **Edit** changes the fields, **Remove** stops the containers and keeps volumes and data.

A deploy goes through these statuses, shown as a stepper: Queued, Fetching, Building, Starting, Checking, then Healthy or Failed (and Rolling back, Rolled back, Rollback failed). The build log streams on the project page and on the deploy page. Only one deploy per project can run at a time.

## What happens on the host

1. **Fetching:** shallow clone of the ref (`git fetch --depth 1`, checkout, clean).
2. **Building:** `docker compose build --pull`. Budget 15 minutes.
3. **Starting:** `docker compose up -d --remove-orphans --wait`, budget 180 s. `--remove-orphans` removes containers of services you deleted from the compose file; volumes are never touched.
4. **Checking:** all services must be running and, where a healthcheck exists, healthy through a 10 s window.
5. With a domain, the route is added to the managed Caddy and the result is `https://<domain>`. Without a domain the result lists published ports.

Timing: the spec targets a push deployed within 30 s and a result within 60 s, and the roadmap notes that 30 s is reachable only with **cached builds**; a cold build is slower and can take up to the 15 minute build budget. Measured numbers: [acceptance.md](acceptance.md). They were taken on a simulated host, not on a real fleet.

## Failure and rollback

- **A build failure never touches the running version.** Replacement only starts after the build succeeded.
- **A failed start rolls back automatically** to the last healthy version (rebuilt from its commit or stored compose; the layer cache makes this quick). The deploy shows **Rolled back** with the new version's error and a rollback note. The UI text: "The new version failed to start. The previous version was restored." If the rollback also fails, the status is **Rollback failed**.
- There is a short gap while containers are replaced. This is not a zero-downtime deploy, because blue-green over shared volumes is unsafe for stateful apps.
- **Manual rollback:** on the project page, Deploy history, **Rollback** next to an earlier healthy deploy, then confirm ("Roll back to commit ..."). It creates a new deploy with trigger **Rollback**.
- **Volumes survive** rollbacks, failed deploys and project removal. Haetae never runs `compose down -v`. It never deletes data.
- The deploy error shown is the raw error (last lines of build output or the failing step).

## Push to deploy

Tick **Auto-deploy on push** (or the **Auto-deploy** checkbox on the project page) for a Git project. The agent runs `git ls-remote` on the ref every 10 seconds and reports a changed commit; the server then starts a deploy with trigger **Push**. There is no webhook and no inbound port. Latency is therefore up to 10 s plus the build and start time. Use a branch name as ref; a pinned commit id never changes.

## HTTPS ingress, domains

The agent runs one Caddy container named `haetae-caddy` per host and routes each project's domain to its service (`<container>:<port>`). Requirements:

- the domain's DNS points to the host;
- ports 80 and 443 on the host are free and reachable (they are the defaults; change with the agent flags `--ingress-http-port` and `--ingress-https-port`);
- the domain is a plain lower-case host name (no wildcard, port, IP address or scheme).

Caddy obtains certificates automatically for real domains. Certificate issuance from a public CA was not tested in this project (tests use Caddy's internal CA), so first-time issuance problems on a real domain are the likeliest surprise; see [troubleshooting.md](troubleshooting.md). Certificates close to expiry appear as misconfigurations on the Security tab ([security.md](security.md)).

## Secrets

- Haetae keeps one secret for you: the Git access token (above).
- It has **no store for application secrets** (database passwords and so on). Do not paste them into a pasted compose file expecting protection; the file is stored on the server as text. Put them in files or environment on the host, or in a private repository that your compose file reads.
- Build output passes through redaction **on the host** before it leaves (secrets and personal data patterns, regular expressions with a time budget). If a chunk cannot be redacted, it is replaced by a withheld marker instead of being sent. So a secret printed during a build should not reach the server. Redaction is pattern based; an unusual secret format can slip through, so do not print secrets in builds.

## Limits

- Git over `https://` only. No SSH, no Nixpacks or buildpacks, no preview deployments.
- Compose file path and ref are validated; unusual names are refused with a message.
- Removing a project while its host is offline needs **Force delete records (host is offline)**.
