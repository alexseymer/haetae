import type { OperatorAutonomy, OperatorProvider, OperatorTestResult } from "../api/operatorTypes";

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
  const model = p.model ? `, model ${p.model}` : "";
  if (p.local) return `${p.name} (local, nothing leaves your network)${model}`;
  return `${p.name} (cloud, redacted before sending)${model}`;
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

/** The one-line outcome of a connection test, and whether it needs attention. */
export function describeTest(r: OperatorTestResult): { tone: "ok" | "warn" | "fail"; text: string } {
  if (!r.ok) {
    return { tone: "fail", text: `Connection failed: ${r.reason ?? "unknown error"}.${r.hint ? ` ${r.hint}` : ""}` };
  }
  const head = `Connected to ${r.provider} / ${r.model} in ${r.latencyMs} ms, tool calling: ${r.toolCalling}`;
  if (r.toolCalling === "supported") return { tone: "ok", text: head };
  if (r.toolCalling === "not used by this model") {
    return {
      tone: "warn",
      text: `${head}. This model did not call tools; the operator needs a model with tool support, otherwise it will answer 'cause not found'.${r.reason ? ` Provider said: ${r.reason}.` : ""}`,
    };
  }
  return { tone: "warn", text: `${head}. Could not tell whether the model supports tools.` };
}
