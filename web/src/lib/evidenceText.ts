/** Format alert evidence as plain text key/value lines (React text, not HTML). */
export function evidenceToText(evidence: unknown): string {
  if (evidence == null) return "";
  if (typeof evidence !== "object" || Array.isArray(evidence)) {
    return typeof evidence === "string" ? evidence : JSON.stringify(evidence);
  }
  const entries = Object.entries(evidence as Record<string, unknown>);
  if (entries.length === 0) return "";
  return entries
    .map(([k, v]) => {
      const value =
        typeof v === "string"
          ? v
          : v == null
            ? ""
            : typeof v === "object"
              ? JSON.stringify(v)
              : String(v);
      return `${k}: ${value}`;
    })
    .join("\n");
}
