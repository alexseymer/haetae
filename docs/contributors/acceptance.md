# Acceptance report

Every "Done correctly" criterion of `docs/contributors/spec.md` (S1 to S11) with the test that checks it, the value measured, and the result. Run on one VM (2 cores, 8 GB, Docker 29, kernel 7.0) on 2026-10-26 with `scripts/acceptance.sh`, which runs every suite in sequence and writes `acceptance-results/` (logs, `summary.txt`, `measures.txt`; not committed). Hosts are Docker containers on the same machine, so all timings are loopback timings: they show the system is not the bottleneck, not what a WAN link will do.

Run result: 16 steps; 14 passed in the full run. Two steps failed and were fixed, then passed on re-run:

- `operator-e2e` failed once on a test race (the harness accepted the previous answer, created less than a second earlier, as the answer to the next question; the product behaved correctly). The harness now ignores messages that existed before the question; the suite then passed (90.6 s).
- `release-verify` and `release-rss` failed because the runner skipped `make release-image` and because the RSS harness pulled a cached image from Docker Hub (rate limited, HTTP 429). Both fixed; `make release release-image test-release bench-release-rss` then passed on a clean tree (version `0.1.0-352c129`).

Final state: build, vet with every tag, lint (0 issues), govulncheck (no called vulnerabilities), web typecheck and unit tests, unit and integration tests with `-race`, load, self-host, web e2e, deploy e2e, alerts e2e, security e2e, operator e2e and release checks all pass.

## Criteria

