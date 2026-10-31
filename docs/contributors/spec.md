# Haetae (working name): One-Page Spec

Oct 23, 2026 · @alex

Haetae is an AI operator that watches your Docker servers and reports back in chat, inside one tool that also deploys apps and includes the metrics, alerts and security scanning small teams otherwise run as a separate stack. Free self-hosted (AGPL-3.0) and hosted. Details for every point are in the full spec (appendix).

## Who the user is

A solo developer or a team of 2 to 10 running 2 to 20 Linux servers with Docker Compose, without Kubernetes or an ops person. They use a deploy tool plus a separate monitoring stack today, and want to work on their own product, not on running Docker.

## Exact input

1. **Enroll a server:** run one command with a single-use token.
2. **Deploy:** a Git repo and branch, or a pasted `compose.yaml`, plus a target host and an optional domain.
3. **Talk to the operator:** choose a chat channel (Telegram, Signal, Slack, email or web) and an autonomy level (report only by default), then ask questions in plain language.

```text
$ curl -fsSL <install-url> | sh -s -- --token T-8f2a        # enroll hel1-prod
Deploy: github.com/acme/shop  branch main  domain shop.example.com
You: Haetae, why was shop slow at lunchtime?
```

## Exact output

- **Servers and stacks:** status, versions, ports, restarts, and CPU, memory, disk and network metrics, all on by default.
- **Deploy result** within 60 seconds: the HTTPS address, or the failing step with the raw error; build log and one-click rollback.
- **Operator messages** in chat: incident reports, a daily briefing, security reports and answers, each with linked evidence.
- **Security findings** per running container: software with versions, CVE, severity and fixed version, plus misconfigurations (exposed ports, mounted Docker socket, root user, expiring certificates).

```text
08:00 briefing, 3 hosts, 24 containers, all running. 1 thing needs you:
  api on hel1-prod: openssl 3.0.8, critical CVE, fixed in 3.0.13.   [Update and redeploy] [Later] [Mute 30 days]
Haetae: shop was slow 12:10 to 12:40. The database sat at 94% of its memory limit after deploy 3f9c2a1.   [logs] [chart]
```

## Done correctly

1. A server shows online within 30 seconds of enrollment with no inbound port, and its metrics appear within 2 minutes.
2. A push to the branch deploys within 30 seconds to an HTTPS address; a failed deploy leaves the previous version running.
3. A high-severity event reaches the user's chat within 60 seconds; a quiet day produces one short briefing.
4. Every operator statement links to evidence; when the data shows no cause, it says "cause not found".
5. Warnings, errors and fatal lines always reach the AI; identical lines are merged with a count, and routine info and debug patterns, once judged green, are skipped (target: 95% of lines never sent).
6. With a non-local AI, personal data and secrets are removed on the host first; if that fails, nothing is sent.
7. The operator acts only at the autonomy level set, never deletes data, treats logs as untrusted text, and accepts chat approvals only from verified accounts.
8. The software in every running container is inventoried on deploy and start and rescanned daily, and its outbound connections are watched; a failed scan shows "scan failed", never "no vulnerabilities"; a new advisory affecting a running container alerts within 15 minutes (target), and freshly published image versions wait 48 hours before auto-pulling unless they carry a verified critical fix.
9. If the AI is unreachable or capped, rule-based alerts for crashes, metrics and new CVEs keep working.
10. The agent on each server uses under 50 MB RAM and reconnects by itself after a reboot or lost connection.
11. Self-hosted: a working login within 5 minutes of one command, every feature included, no data sent to us. Hosted: raw logs stay on the customer's server.

## Out of scope for 0.8.0

Kubernetes, Swarm, Nixpacks and buildpacks, preview deployments, running backups (backup checks only), SAML and SCIM, mobile apps. Next after 0.8.0: a local decision model (Laya) as a cheap first stage for log labeling and privacy filtering, then SAML and SCIM and optional backups.

## Business model

Self-hosted is free and unlimited under AGPL-3.0. Revenue comes from the hosted service (starting point to test: $15 for 3 hosts, AI included up to a cap) and paid support. All timing and resource figures are targets to validate. The name Haetae still needs trademark clearance (software classes 9 and 42) and a domain.
