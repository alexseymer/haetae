/** M8 AI operator settings API types (JSON camelCase). */

export type OperatorAutonomy = "report" | "suggest" | "act";

export interface OperatorProvider {
  configured: boolean;
  name: string;
  local: boolean;
  /** Configured model id (never the key or the base URL). */
  model?: string;
}

export interface OperatorTestResult {
  ok: boolean;
  latencyMs: number;
  provider: string;
  model: string;
  local: boolean;
  toolCalling: "supported" | "not used by this model" | "unknown";
  class?: string;
  reason?: string;
  hint?: string;
}

export interface OperatorSettings {
  aiEnabled: boolean;
  autonomy: OperatorAutonomy;
  dailyTokenCap: number;
  provider: OperatorProvider;
  usageToday: { inputTokens: number; outputTokens: number };
  available: boolean;
  reason: string;
}

export interface PutOperatorSettings {
  aiEnabled: boolean;
  autonomy: OperatorAutonomy;
  dailyTokenCap: number;
}
