/**
 * Every help text shown in the UI lives here so it can be reviewed in one place.
 * Wording follows docs/*.md; do not describe behaviour that the docs do not.
 */

export const DOCS_BASE = "https://github.com/alexseymer/haetae/blob/main/docs";

export const DOCS_INDEX = "https://github.com/alexseymer/haetae/tree/main/docs";

export type DocName =
  | "install"
  | "enroll"
  | "deploy"
  | "chat"
  | "ai-operator"
  | "security"
  | "troubleshooting";

export function docUrl(doc: DocName): string {
  return `${DOCS_BASE}/${doc}.md`;
}

export const DOC_LINKS: Array<{ doc: DocName; label: string; blurb: string }> = [
  { doc: "install", label: "Install", blurb: "Run the server and get a first login." },
  { doc: "enroll", label: "Enroll a host", blurb: "Connect a server with one command." },
  { doc: "deploy", label: "Deploy apps", blurb: "Git or compose file, domains, rollback." },
  { doc: "chat", label: "Chat and alerts", blurb: "Telegram, Slack, email, Signal, the daily briefing." },
  { doc: "ai-operator", label: "AI operator", blurb: "What it does, autonomy levels, the daily cap." },
  { doc: "security", label: "Security scanning", blurb: "Vulnerabilities, misconfigurations, image updates." },
  { doc: "troubleshooting", label: "Troubleshooting", blurb: "When something does not work." },
];

export interface HelpEntry {
  /** Short term, also the glossary heading. */
  title: string;
  /** One or two plain sentences. */
  body: string;
  doc?: DocName;
}

export const HELP = {
  severity: {
    title: "Severity",
    body: "How urgent a problem is: critical, high, medium or low. High and critical are sent to your chat right away; low and medium wait for the daily briefing and show under Incidents.",
    doc: "chat",
  },
  incident: {
    title: "Incident",
    body: "One open item per host that groups related alerts, so you look at one thing instead of many. You can open one to see its alerts and evidence.",
    doc: "chat",
  },
  alert: {
    title: "Alert",
    body: "A single rule firing, for example a container restarting again and again or a disk filling up. Alerts on the same host are grouped into one incident. Alerts work even when the AI is off.",
    doc: "chat",
  },
  autonomy: {
    title: "Autonomy",
    body: "What the AI operator may do on its own. Report: only answers and explains. Suggest: may propose restarting a crashed container, and an admin approves each one. Act: may restart crashed containers itself. It never deletes anything.",
    doc: "ai-operator",
  },
  tokenCap: {
    title: "Daily token cap",
    body: "The most AI text (input plus output tokens) Haetae uses per UTC day. At the cap the operator pauses until the next day and rule-based alerts keep working. 0 means no AI use.",
    doc: "ai-operator",
  },
  toolCalling: {
    title: "Tool calling",
    body: "The operator answers by asking the model to call small tools that read your hosts, metrics and logs. A model without tool support cannot do this and the operator answers \"cause not found\". Use Test connection in Settings to check your model. Free models on OpenRouter often lack it.",
    doc: "ai-operator",
  },
  matchingPending: {
    title: "Matching pending",
    body: "Haetae has the list of software in this container but has not checked it against the vulnerability database yet. It is not a clean result, and it retries on its own.",
    doc: "security",
  },
  scanFailed: {
    title: "Scan failed",
    body: "The software scan did not finish, for example it ran out of memory or timed out. Haetae shows this instead of \"no vulnerabilities\": a failed scan is never reported as clean.",
    doc: "security",
  },
  securityBadge: {
    title: "Security column",
    body: "The number of critical and high vulnerabilities in running containers on this host. \"scan failed\" or \"matching pending\" means the result is not known yet, never that the host is clean.",
    doc: "security",
  },
  notMonitored: {
    title: "Not monitored",
    body: "Containers that use the host network cannot have their outbound connections attributed to them, so they are not watched.",
    doc: "security",
  },
  degraded: {
    title: "Degraded",
    body: "The outbound connection watch lost events or cannot run, so the list below may be incomplete. It needs the nf_conntrack kernel module and the NET_ADMIN capability on the agent.",
    doc: "security",
  },
  logTemplates: {
    title: "Log templates: green and watch",
    body: "Identical log lines are merged into one template with a count. Routine info and debug templates judged green are skipped. The AI may mark one as watch, but warnings, errors and fatal lines always get through.",
    doc: "ai-operator",
  },
  withheld: {
    title: "Withheld",
    body: "The share of log lines the agent did not send to the server because they were routine. Warnings, errors and fatal lines are never withheld.",
    doc: "ai-operator",
  },
  rollback: {
    title: "Rollback",
    body: "Starts a new deploy of an earlier healthy version. Volumes and data are kept. If a new version fails to start, Haetae restores the previous one automatically.",
    doc: "deploy",
  },
  ingress: {
    title: "Domain and ingress",
    body: "To serve the app on a domain over HTTPS, give the domain, the service name and its port. The domain's DNS must point to the host and ports 80 and 443 must be reachable. All three fields are needed together.",
    doc: "deploy",
  },
  autoDeploy: {
    title: "Auto-deploy on push",
    body: "For Git projects. The agent checks the branch every 10 seconds and deploys when it changes. There is no webhook and no inbound port.",
    doc: "deploy",
  },
  autoPull: {
    title: "Image updates",
    body: "A newly published image waits 48 hours before it can be pulled, unless it fixes a verified critical vulnerability. Pulling never restarts containers; redeploy to use the new image.",
    doc: "security",
  },
  enrollToken: {
    title: "Install command",
    body: "The command contains a one-time token. It works once and stops working after the validity period.",
    doc: "enroll",
  },
  agent: {
    title: "Agent",
    body: "A small program on each server. It connects out to Haetae, so it needs no open inbound port, and it reconnects by itself.",
    doc: "enroll",
  },
} satisfies Record<string, HelpEntry>;

