# Core principle: the Linux principle

Haetae is built the way Linux and Unix are built. This is a standing rule for every change, not a style preference. It has two parts: how the software is designed, and how it is developed.

## 1. Design: the Unix philosophy

1. **One thing, done well.** Every binary, package and component has one job that fits in a sentence. If a description needs "and", split it. The agent, the server and the inventory helper are separate programs for this reason.
2. **Built to work together.** Output of one part is input to the next. Components talk through small, explicit interfaces (protobuf messages, HTTP/JSON, plain streams), never through shared internals.
3. **Text is the universal interface.** Anything an operator or a script may need is available as text: JSON or plain lines from the API and the CLI, configuration in environment variables and plain files, logs as text lines. No data that can only be read through our UI.
4. **Small is beautiful.** Prefer a small package with a narrow API over a large one with options. Reject a feature that needs a new subsystem when a composition of existing parts will do.
5. **Prototype early.** Build the smallest working version first, test it for real (real Docker, real Postgres, real host), then refine. Fakes support tests; they do not replace a real run.
6. **Portability before cleverness.** Static binaries, no CGO, standard `docker compose`, the Go standard library where it is enough. Do not tie a feature to one distribution, cloud or vendor when a portable way exists.
7. **Simple text for data people own.** Configuration and exports (backups, SBOMs, audit log, briefings) are plain, documented text formats that can be read, diffed and moved without our software.
8. **Scripts as leverage.** Operations that users repeat (install, enroll, backup, restore, release) are short, readable shell scripts or one-line commands, not hidden behind the UI.
9. **No captive interface.** Everything the web UI can do is also possible through the API, so users can automate, script and leave. The UI is one client among others.
10. **Every tool is a filter.** CLI commands read stdin or arguments, write results to stdout and errors to stderr, return meaningful exit codes, and do not prompt unless asked to. They compose in a pipeline.

## 2. Development: the bazaar

1. **Release early, release often.** Small PRs, merged when green; frequent tagged releases rather than big drops.
2. **Many eyes.** Every change is reviewed independently, and users are co-developers: issues, logs and reproductions are welcome and answered. Decisions are made in the open, in issues, PRs and ADRs, not in private chats.
3. **Open by default.** AGPL-3.0, public roadmap, public acceptance evidence, public known gaps (the `Later` issues). A shortcoming is recorded, never hidden.
4. **Clear maintainers, distributed ownership.** Each area has design notes and tests that let a newcomer work on it alone; the maintainer has the final decision but must give the reason in writing.
5. **Evidence over opinion.** A claim ("fast", "secure", "under 50 MB") needs a test or a measurement in the repository.

## How to apply it in review

A PR description answers two questions, and a reviewer blocks the PR if the answer is missing or weak:

- *Which single job does the new or changed part do, and what are its text or API inputs and outputs?*
- *Can a user do this without the web UI, and can the result be piped, saved and read as plain text?*

Where a hard constraint in `spec.md` and this principle collide, the hard constraint wins and the conflict is recorded in an ADR.
