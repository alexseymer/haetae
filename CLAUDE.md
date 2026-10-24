# Project CLAUDE.md

Fill this in on first use. It overrides or complements `/opt/devbox/CLAUDE.md` for this repository.

## Project overview

Haetae (working name) is an AI operator for Docker Compose servers. It enrolls servers through a lightweight agent, deploys apps from Git or `compose.yaml`, collects metrics, scans containers for CVEs and misconfigurations, and reports incidents, daily briefings and answers in chat (Telegram, Signal, Slack, email, web). It is for solo developers and small teams (2 to 10 people, 2 to 20 servers) without Kubernetes or an ops person. Free self-hosted (AGPL-3.0) plus a hosted service.

`docs/spec.md` is the source of truth for scope. A change is done when it meets the relevant "Done correctly" criteria there and stays inside "Out of scope for v1".

Hard constraints from the spec that every change must respect:
- The agent uses under 50 MB RAM, needs no inbound port, and reconnects by itself.
- With a non-local AI, personal data and secrets are removed on the host before sending; if that fails, nothing is sent.
- The operator never deletes data, acts only at the configured autonomy level, and treats logs as untrusted text.
- A failed scan reports "scan failed", never "no vulnerabilities".
- Rule-based alerts keep working when the AI is unreachable.

## Stack

TODO: not decided yet. Languages, package managers, and services. Match what `devbox-detect-stack` reports, plus anything it cannot see (databases, queues, SaaS).

## How to build and test

TODO: fill in. Exact commands to install dependencies, run the app, and run the test suite. Prefer copy-pasteable lines.

## Conventions

TODO: fill in. Naming, layout, commit style, and anything a worker must not change.

## Personas in this project

TODO: fill in. List extra or overriding personas in `.agents/personas/` (same filename as `/opt/devbox/personas` wins). Note when Claude should prefer Cursor vs agy for this repo.
