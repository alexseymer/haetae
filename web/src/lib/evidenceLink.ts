/**
 * Allowlist for chat evidence paths: exact app-relative patterns only
 * (/hosts/<uuid>, /incidents/<uuid>, optional ?tab=…), not arbitrary prefixes.
 */

const UUID =
  "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}";

const ALLOWED = [
  new RegExp(`^/hosts/${UUID}$`),
  new RegExp(`^/hosts/${UUID}\\?tab=[a-zA-Z0-9_-]+$`),
  new RegExp(`^/incidents/${UUID}$`),
  new RegExp(`^/incidents/${UUID}\\?tab=[a-zA-Z0-9_-]+$`),
  /^\/hosts$/,
  /^\/incidents$/,
];

export function isSafeEvidencePath(path: string): boolean {
  if (typeof path !== "string" || path.length === 0) return false;
  if (!path.startsWith("/")) return false;
  if (path.startsWith("//")) return false;
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(path)) return false;
  if (path.includes("://")) return false;
  return ALLOWED.some((re) => re.test(path));
}
