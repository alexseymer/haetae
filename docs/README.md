# Documentation index

## Reading order

**Using Haetae:** [install](install.md), then [enroll](enroll.md) a host, [deploy](deploy.md) an app, connect [chat](chat.md), optionally enable the [AI operator](ai-operator.md), and read [security](security.md). Keep [troubleshooting](troubleshooting.md) at hand.

**Contributing:** [../CONTRIBUTING.md](../CONTRIBUTING.md), [spec](spec.md), [architecture](architecture.md), the [stack decision](adr/0001-stack.md), the [roadmap](roadmap.md), then the [design notes](design/) for the area you touch. Check [acceptance](acceptance.md) for what is verified and how.

## Users

| Document | What is in it |
|---|---|
| [install.md](install.md) | Server install, first login, configuration, ports, TLS, reverse proxy, backups, upgrade, uninstall, trust model |
| [enroll.md](enroll.md) | Enrolling hosts, what the installer and systemd unit do, agent flags, reconnect, removing a host |
| [deploy.md](deploy.md) | Projects, Git and pasted compose, push to deploy, HTTPS ingress, rollback, secrets, limits |
| [chat.md](chat.md) | Channels (web, Telegram, Slack, email, Signal), linking, briefing, alerts, mutes |
| [ai-operator.md](ai-operator.md) | Providers, what it does and does not do, redaction, autonomy, approvals, limits |
| [security.md](security.md) | Scanning, CVE matching, misconfigurations, outbound watch, image updates, known limits |
| [security-ui.md](security-ui.md) | The Security tab and its API |
| [troubleshooting.md](troubleshooting.md) | Symptom to check lists |
| [self-hosting.md](self-hosting.md) | Pointer to install.md (kept for old links) |
| [../examples/hello/](../examples/hello/README.md) | A tiny app to try a deploy |

## Contributors

| Document | What is in it |
|---|---|
| [spec.md](spec.md) | One-page spec: scope, "Done correctly" criteria S1 to S11, out of scope |
| [roadmap.md](roadmap.md) | Steps M0 to M9 and their acceptance criteria |
| [architecture.md](architecture.md) | Components, packages, data flows, trust model |
| [adr/0001-stack.md](adr/0001-stack.md) | Technology stack decision |
| [design/m5-deploys.md](design/m5-deploys.md) | Deploy design |
| [design/m6-alerts-chat.md](design/m6-alerts-chat.md) | Alerts, incidents, chat, briefing design |
| [design/m7-security.md](design/m7-security.md) | Security scanning design |
| [design/m8-operator.md](design/m8-operator.md) | AI operator design |
| [acceptance.md](acceptance.md) | Acceptance report: each criterion, its test, measured value, caveats |
| [benchmarks-m7.md](benchmarks-m7.md) | Scanner memory and Grype versus Trivy comparison |
| [release.md](release.md) | Building release artifacts and the server image |
