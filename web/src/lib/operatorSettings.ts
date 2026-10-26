import type { OperatorAutonomy, OperatorProvider } from "../api/operatorTypes";

export const MAX_DAILY_TOKEN_CAP = 50_000_000;

export const AUTONOMY_OPTIONS: Array<{
  id: OperatorAutonomy;
  label: string;
  help: string;
}> = [
  {
    id: "report",
    label: "Report",
    help: "Answers questions and explains incidents. Never proposes or runs actions.",
  },
  {
    id: "suggest",
    label: "Suggest",
    help: "May propose restarting a crashed container. A verified admin approves each one in chat.",
  },
  {
    id: "act",
    label: "Act",
    help: "May restart crashed containers on its own. It never deletes anything.",
  },
];

/** One-line provider status for the settings page. */
export function providerStatus(p: OperatorProvider): string {
  if (!p.configured) {
    return "Not configured: set HAETAE_AI_PROVIDER (and the provider's key or URL) on the server.";
  }
  if (p.local) return `${p.name} (local, nothing leaves your network)`;
  return `${p.name} (cloud, redacted before sending)`;
}

/** Returns an error message for an invalid daily cap input, or null. */
export function validateTokenCap(raw: string): string | null {
  if (!/^\d+$/.test(raw.trim())) return "Enter a whole number of tokens (0 or more).";
  const n = Number(raw);
  if (!Number.isSafeInteger(n) || n > MAX_DAILY_TOKEN_CAP) {
    return `Must be at most ${MAX_DAILY_TOKEN_CAP.toLocaleString("en-US")}.`;
  }
  return null;
}
