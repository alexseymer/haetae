# M8 design: AI operator

Covers S4 (every statement links to evidence, otherwise "cause not found"), S6 (redaction before a non-local provider), S7 (autonomy, never deletes, logs are untrusted, approvals from verified accounts) and S9 (rule alerts keep working without the AI). The AI never has its own path to hosts: it can only call the read-only tools below, and it can only *propose* actions, which the server and the agent each check.

## Packages and ownership

- `internal/operator` (new): orchestrator, tools, evidence, answer validation, action gating, usage cap. Imports `internal/store`; does **not** import `internal/alerts` and `internal/alerts` does not import it (test-enforced, S9).
- `internal/operator/provider` (new): `Provider` interface, `Fake`, `Anthropic`, `OpenAICompat` (also covers local endpoints such as Ollama or llama.cpp). The only package that opens network connections for AI.
- `internal/agent` (changed): autonomy policy, `RestartContainer` command, enumerated command allowlist.
- `internal/chat`, `internal/briefing`, `internal/gateway/judge.go`, `internal/alerts` notifier hook (changed): call the operator through narrow interfaces and fall back to the M6 behaviour on any error.

## Configuration

Server-wide, from the environment (no unconfigured outbound calls, S11): `HAETAE_AI_PROVIDER` (`anthropic`, `openai`, `local`; empty = AI off), `HAETAE_AI_BASE_URL`, `HAETAE_AI_API_KEY`, `HAETAE_AI_MODEL`. `anthropic` and `openai` are non-local (redaction rule applies, see below); `local` is a loopback or private (RFC1918, unique-local) endpoint; link-local addresses such as the cloud metadata address are not local the operator runs. Per organisation, in `org_settings`: `ai_enabled` (default false), `autonomy` (`report` default, `suggest`, `act`), `ai_daily_token_cap` (default 200 000 input+output tokens per UTC day). The AI is on only when the provider is configured **and** the organisation enabled it.

## Provider interface

`Complete(ctx, Request) (Response, error)` with `Request{System, Messages, Tools, MaxTokens}` and `Response{Text, ToolCalls, Usage{InputTokens, OutputTokens}}`. Messages are role + text or tool result. Errors are typed: `ErrUnavailable` (network, 5xx, timeout), `ErrRateLimited`, `ErrAuth`, `ErrBadResponse`. Every provider has a hard timeout (default 60 s), a response size limit (1 MiB) and no redirects to other hosts. `Fake` is deterministic and scripted (a list of responses or a func), records every request so tests can assert exactly what was sent.

## Egress rule (S6)

Everything handed to a provider goes through `operator.Egress`: for non-local providers each string (system aside) is passed through the `internal/redact` pipeline on the server as defence in depth (logs were already redacted on the host before they left it); if redaction returns an error or exceeds its budget **nothing is sent** and the operation fails closed with `ErrRedaction`. Local providers skip the server-side pass but not the host-side one.

## Tools and evidence (S4)

Read-only tools over org-scoped store queries (the org is fixed by the caller, never a model argument): `list_hosts`, `list_containers(host_id)`, `get_metrics(host_id|container_id, from, to)`, `list_events(host_id, from, to)` (alerts and incidents), `list_deploys(project_id|host_id)`, `list_findings(container_id)` (CVEs, misconfigs, outbound state), `get_logs(container_id, from, to, max_lines)` (a `QueryLogs` command; the agent redacts on the host). Arguments are validated (UUID syntax, time window at most 24 h, line cap 200); unknown tools are refused. At most 8 tool calls per question.

Each tool result is stored as an **evidence record** (`ai_evidence`: id, org, conversation id, kind, subject type and id, title, summary text, structured data, a UI link, created_at, trusted flag). `kind` is one of `hosts`, `containers`, `metrics`, `events`, `deploys`, `findings`, `logs`. Records of kind `logs` are **untrusted** (their content is third-party text); all others are trusted structured data from our own tables. The model sees evidence ids (`ev_<n>`); the UI link opens the page (host, container, deploy, incident or log view) the evidence came from.

## Answer format and validation (S4)

The model's final output must be JSON: `{"statements":[{"text":"...","evidence":["ev_1","ev_3"]}],"actions":[{"kind":"restart_container","container_id":"...","reason":"...","evidence":["ev_2"]}]}`. The server parses it strictly (unknown fields, wrong types and oversize output are `ErrBadResponse`), then:

1. drops every statement whose `evidence` is empty or names an id that does not belong to this conversation;
2. if no statement is left, the reply is the fixed text **"cause not found"** (also used when the conversation produced no evidence at all, without calling the provider again);
3. renders the remaining statements, each followed by its evidence links.

If no statement is supported, proposed `actions` are ignored too (nothing is decided or recorded). Free text outside the JSON is ignored. The reply stores which statements were dropped (counts only) for the audit log.

## Untrusted text and prompt injection (S7)

Log lines, container names, labels, image names and commit messages are third-party text. They only ever reach the model inside tool results, JSON-encoded as string values under an `untrusted` key, never concatenated into the system prompt or the user's question, with the system prompt stating that such values are data and never instructions. Defence does not rely on the model obeying: (1) the model has no action tools, only a `proposed actions` field in its answer; (2) every proposed action is validated by the server (next section) against **trusted** evidence only; (3) the server never executes anything because a log said so: evidence of kind `logs` can support a statement but can never justify an action.