| # | Criterion (short) | Test | Measured | Result |
|---|---|---|---|---|
| S1 | Online within 30 s of enrollment, no inbound port; metrics within 2 min | `test/integration` enroll/connect (`S1 enrollment-to-online`), metrics (`S1 enrollment-to-host-metrics`), the agent process opens no listening socket (`test/integration/connection_test.go`) | online **116 ms**; host metrics **10.1 s**, container metrics **20.1 s** (limit 2 min) | pass |
| S2 | Push deploys to HTTPS within 30 s; a failed deploy leaves the previous version | `test/deploy` (`push-to-healthy cached`, `bad-build-to-failed`, `crash-to-rolled_back`, `inline-to-healthy`) | push to healthy, cached build **6.1 s** (cold, first deploy **3.3 s** for the sample app); inline deploy **3.9 s**; bad build reported **0.7 s**, previous version keeps serving; crash rolled back after **14.1 s** | pass, narrowed (see below) |
| S3 | High-severity event in chat within 60 s; a quiet day gives one short briefing | `test/alerts` (`crash_loop`, `host_offline`, `quiet-day-briefing`); `internal/chat` `TestIncidentToTelegramWithin60s` | crash loop: web **6 ms** after the alert fired (alert **9.6 s** after container start), fake Telegram within the same second; host offline **34.5 s** after last seen (limit 60 s); quiet day: **1 line, 49 characters**, once per day | pass |
| S4 | Every operator statement links to evidence; otherwise "cause not found" | `internal/operator` `TestAskDropsUnsupportedStatements`, `TestAskWithoutEvidenceIsCauseNotFoundWithoutMoreCalls`; `test/operator` `qa_evidence`, `no_evidence` | reply `The host runs a busybox container. [ev_1]` with a `/hosts/<id>` link, unsupported claim removed (**277 ms**); exactly `cause not found` for an uncited statement | pass, scripted provider |
| S5 | Warnings, errors, fatal always reach the AI; identical lines merged; judged-green patterns skipped (target 95% never sent) | `internal/logpipe` corpus test; `test/integration` S5 test | corpus 20 000 lines to 68 events, **96.0%** suppressed after warm-up; live: 60 of 60 warn/error lines stored, after the first template judged green (**10 s**, 1 s windows) **100%** of 1 649 info lines not sent | pass |
| S6 | Non-local AI: personal data and secrets removed on the host first; if that fails nothing is sent | `internal/redact` recall test and fail-closed test; `internal/operator` `TestNonLocalProviderNeverSeesSecretsOrEmails`, `TestRedactionFailureSendsNothing` | recall on the labelled corpus: secrets **1.000**, personal data **1.000**, **0/90** clean lines changed; a failing redactor sent **0** requests | pass |
| S7 | Acts only at the autonomy level; never deletes; logs are untrusted; approvals only from verified accounts | `internal/agent` allowlist and policy tests, `test/integration` `TestAutonomyWelcomeAndPushPolicy`; `internal/operator` injection and level tests; `internal/chat` approval tests; `test/operator` `injection`, `legit_action` | levels: report refused, suggest stored for approval (no command sent), act restarted the container in **282 ms** (`act / auto / succeeded`); an obeyed log injection at level act left the healthy container untouched (`act / auto / refused`); command allowlist enumerated by protobuf reflection, operator class is exactly `restart_container`, no deleting kind; unlinked, non-admin and replayed approvals rejected | pass |
| S8 | Inventory on deploy and start, daily rescan, outbound watch; "scan failed", never "no vulnerabilities"; new advisory alerts within 15 min; 48 h wait | `test/security` (all scenarios); `internal/alerts` `TestNewCVEAdvisoryToAlert`; `internal/agent/conntrack` incl. `TestKernelSourceRealEvent` | known CVE with fixed version **0.8 s**; failed scan **14 ms**, helper timeout reported; deploy and start scans **3.3 s**; crafted advisory to alert **1.0 s** (production check every 10 min plus rematch, under 11 min worst case); misconfigurations reported **2.8 s**, resolved **5.9 s**; image younger than 48 h waits, backdated `created` still waits, eligible after aging first-seen | pass, narrowed |
| S9 | AI unreachable or capped: rule alerts keep working | `test/operator` `s9_*`, `cap`; unit tests with a blocking provider; `internal/alerts` and `internal/operator` import neither each other | provider hanging: incident in chat **9.6 s** after the container started, question answered "operator is unavailable" after **60.1 s** (the provider's own timeout); provider returning 500: **280 ms**; cap: **4 ms** and **0** provider calls | pass |
| S10 | Agent under 50 MB RAM, reconnects by itself | `test/integration` `TestAgentMemoryUnderLoad` and reconnect tests; `make bench-release-rss` on the release binary | RSS: **29.1 MiB** peak with 20 noisy containers (113 110 log lines in); **18.6 MiB** peak on the release build with 4 containers and scan and security active (19.3 MiB as root, live conntrack not confirmed in the harness); reconnect after server restart **0.4 s**, agent restart **0.1 s**, network restore **3.1 s**, silent loss detected after **25.0 s** and reconnected **0.5 s** after it ended | pass |
| S11 | Self-hosted: working login within 5 min of one command, every feature included, no data sent to us | `test/selfhost` (`install-to-login`); `test/install`; the self-host stack runs on an `--internal` Docker network with no route out | install to login **31.4 s** (image already built); no outbound connection except configured ones (the AI provider, chat bots and the vulnerability database URL are all explicit); tenant isolation test over every tenant table; no feature gated by edition | pass, narrowed |

## What these results do not show

These narrowings apply to the numbers above. None of them is hidden in a test.

- **S2**: the 30 s target is for **cached** builds (the sample app builds from cached layers). A cold build of a real application takes as long as its build. HTTPS is exercised with the internal CA and `*.localhost` names; public certificate issuance (Let's Encrypt) was not run.
- **S3**: Telegram runs against a fake Bot API server; Slack, Signal and email are covered by unit tests of the bridges and the delivery code only. No live chat service was used.
- **S4, S6, S7, S9**: the AI is a scripted provider (including a scripted OpenAI-compatible HTTP server in the real-stack test). The Anthropic and OpenAI-compatible wire formats are tested against `httptest` servers. **No run with a live provider was made** (no API key). Answer quality with a real model is therefore unmeasured.
- **S5**: measured on the recorded corpus and on synthetic noisy containers, not on production workloads.
- **S8**: new advisories are tested with a crafted database; the real Grype database was used for the benchmarks only (`docs/contributors/benchmarks-m7.md`). Real conntrack attribution was verified as root on this VM; without `CAP_NET_ADMIN` the state is "unavailable" with the reason. Syft exceeds the default 256 MiB inventory cap on large images (`node:22-bookworm`, `python:3.12`): those report "scan failed: oom" unless `HAETAE_INVENTORY_MEM` is raised. Grype versus Trivy agreement on four reference images is recorded in the benchmarks (0.35 to 1.0 Jaccard; Ubuntu differs because Grype includes unfixed entries).
- **S10**: arm64 binaries are verified by ELF header only (no qemu here); only amd64 was executed. "Reconnects after a reboot" is covered by restart and network-cut tests, not by rebooting a machine.
- **S11**: the install-to-login time excludes building or pulling the server image (5 min 45 s to build locally in the test; a published image was not available to pull). The **hosted** service is not built; "raw logs stay on the customer's server" holds by design (logs live in the agent's ring buffer, are sent only after on-host redaction and only on request) and is tested at the agent and redaction level.
- **Containers on one machine**: all hosts share one kernel and one Docker daemon.
- **Release**: no multi-arch image (no `buildx` here), no registry push, no signing (`docs/contributors/release.md`).

## Known limits and follow-ups

Tracked for after v1 (issue #13 and below):

- Agent client certificates last one year and the code has no renewal path: an agent that is not re-enrolled within a year stops connecting.
- The Signal bridge expects a signal-cli REST endpoint that the stack does not ship.
- The vulnerability matcher reports a distro it cannot recognise without an explicit "unsupported" state; the `scanned` map of the image-update loop is never pruned.
- `ai_actions` has no `approved_by` (the pressing account is only linked through the inbound chat message).
- Reasons for a provider timeout read "provider unreachable".
- Hosted-service operations (billing, tenancy at scale) are outside this build.

## Reproducing

```bash
scripts/acceptance.sh          # about 40 minutes; needs Docker, Chromium, cached busybox/caddy/alpine/nginx images
cat acceptance-results/summary.txt acceptance-results/measures.txt
```

Individual suites: `make test`, `make test-integration`, `make test-load`, `make test-selfhost`, `make test-e2e`, `make test-deploy`, `make test-alerts`, `make test-security`, `make test-operator`, `make release release-image test-release bench-release-rss`.
