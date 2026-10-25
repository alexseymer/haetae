import type { Deploy, DeployBrief, DeployStatus, Project, ProjectSource } from "../api/types";

type BadgeVariant = "online" | "offline" | "warn" | "danger" | "info";

const DOMAIN_RE =
  /^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;

/** True when domain is a plain host name suitable for https:// linking. */
export function isValidDomain(domain: string): boolean {
  return domain.length > 0 && domain.length <= 253 && DOMAIN_RE.test(domain);
}

/** Return an href only when URL parses and protocol is exactly https:. */
export function safeHttpsHref(raw: string): string | null {
  if (!raw) return null;
  try {
    const u = new URL(raw);
    if (u.protocol === "https:") return u.href;
  } catch {
    // not a URL
  }
  return null;
}

export function domainHref(domain: string): string | null {
  if (!isValidDomain(domain)) return null;
  return `https://${domain}`;
}

export function sourceSummary(source: ProjectSource): string {
  if (source.kind === "inline") return "pasted compose";
  const url = source.gitUrl ?? "";
  const ref = source.gitRef || "main";
  let hostPath = url;
  if (hostPath.startsWith("https://")) hostPath = hostPath.slice("https://".length);
  hostPath = hostPath.replace(/\.git$/, "").replace(/\/$/, "");
  return hostPath ? `${hostPath}@${ref}` : `@${ref}`;
}

export function shortSha(commit: string): string {
  if (!commit) return "—";
  return commit.length > 7 ? commit.slice(0, 7) : commit;
}

export function triggerLabel(trigger: string): string {
  switch (trigger) {
    case "manual":
      return "Manual";
    case "push":
      return "Push";
    case "rollback":
      return "Rollback";
    default:
      return trigger || "—";
  }
}

export function deployStatusLabel(status: DeployStatus | string): string {
  switch (status) {
    case "queued":
      return "Queued";
    case "fetching":
      return "Fetching";
    case "building":
      return "Building";
    case "starting":
      return "Starting";
    case "checking":
      return "Checking";
    case "healthy":
      return "Healthy";
    case "failed":
      return "Failed";
    case "rolling_back":
      return "Rolling back";
    case "rolled_back":
      return "Rolled back";
    case "rollback_failed":
      return "Rollback failed";
    default:
      return status;
  }
}

export function deployStatusVariant(status: DeployStatus | string): BadgeVariant {
  switch (status) {
    case "healthy":
      return "online";
    case "failed":
    case "rollback_failed":
      return "danger";
    case "rolling_back":
    case "rolled_back":
    case "queued":
    case "fetching":
    case "building":
    case "starting":
    case "checking":
      return "warn";
    default:
      return "info";
  }
}

export function formatDuration(
  startedAt: string | null,
  finishedAt: string | null,
): string {
  if (!startedAt) return "—";
  const start = Date.parse(startedAt);
  const end = finishedAt ? Date.parse(finishedAt) : Date.now();
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return "—";
  const sec = Math.round((end - start) / 1000);
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  if (m < 60) return s === 0 ? `${m}m` : `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  const rm = m % 60;
  return rm === 0 ? `${h}h` : `${h}h ${rm}m`;
}

export const PHASE_STEPS = [
  "fetching",
  "building",
  "starting",
  "checking",
  "healthy",
] as const;

export type PhaseStep = (typeof PHASE_STEPS)[number];

export function phaseIndex(status: DeployStatus | string): number {
  if (status === "queued") return -1;
  const i = PHASE_STEPS.indexOf(status as PhaseStep);
  if (i >= 0) return i;
  if (status === "failed") return -2;
  if (
    status === "rolling_back" ||
    status === "rolled_back" ||
    status === "rollback_failed"
  ) {
    return -3;
  }
  return -1;
}

export function isTerminalStatus(status: DeployStatus | string): boolean {
  return (
    status === "healthy" ||
    status === "failed" ||
    status === "rolled_back" ||
    status === "rollback_failed"
  );
}

export function toDeployBrief(d: Deploy): DeployBrief {
  return {
    id: d.id,
    status: d.status,
    commit: d.commit,
    trigger: d.trigger,
    startedAt: d.startedAt,
    finishedAt: d.finishedAt,
    url: d.url,
    error: d.error,
  };
}

export function projectInlineCompose(
  project: Project,
  topLevel?: string,
): string {
  if (typeof topLevel === "string" && topLevel.length > 0) return topLevel;
  return project.source.inlineCompose ?? "";
}

/** Latest healthy deploy that is not this one (for rollback visibility). */
export function latestHealthyId(deploys: Deploy[]): string | null {
  for (const d of deploys) {
    if (d.status === "healthy") return d.id;
  }
  return null;
}

export function canRollback(deploy: Deploy, deploys: Deploy[]): boolean {
  if (deploy.status !== "healthy") return false;
  const latest = latestHealthyId(deploys);
  return latest != null && latest !== deploy.id;
}
