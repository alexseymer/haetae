# Haetae

**An AI operator for Docker Compose servers: deploy, monitor, scan and get answers in chat, from one self-hosted tool.**

[![CI](https://github.com/alexseymer/haetae/actions/workflows/ci.yml/badge.svg)](https://github.com/alexseymer/haetae/actions/workflows/ci.yml)
[![Docs](https://img.shields.io/badge/docs-alexseymer.github.io%2Fhaetae-1f6f4a)](https://alexseymer.github.io/haetae/)
[![License: AGPL-3.0](https://img.shields.io/github/license/alexseymer/haetae)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/alexseymer/haetae)](go.mod)

> **Vibe-coded.** This project was written with heavy AI assistance and is not yet fully tested. Try it on a test server first, review it before you rely on it, and do not use it as your only monitoring.
>
> Haetae is a working name, pending trademark clearance. Status: version 0.8.0 is built and verified in a single-VM environment; see [what you can and cannot trust](#what-you-can-and-cannot-trust).

## What is it

Haetae is for solo developers and small teams (2 to 10 people, 2 to 20 Linux servers) who run Docker Compose without Kubernetes or an ops person, and who today glue together a deploy tool, a monitoring stack and a scanner.

- **The problem:** running servers takes more attention than your product deserves.
- **The promise:** a small agent on each server, one control plane you run with Docker Compose, and a chat that tells you what happened and why, with a link to the evidence behind every statement. If the data shows no cause, it says "cause not found".
- **Free and self-hosted** under AGPL-3.0, every feature included, no data sent to us. A hosted service is planned, not built.

<!-- screenshots:start -->
<p align="center">
  <img src="docs/images/host-overview.png" alt="Host overview: CPU, load, memory, disk and network charts for one server" width="860">
</p>

<table>
  <tr>
    <td width="50%"><img src="docs/images/hosts.png" alt="Hosts list with status, security badge and container count"><br><sub>Every server at a glance: status, security badge, containers.</sub></td>
    <td width="50%"><img src="docs/images/incident.png" alt="An incident with the alerts and the evidence behind each one"><br><sub>An incident groups related alerts, each with its evidence.</sub></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/images/chat.png" alt="Web chat: questions answered by the operator with links to the evidence"><br><sub>Ask in plain language; every statement links to its evidence.</sub></td>
    <td width="50%"><img src="docs/images/security.png" alt="Security tab listing vulnerabilities with fixed versions and risky container settings"><br><sub>Vulnerabilities with the fixed version, and risky settings.</sub></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/images/ai-operator.png" alt="Settings, AI operator: enabled at the suggest level with usage today"><br><sub>The AI operator is optional: autonomy level and daily token cap.</sub></td>
    <td width="50%"><img src="docs/images/add-host.png" alt="Add host dialog showing the one-time install command"><br><sub>Add a host with one command; the token works once.</sub></td>
  </tr>
</table>
<!-- screenshots:end -->

## How it works

```mermaid
flowchart LR
    user["You: browser or chat<br/>(web, Telegram, Slack, email, Signal)"]
    subgraph cp["Control plane (Docker Compose)"]
        server["haetae-server<br/>web UI + API, agent gateway,<br/>alert rules, AI operator"]
        db[("PostgreSQL")]
        server --- db
    end
    ai["AI provider<br/>(optional)"]
    subgraph host["Each Linux host"]
        agent["haetae-agent<br/>metrics, logs, deploys,<br/>redaction, scan"]
        docker["Docker + Compose"]
        caddy["Caddy ingress<br/>(HTTPS)"]
        agent --> docker
        agent --> caddy
    end
    user <--> server
    agent == "outbound only, mutual TLS" ==> server
    server -.->|"redacted data only"| ai
```

1. You run the **server** (plus PostgreSQL) with one installer command. It has its own certificate authority.
2. You enroll a **host** with a single-use token. The agent connects **outbound** to the server's gateway port with mutual TLS; it opens no listening port. It reconnects by itself.
3. The agent reads Docker state, metrics and logs, filters and redacts them **on the host**, and sends them over that connection. Commands (deploy, scan) come back the same way.
4. The server stores data in PostgreSQL, evaluates alert rules without any AI, and delivers messages to your chat channels.
5. The **AI operator** is optional. It answers questions from your own data, cites evidence, and only proposes or performs a restart if you allow it. Non-local providers only ever see redacted data.
6. Apps you deploy get HTTPS through a **Caddy** container the agent manages on that host.

The ideas in plain words: [Concepts](docs/concepts/index.md). For contributors: [docs/contributors/architecture.md](docs/contributors/architecture.md).

## Quick start

There is **no published container image yet**, so you build it from this repository.

**Prerequisites:** a Linux host with Docker and Compose v2 (`docker compose version`), `git`, and `curl`; free TCP ports 8080 and 8443; root or `sudo` (the installer writes `/opt/haetae`). Hosts you manage need Docker and must reach both ports. One machine can be both for a trial.

**1. Build the server image and install** (clone anywhere except `/opt/haetae`):

```sh
git clone https://github.com/alexseymer/haetae.git haetae-src
cd haetae-src
docker build -t haetae-server:local .
sudo HAETAE_IMAGE=haetae-server:local HAETAE_NO_PULL=1 sh deploy/install-server.sh
```

The build includes the web UI and takes several minutes the first time. The installer prints a URL such as `https://203.0.113.10:8080` and a one-time setup code. If your managed hosts cannot reach that address, set `HAETAE_HOST=<name or IP they can reach>` as well. Options: [Install the server](docs/getting-started/install-the-server.md).

**2. First login.** Open the URL (the browser warns about the certificate, which comes from the server's own CA). Enter the setup code, then the organisation name, email and password of the first administrator.

**3. Add a host.** **Hosts**, **Add host**, enter a name and validity, **Create token**. Run the shown command on the host as root. The host appears as **Online**, normally within seconds. Details: [Enroll a host](docs/getting-started/enroll-a-host.md).

**4. Deploy the example.** Open the host, **Deploy** tab, **New project**. Name `hello`, source **Pasted compose file**, paste [examples/hello/compose.yaml](examples/hello/compose.yaml), leave the domain empty, **Create project**, then **Deploy now**. When it is **Healthy**, open `http://<host address>:8088/`. Steps, Git source and HTTPS: [examples/hello/README.md](examples/hello/README.md), [Deploy an app](docs/getting-started/deploy-an-app.md).

**5. Optional:** connect a chat channel ([Connect a chat channel](docs/getting-started/connect-chat.md)) and the AI operator ([Turn on the AI operator](docs/getting-started/turn-on-the-operator.md)). A published image (`ghcr.io/alexseymer/haetae-server`, the installer's default) is planned; until then keep the `HAETAE_IMAGE` and `HAETAE_NO_PULL` settings above.

## What you get

| You get | In plain words | Docs |
|---|---|---|
| **Servers and stacks** | Status, versions, ports, restarts, and CPU, memory, disk and network metrics, on by default. | [enroll a host](docs/getting-started/enroll-a-host.md) |
| **Deploys** | From a Git repo or a pasted `compose.yaml`; build log, HTTPS via managed Caddy, one-click rollback; a failed deploy keeps the previous version running. | [deploy from Git](docs/guides/deploy-from-git.md) |
| **Push to deploy** | By polling the branch every 10 s from the agent; no webhook, no inbound port. | [deploy from Git](docs/guides/deploy-from-git.md) |
| **Alerts and briefing** | Rule-based incident alerts and one short daily briefing, working even when the AI is down. | [alerts and incidents](docs/guides/alerts-and-incidents.md) |
| **Answers in chat** | Plain-language questions, each statement linked to evidence; "cause not found" otherwise. Needs a provider you configure. | [asking the operator](docs/guides/asking-the-operator.md) |
| **Security per container** | Installed software, CVE with severity and fixed version, misconfigurations, outbound connections, image update policy. A failed scan says "scan failed", never "no vulnerabilities". | [watching vulnerabilities](docs/guides/watching-vulnerabilities.md) |
| **Safe by design** | Secrets and personal data are redacted on the host before a non-local AI sees anything; if redaction fails, nothing is sent. The operator never deletes data. | [asking the operator](docs/guides/asking-the-operator.md) |

Out of scope for 0.8.0: Kubernetes, Swarm, buildpacks, preview deployments, running backups, SAML/SCIM, mobile apps ([spec](docs/contributors/spec.md)).

## What you can and cannot trust

Everything below comes from the [acceptance report](docs/contributors/acceptance.md) (a plain-language summary is in [What is tested and what is not](docs/tested.md)), run on one VM (2 cores, 8 GB) with `scripts/acceptance.sh`.

| Measured (loopback, hosts as containers on one machine) | Value |
|---|---|
| Enrollment to online | 116 ms |
| Host metrics visible | 10.1 s |
| Push to healthy, cached build | 6.1 s |
| Crash loop alert in web chat after the alert fired | 6 ms |
| Agent RSS peak, 20 noisy containers | 29.1 MiB (limit 50 MiB) |
| Redaction recall on the labelled corpus | secrets 1.000, personal data 1.000 |
| Install to login (image already built) | 31.4 s |

**Verified only against fakes or not at all:**

- **No run with a live AI provider.** Answer quality with a real model is unmeasured; the provider was scripted, and the wire formats were tested against test servers.
- Telegram ran against a fake Bot API; Slack, Signal and email are covered by unit tests only.
- Public certificate issuance (Let's Encrypt) was not run.
- Timings are loopback timings, not WAN or a real fleet. Only amd64 binaries were executed.
- No published image, no multi-arch image, no signing. The hosted service is not built.
- Agent client certificates last one year and cannot yet be renewed.

## Why is it called Haetae?

Haetae (also written Haechi) is a lion-like guardian creature from Korean folklore. It was believed to keep away fire and disaster and to tell right from wrong; stone figures of it stand at Gwanghwamun Gate in Seoul. The name fits: Haetae watches your servers and only says what it can back up with evidence, and when the data shows no cause it says so.

## Documentation

**Read the documentation on the site: <https://alexseymer.github.io/haetae/>**. It is written for users: [getting started](docs/getting-started/index.md), [guides](docs/guides/index.md), [concepts in plain words](docs/concepts/index.md), [problems and what to do](docs/problems/index.md) and [what is tested and what is not](docs/tested.md).

The same pages live in this repository under [`docs/`](docs/), so you can also read them here. Material for contributors (specification, roadmap, architecture, decisions, design notes, acceptance report) is in [`docs/contributors/`](docs/contributors/README.md). To run the site locally: `cd docs && npm ci && npm run dev`.

## Status and roadmap

All roadmap steps (M0 to M9) are built. Next:

- A run against a **live AI provider** and real chat services.
- A **published container image** (and release signing).
- **Agent certificate renewal** (see known limits in [docs/contributors/acceptance.md](docs/contributors/acceptance.md)).
- A **hosted service**.
- After 0.8.0: a local decision model for log labeling and privacy filtering, then SAML/SCIM and optional backups ([spec](docs/contributors/spec.md)).

## Security

Please report vulnerabilities privately, not in public issues. See [SECURITY.md](SECURITY.md).

## Contributing

Issues and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first, and follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

[GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0-only). Every feature is included in the self-hosted version.
