/** M6 alerts, incidents, mutes, and chat API types (JSON camelCase). */

export type Severity = "low" | "medium" | "high" | "critical";

export type IncidentState = "open" | "resolved";

export type AlertState = "firing" | "resolved";

export interface IncidentSummary {
  id: string;
  orgId: string;
  hostId: string;
  hostName: string;
  title: string;
  severity: Severity | string;
  state: IncidentState | string;
  openedAt: string;
  updatedAt: string;
  resolvedAt: string | null;
  alertCount: number;
}

export interface IncidentsResponse {
  incidents: IncidentSummary[];
}

export interface AlertDetail {
  id: string;
  rule: string;
  container: string;
  severity: Severity | string;
  state: AlertState | string;
  summary: string;
  /** JSON object from the server; render as escaped text key/values. */
  evidence: Record<string, unknown> | unknown;
  firstAt: string;
  lastAt: string;
  resolvedAt: string | null;
}

export interface IncidentDetailResponse {
  incident: IncidentSummary;
  alerts: AlertDetail[];
}

export interface AlertSettings {
  timeZone: string;
  briefingHour: number;
  cpuPct: number;
  memPct: number;
  diskPct: number;
  sustainedS: number;
  offlineAfterS: number;
}

export interface Mute {
  id: string;
  hostId: string | null;
  rule: string;
  until: string;
  createdAt: string;
}

export interface MutesResponse {
  mutes: Mute[];
}

export interface CreateMuteRequest {
  hostId?: string;
  rule?: string;
  days: number;
}

export type ChatChannelName = "web" | "telegram" | "slack" | "email" | "signal";

export interface ChatChannel {
  name: string;
  enabled: boolean;
}

export interface ChatChannelsResponse {
  channels: ChatChannel[];
}

export interface ChatAction {
  id: string;
  label: string;
}

export interface ChatEvidence {
  label: string;
  path: string;
}

/** Structured fields of an incident message (web channel). Links are in `evidence`. */
export interface ChatMeta {
  severity: string;
  title: string;
  state: "open" | "resolved" | string;
  host?: string;
  summary?: string[];
}

export type ChatMessageKind = "incident" | "briefing" | "answer" | "text" | "action";

export type ChatDirection = "in" | "out";

export interface ChatMessage {
  id: string;
  kind: ChatMessageKind | string;
  direction: ChatDirection | string;
  body: string;
  actions: ChatAction[];
  evidence: ChatEvidence[];
  meta?: ChatMeta;
  incidentId: string | null;
  createdAt: string;
}

export interface ChatMessagesResponse {
  messages: ChatMessage[];
  next: string | null;
}

export interface PostChatMessageRequest {
  body: string;
}

export interface PostChatMessageResponse {
  reply?: string;
}

export interface ChatActionRequest {
  messageId: string;
  action: string;
}

export interface ChatActionResponse {
  reply: string;
}

export interface ChatAccount {
  id: string;
  channel: string;
  /** Masked by the server. */
  externalId: string;
  createdAt: string;
}

export interface ChatAccountsResponse {
  accounts: ChatAccount[];
}

export type LinkableChannel = "telegram" | "slack" | "email" | "signal";

export interface ChatLinkRequest {
  channel: LinkableChannel;
}

export interface ChatLinkResponse {
  /** Absent for email: the code is only sent to the user's own address. */
  code?: string;
  expiresAt: string;
  instructions: string;
}

export interface ChatLinkConfirmRequest {
  channel: "email";
  code: string;
}

export interface ChatLinkConfirmResponse {
  linked: boolean;
  channel: string;
  externalId: string;
}

export interface IncidentsQuery {
  state?: IncidentState;
  hostId?: string;
  limit?: number;
}

export interface ChatMessagesQuery {
  after?: string;
  limit?: number;
}
