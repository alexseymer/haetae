import { Badge } from "./Badge";
import { severityLabel, severityVariant } from "../lib/severity";

/** Severity as text label plus colour (never colour alone). */
export function SeverityBadge({ severity }: { severity: string }) {
  return <Badge variant={severityVariant(severity)}>{severityLabel(severity)}</Badge>;
}
