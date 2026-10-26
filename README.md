# Haetae

> Working name, pending trademark clearance.

Haetae is an AI operator that watches your Docker servers and reports back in chat. It is one tool that also deploys apps and includes the metrics, alerts and security scanning that small teams otherwise run as a separate stack. A small agent on each Linux host connects outbound to a control plane you run with Docker Compose; you deploy from a Git repository or a `compose.yaml`, get alerts, a daily briefing and answers in web chat, Telegram, Slack, email or Signal, and see CVEs and misconfigurations per running container.

## Who it is for

Solo developers and teams of 2 to 10 who run 2 to 20 Linux servers with Docker Compose, without Kubernetes or a dedicated ops person. Kubernetes, Swarm, buildpacks, preview deployments, running backups, SAML/SCIM and mobile apps are out of scope for v1 (see [the spec](docs/spec.md)).

## Quick start (about 5 minutes)

You need one Linux host with Docker (Compose v2) for the server, and one or more Linux hosts with Docker to manage (they can be the same machine for a trial).

**1. Install the server** (on the server host, as a user that can run Docker):

```sh
curl -fsSL https://raw.githubusercontent.com/alexseymer/haetae/main/deploy/install-server.sh | sh
```

It prints a URL such as `https://203.0.113.10:8080` and a one-time setup code. Details and overrides: [docs/install.md](docs/install.md).

**2. First login.** Open the URL (your browser warns about the certificate, which comes from the server's own CA until you supply one). Enter the setup code, then the organisation name, email and password of the first administrator.

**3. Add a host.** In the UI choose **Hosts**, **Add host**, enter a host name and validity (hours), and **Create token**. Copy the shown command and run it on the host to manage, as root. The host appears as **Online** on the Hosts page. Details: [docs/enroll.md](docs/enroll.md).

**4. Deploy an app.** Open the host, **Deploy** tab, **New project**. Choose **Git repository** (URL, ref, compose path) or **Pasted compose file**, optionally a domain, service and port for HTTPS, and **Create project**. On the project page press **Deploy now**. The result is the HTTPS address, or the failing step with the raw error. Details: [docs/deploy.md](docs/deploy.md).

## What you get

Tied to the "Exact output" section of the [spec](docs/spec.md):

- **Servers and stacks:** status, versions, ports, restarts, and CPU, memory, disk and network metrics on by default (Hosts, host Overview and Containers tabs).
- **Deploys:** from Git or a pasted `compose.yaml`, build log, HTTPS ingress through a managed Caddy container, one-click rollback, a failed deploy keeps the previous version running, optional push-to-deploy by branch polling. [docs/deploy.md](docs/deploy.md)
- **Operator messages in chat:** rule-based incident alerts, a daily briefing, and AI answers with linked evidence when you enable the AI operator. [docs/chat.md](docs/chat.md), [docs/ai-operator.md](docs/ai-operator.md)
- **Security findings per running container:** installed software, CVE, severity and fixed version, misconfigurations (published ports, Docker socket, root user, privileged mode, expiring certificates), outbound connections, image update policy. A failed scan shows "scan failed", never "no vulnerabilities". [docs/security.md](docs/security.md)

## Documentation

| Document | Contents |
|---|---|
| [docs/install.md](docs/install.md) | Server install, ports, TLS, reverse proxy, backups, upgrade, uninstall |
| [docs/enroll.md](docs/enroll.md) | Enrolling hosts, what the agent installs, flags, removing a host |
| [docs/deploy.md](docs/deploy.md) | Deploys, push-to-deploy, domains and HTTPS, rollback |
| [docs/chat.md](docs/chat.md) | Web, Telegram, Slack, email and Signal, linking, briefing, alerts, mutes |
| [docs/ai-operator.md](docs/ai-operator.md) | AI operator: providers, redaction, autonomy, approvals, limits |
| [docs/security.md](docs/security.md) | Scanning, CVE matching, outbound watch, image updates, known limits |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Symptom to check lists |
| [docs/spec.md](docs/spec.md), [docs/roadmap.md](docs/roadmap.md), [docs/adr/0001-stack.md](docs/adr/0001-stack.md) | Scope, build plan, stack decision |
| [docs/acceptance.md](docs/acceptance.md) | Acceptance report: every spec criterion with its test and measured value |
| [docs/benchmarks-m7.md](docs/benchmarks-m7.md) | Scanner memory and Grype/Trivy comparison |

## Status

All roadmap steps (M0 to M9) are built. What was measured, how, and what was verified only against fakes (live Telegram, Slack, Signal and SMTP servers, a real AI provider, publicly trusted certificates) is recorded in [docs/acceptance.md](docs/acceptance.md). All timing and resource figures in the spec are targets validated on a simulated setup (Docker-in-Docker on one small machine), not on a fleet of real servers.

## License

Haetae is free software, licensed under the [GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0-only). Every feature is included in the self-hosted version.
