/**
 * Allowlist for chat evidence paths: must be app-relative, start with a single
 * slash, no scheme, no protocol-relative URL.
 */
export function isSafeEvidencePath(path: string): boolean {
  if (typeof path !== "string" || path.length === 0) return false;
  if (!path.startsWith("/")) return false;
  if (path.startsWith("//")) return false;
  // Reject any scheme (javascript:, https:, data:, …) before the first slash path.
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(path)) return false;
  // Paths like "/foo" never match a scheme; also reject embedded "://" just in case.
  if (path.includes("://")) return false;
  return true;
}
