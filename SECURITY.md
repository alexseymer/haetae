# Security policy

## Reporting a vulnerability

Please report vulnerabilities **privately** through GitHub security advisories:

1. Go to the repository's **Security** tab.
2. Choose **Report a vulnerability** (this opens a private advisory visible only to you and the maintainers).
3. Describe the problem, the affected version or commit, and steps to reproduce. Please do not include real secrets or other people's data.

Do not open a public issue or pull request for a vulnerability.

## Scope

In scope: the Haetae server, agent and inventory helper, the installers (`deploy/install-server.sh`, the agent `install.sh`), the web UI and API, and the docs where they would lead users to an unsafe setup. Areas where reports are especially valuable: agent identity and mutual TLS, tenant isolation, redaction of secrets and personal data before data leaves a host, autonomy enforcement, and handling of untrusted log text.

Out of scope: vulnerabilities in third-party software that Haetae only reports on or runs (Docker, Caddy, PostgreSQL, Syft, Grype) which should go to those projects; problems that need an already-compromised administrator or host; denial of service by volume alone; and the documented limits in [docs/security.md](docs/security.md) and [docs/contributors/acceptance.md](docs/contributors/acceptance.md).

## What to expect

Haetae is maintained by a small team without a formal service-level agreement. We aim to acknowledge a report, tell you whether we consider it valid, and keep you informed until a fix is released and the advisory is published. We will credit you if you wish. Please give us reasonable time to fix before disclosing.

## Supported versions

v1 is pre-release and no version has been published yet; fixes go to the `main` branch.
