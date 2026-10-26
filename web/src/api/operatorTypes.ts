/** M8 AI operator settings API types (JSON camelCase). */

export type OperatorAutonomy = "report" | "suggest" | "act";

export interface OperatorProvider {
  configured: boolean;
  name: string;
  local: boolean;
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
