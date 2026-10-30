export type DigestWindow = "1h" | "6h" | "24h";

export interface DigestStatement {
  text: string;
  evidence: string[];
}

export interface DigestEvidence {
  id: string;
  title: string;
  /** App path such as /hosts/<id>?tab=logs; only rendered as a link when allowlisted. */
  link: string;
  kind: string;
}

export interface DigestResponse {
  kind?: string;
  title: string;
  generatedAt: string;
  windowFrom: string;
  windowTo: string;
  usedAI: boolean;
  /** Plain text, set when usedAI is false. */
  reason?: string;
  quiet?: boolean;
  needsAttention?: boolean;
  statements: DigestStatement[];
  evidence: DigestEvidence[];
}
