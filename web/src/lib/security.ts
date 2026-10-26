import { safeHttpsHref } from "./deploy";
import type { ContainerVuln, ScanStatus } from "../api/securityTypes";

/** Plain-words misconfiguration kind labels. */
export function misconfigKindLabel(kind: string): string {
  switch (kind) {
    case "published_port":
      return "published port";
    case "docker_socket":
      return "Docker socket mounted";
    case "root_user":
      return "runs as root";
    case "privileged":
      return "privileged";
    case "host_network":
      return "host network";
    case "host_pid":
      return "host PID namespace";
    case "dangerous_capability":
      return "dangerous capability";
    case "cert_expiring":
      return "certificate expiring";
    default:
      return kind;
  }
}

/** Plain-words image update decision labels. */
export function imageUpdateDecisionLabel(decision: string): string {
  switch (decision) {
    case "current":
      return "up to date";
    case "wait":
      return "waiting 48 h";
    case "eligible":
      return "eligible";
    case "eligible_critical_fix":
      return "eligible because it fixes a critical CVE";
    case "unknown":
      return "unknown";
    default:
      return decision || "unknown";
  }
}

export function isScanFailedStatus(status: ScanStatus | string): boolean {
  return (
    status === "failed" ||
    status === "timeout" ||
    status === "oom" ||
    status === "unsupported"
  );
}

/** Status line for a container vulnerability card. */
export function containerScanStatusLine(
  c: ContainerVuln,
  formatRelative: (iso: string | null) => string,
  formatDate: (iso: string | null) => string,
): string {
  if (isScanFailedStatus(c.scan.status)) {
    const reason = c.scan.error || c.scan.status;
    return `Scan failed: ${reason}`;
  }
  if (c.match.status === "failed") {
    const reason = c.match.error || "match failed";
    return `Scan failed: ${reason}`;
  }
  if (c.scan.status === "none" || c.scan.status === "pending") {
    if (c.scan.status === "pending") return "Scan pending…";
    return "Not scanned yet";
  }
  if (c.scan.status === "ok" && c.match.status !== "ok") {
    return "Matching pending";
  }
  if (c.scan.status === "ok") {
    const when = formatRelative(c.scan.at);
    const db = c.match.dbBuiltAt ? formatDate(c.match.dbBuiltAt) : null;
    if (db) return `Scanned ${when} (database ${db})`;
    return `Scanned ${when}`;
  }
  return c.scan.status;
}

/** CVE id as a link only when a https URL is available. */
export function vulnIdHref(vulnId: string, urls: string[]): string | null {
  for (const u of urls) {
    const href = safeHttpsHref(u);
    if (href) return href;
  }
  void vulnId;
  return null;
}

export function hostSecurityBadgeText(
  scanFailed: boolean | undefined,
  summary: { critical: number; high: number } | null,
  matchPending?: number,
): string | null {
  if (scanFailed) return "scan failed";
  if (!summary) return null;
  const n = summary.critical + summary.high;
  if (n <= 0) return matchPending && matchPending > 0 ? "matching pending" : null;
  return String(n);
}