## Autonomy and actions (S7)

Levels: `report` (read-only: the operator answers and reports, proposes nothing), `suggest` (the operator may propose an action; it is sent as a message with an approval button, executed only after a verified, linked chat account of an organisation admin presses it, reusing the M6 single-use action refs; the approval buttons are sent only to verified accounts of admins, other accounts and the web chat, which every member sees, get the text with "Ask an administrator to approve"), `act` (the operator may execute **safe actions** on its own). The safe action set in v1 is exactly one kind: `restart_container`. There is no data-deleting action in the operator's catalogue or in the agent's command set the operator can reach; volumes, images and containers are never removed by the operator (`RemoveProject` stays a user-initiated deploy-service command, class `user`).

Server check for a proposed `restart_container`: the level allows it (`suggest` or `act`); the container belongs to a host of the organisation; at least one cited evidence record is **trusted** and shows that container as `exited`, `restarting`, `unhealthy` or crash-looping (so a healthy container can never be restarted because of what a log says); a per-container limit of 3 operator restarts per hour; `ExecuteApproved` re-checks with current data before sending (proposal still awaiting and unexpired, level, rate limit, container in the organisation and still exited, restarting or unhealthy; otherwise the row is refused and the chat says "no longer needed"); a proposal's approve and decline presses from different messages are serialised on the `ai_actions` row, a later press answers "Already handled."; every proposal and execution is written to `ai_actions` (org, container, level at the time, origin `auto|approved`, evidence ids, outcome).

AI off (`ai_enabled` false) means the effective level is `report` everywhere: the server's decisions, `Welcome.autonomy` and `Policy` pushed to agents (a settings change of `ai_enabled` pushes the policy too), whatever level is stored.

Agent check (separate and independent): the server tells the agent the organisation's level in `Welcome.autonomy` and in a `Policy` message whenever it changes; the agent's effective level is the lower of that and an optional local cap (`--max-autonomy`, default `act`). Every command carries an authorization (`UNSPECIFIED` for user-initiated, `APPROVED` after a button press, `AUTO` for operator-initiated). The agent keeps a table of every command kind with its class and refuses anything above the effective level: `ping`, `query_logs`, `scan_*`, `pull_image`, `deploy`, `remove_project` are class `user` (allowed at every level: user- or setting-initiated commands, plus the read-only diagnostic `query_logs` the operator may issue); `restart_container` is class `operator`: `APPROVED` needs level `suggest` or `act`, `AUTO` needs `act`, `UNSPECIFIED` is refused. A test enumerates the oneof kinds of `Command` against the table (adding a kind without classifying it fails the build) and asserts that no class or kind deletes data. An over-level command injected onto the agent's stream (a fake server sending `AUTO` restart to an agent at `suggest`) is refused with an error result.

## Uses

- **Chat questions** (`chat.Service` inbound text from a verified account): replaces `OperatorDisabledReply` when the AI is available; otherwise it returns "The operator is unavailable (<reason>); rule alerts are active." Conversation memory is not kept in v1 (each question is independent); evidence and the reply are stored.
- **Incident reports**: when an incident opens or its alert set grows, a background job asks the operator to explain the incident from evidence (same validation); the result is posted as a chat message with evidence links. If nothing is validated the message says "cause not found". The rule-based alert notification is sent first and independently.
- **Daily briefing text**: the briefing is composed by rules (at most 3 lines, 300 characters, unchanged). With the AI available the operator may rewrite the same facts; the result is accepted only if it still has at most 3 lines and 300 characters and mentions every item the rule text mentions (counts and names), else the rule text is used.
- **Log-template judging**: for templates the rule heuristic cannot decide (info/debug level, no `suspicious` word, low occurrence), the operator may return `green` or `watch`; it only ever sees redacted templates (never raw lines). Warn, error and fatal levels are never judged by it and never become green. Any failure falls back to the rule judge.

## Usage cap and S9

Each provider call adds its token usage to `ai_usage` (org, UTC day). Before a call the cap is checked; when reached the operation returns `ErrCapped`, which every use maps to its fallback (chat: unavailable message, briefing and judge: rule behaviour, incident report: skipped). A provider failure (`ErrUnavailable`, `ErrAuth`, timeouts) is handled the same way and is rate-limited in the logs. The alert evaluator, notifier and briefing scheduler never wait on the operator: all AI work runs in separate goroutines with their own timeouts, and the rule path is tested with a provider that blocks forever.

## Evidence for the PR (roadmap M8 accept)

With the fake provider: statements without evidence are removed; "cause not found" when evidence is empty; a log line containing an injection cannot trigger an action (a scripted fake that obeys it still gets refused, at every level); an action above the level is refused by the server and separately by the agent when injected on its stream; the agent allowlist is enumerated and has no data-deleting operation; with the provider unreachable, capped or hanging, rule alerts and the rule briefing still work. Wire-format tests for both HTTP providers against `httptest` servers. A recorded run with a real provider is attached when an API key is available (none on this host).
