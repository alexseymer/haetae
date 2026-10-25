# M5 design: deploys

Spec S2: a push to the branch deploys to an HTTPS address within 30 s; a failed
deploy leaves the previous version running; the result (address, or the
failing step with the raw error) is reported within 60 s; build log and
one-click rollback.

## Where things run

- **On the host (agent):** clone, build, `docker compose up`, health check,
  rollback, Caddy ingress. Source code and build output never reach the
  server unredacted; the server never sees the code at all.
- **On the server:** projects, deploy history, redacted logs, the API and UI,
  and the decision to deploy (human click, API call, or branch update).

## Concepts

- **Project:** name (`^[a-z0-9][a-z0-9-]{0,39}$`, unique per host), host,
  source (Git repo + ref + compose path, or inline compose YAML), optional
  ingress (domain + service + port), auto-deploy flag.
- **Deploy:** one attempt to make a project run a given version. Statuses
  (phases): queued, fetching, building, starting, checking, healthy | failed,
  then rolling_back, rolled_back | rollback_failed.
- **Rollback:** a deploy of an earlier healthy version (new deploy record with
  `rollback_of`). Automatic after a failed start; manual from the UI.

## Safety rules (hard)

1. **Build failures never touch the running version.** Build first
   (`docker compose build`, `pull`), only then replace containers.
2. **Runtime failures roll back automatically** to the last healthy version
   (rebuilt from its commit or stored compose; layer cache makes this fast).
   There is a short gap while containers are replaced; true zero downtime
   would need blue-green over shared volumes, which is unsafe for stateful
   apps. This is a documented limit.
3. **Volumes are never removed.** `compose down` never gets `-v`; removing a
   project stops it and keeps its data. A test proves rollback and removal
   leave volumes in place.
4. **Deploy rights are root-equivalent on the host** (a compose file can mount
   `/` or run privileged). Only administrators may deploy; the UI says so.
5. **No shell, strict inputs.** The agent runs `git` and `docker` with argument
   lists. Names, refs, domains, URLs and paths are validated by allowlist
   regexes before use. Git URLs must be `https://` (tests may allow `file://`
   through `HAETAE_AGENT_ALLOW_LOCAL_GIT=1`). Project files live under the
   agent's state directory only.
6. **Everything that leaves the host is redacted, fail-closed.** Build output
   goes through `internal/redact` (with the PEM guard) in chunks; a chunk that
   cannot be redacted is replaced by a "withheld" marker, never sent.
7. **Git tokens are encrypted at rest** on the server (AES-256-GCM, key in the
   data directory, mode 0600) and never returned by the API or written to logs;
   the agent passes them to git through a credential helper environment, not
   the command line.

## Ingress

The agent keeps one Caddy container (`haetae-caddy`, image pinned by digest in
the agent) on a Docker network it shares with each project that has a domain.
Routes are applied through Caddy's admin API from a state file, so a restart
restores them. Real domains get Let's Encrypt automatically; tests use
`tls internal`. Listening ports default to 80/443 and are configurable.

## Push to deploy

The agent polls `git ls-remote` every 10 s for projects the server marked
auto-deploy (sent in a WatchSet) and reports `BranchUpdate`; the server starts
the deploy. No inbound port, no webhook.

## Measurements to record (acceptance)

- push to healthy, cached build (target <= 30 s), and cold build separately;
- result reported (healthy or failed with error) within 60 s of the start;
- a broken build leaves the old version serving; a crashing start rolls back.