export type HelpTerm = keyof typeof HELP;

/** Order of the glossary on the Help page. */
export const GLOSSARY_ORDER: HelpTerm[] = [
  "agent",
  "alert",
  "incident",
  "severity",
  "autonomy",
  "tokenCap",
  "toolCalling",
  "logTemplates",
  "withheld",
  "scanFailed",
  "matchingPending",
  "securityBadge",
  "notMonitored",
  "degraded",
  "autoPull",
  "ingress",
  "autoDeploy",
  "rollback",
  "enrollToken",
];

/** Short intro under each page title. */
export const INTROS = {
  hosts:
    "A host is a Linux server running Docker that Haetae watches and deploys to. See every server's status here, and open one for its containers, logs, deploys and security.",
  hostOverview:
    "CPU, memory, disk and network over time, and how many log lines the agent read and sent.",
  hostContainers:
    "The containers running on this host, grouped by Compose stack. Select one to see its charts.",
  hostLogs:
    "Warnings and errors from this host's containers. Identical lines are merged with a count, and secrets are removed on the host first.",
  hostDeploy:
    "Projects are Docker Compose apps on this host, deployed from a Git repository or a pasted compose file.",
  hostSecurity:
    "What is installed in each running container, which known vulnerabilities (CVEs) affect it, risky container settings, outbound connections and image updates.",
  incidents:
    "Something that needs attention, such as a crashing container or an offline host. Related alerts are grouped into one incident per host. Open ones need a look; resolved ones stay as history.",
  incidentDetail:
    "The alerts that make up this incident, each with the evidence behind it. Mute the host if the noise is expected.",
  chat: "Ask the operator a question in plain language. Incident reports and the daily briefing also appear here.",
  settings: "Your account, who can sign in, when alerts fire, where messages go, and the optional AI operator.",
  settingsAccount: "Change your password and see who can sign in. Admins can add people.",
  settingsAlerts:
    "When Haetae raises an alert and when it sends the daily briefing. Mutes silence notifications for a host without hiding the alerts.",
  settingsChat:
    "Where Haetae sends incident messages and the daily briefing. Link your own account to a channel to receive them there.",
  settingsOperator:
    "The AI operator answers questions and explains incidents. It is optional: rule-based alerts and the briefing work without it.",
  project:
    "A project is one Compose app on one host. Deploy it, follow the build, and roll back to an earlier version.",
  deploy: "One deploy of a project: its progress, result and build log.",
  help: "How the pieces fit together, what the terms mean, and where the full documentation lives.",
} as const;
