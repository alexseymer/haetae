import type { Variant } from "../components/Badge";
import type { Severity } from "../api/m6Types";

/** Human-readable severity label (always shown as text, not colour alone). */
export function severityLabel(severity: string): string {
  switch (severity) {
    case "critical":
      return "critical";
    case "high":
      return "high";
    case "medium":
      return "medium";
    case "low":
      return "low";
    default:
      return severity || "unknown";
  }
}

export function severityVariant(severity: string): Variant {
  switch (severity as Severity) {
    case "critical":
    case "high":
      return "danger";
    case "medium":
      return "warn";
    default:
      return "info";
  }
}
