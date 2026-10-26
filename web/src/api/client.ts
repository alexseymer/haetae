import type {
  AlertSettings,
  ChatAccountsResponse,
  ChatActionRequest,
  ChatActionResponse,
  ChatChannelsResponse,
  ChatLinkRequest,
  ChatLinkResponse,
  ChatMessagesQuery,
  ChatMessagesResponse,
  CreateMuteRequest,
  IncidentDetailResponse,
  IncidentsQuery,
  IncidentsResponse,
  MutesResponse,
  PostChatMessageRequest,
  PostChatMessageResponse,
} from "./m6Types";
import type {
  HostImageUpdatesResponse,
  HostOutboundResponse,
  HostSecurityResponse,
  HostVulnerabilitiesResponse,
  UpdateSettings,
} from "./securityTypes";
import type {
  ContainersResponse,
  CreateProjectRequest,
  CreateUserRequest,
  DeployLogsResponse,
  DeployResponse,
  DeploysResponse,
  EnrollmentRequest,
  EnrollmentResponse,
  HostDetailResponse,
  HostsResponse,
  LoginRequest,
  LoginResponse,
  LogsResponse,
  LogLevelFilter,
  MeResponse,
  MetricsResponse,
  PasswordRequest,
  PatchProjectRequest,
  ProjectResponse,
  ProjectsResponse,
  SetupRequest,
  SetupResponse,
  StatusResponse,
  UsersResponse,
} from "./types";

export class ApiError extends Error {
  status: number;
  retryAfter: number | null;

  constructor(status: number, message: string, retryAfter: number | null = null) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.retryAfter = retryAfter;
  }
}

/** Duck-typed check so callers still work if a bundler duplicates the class. */
export function isApiError(err: unknown): err is ApiError {
  if (err instanceof ApiError) return true;
  if (typeof err !== "object" || err === null) return false;
  const e = err as { name?: unknown; status?: unknown; message?: unknown; retryAfter?: unknown };
  return e.name === "ApiError" && typeof e.status === "number" && typeof e.message === "string";
}

type UnauthorizedHandler = () => void;

let onUnauthorized: UnauthorizedHandler | null = null;

export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  onUnauthorized = handler;
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  skipUnauthorizedHandler?: boolean;
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? "GET";
  const headers: Record<string, string> = {};

  if (method !== "GET") {
    headers["Content-Type"] = "application/json";
    headers["X-Haetae-Request"] = "1";
  }

  const init: RequestInit = {
    method,
    credentials: "same-origin",
    headers,
  };

  if (options.body !== undefined) {
    init.body = JSON.stringify(options.body);
  }

  const res = await fetch(path, init);

  if (res.status === 401 && !options.skipUnauthorizedHandler) {
    onUnauthorized?.();
  }

  const retryAfterHeader = res.headers.get("Retry-After");
  let retryAfter: number | null = null;
  if (retryAfterHeader != null && retryAfterHeader !== "") {
    const n = Number(retryAfterHeader);
    retryAfter = Number.isFinite(n) ? n : null;
  }

  if (res.status === 204) {
    return undefined as T;
  }

  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text) as unknown;
    } catch {
      data = null;
    }
  }

  if (!res.ok) {
    const message =
      data &&
      typeof data === "object" &&
      "error" in data &&
      typeof (data as { error: unknown }).error === "string"
        ? (data as { error: string }).error
        : res.statusText || "Request failed";
    throw new ApiError(res.status, message, retryAfter);
  }

  return data as T;
}

export const api = {
  status: () => request<StatusResponse>("/api/status", { skipUnauthorizedHandler: true }),

  setup: (body: SetupRequest) =>
    request<SetupResponse>("/api/setup", { method: "POST", body, skipUnauthorizedHandler: true }),

  login: (body: LoginRequest) =>
    request<LoginResponse>("/api/login", { method: "POST", body, skipUnauthorizedHandler: true }),

  logout: () =>
    request<void>("/api/logout", { method: "POST", body: {}, skipUnauthorizedHandler: true }),

  me: () => request<MeResponse>("/api/me", { skipUnauthorizedHandler: true }),

  changePassword: (body: PasswordRequest) =>
    request<void>("/api/password", { method: "POST", body }),

  users: () => request<UsersResponse>("/api/users"),

  createUser: (body: CreateUserRequest) =>
    request<UserLike>("/api/users", { method: "POST", body }),

  hosts: () => request<HostsResponse>("/api/hosts"),

  host: (id: string) => request<HostDetailResponse>(`/api/hosts/${encodeURIComponent(id)}`),

  containers: (id: string) =>
    request<ContainersResponse>(`/api/hosts/${encodeURIComponent(id)}/containers`),

  hostMetrics: (id: string, metric: string, from: number, to: number) =>
    request<MetricsResponse>(
      `/api/hosts/${encodeURIComponent(id)}/metrics?metric=${encodeURIComponent(metric)}&from=${from}&to=${to}`,
    ),

  containerMetrics: (id: string, cid: string, metric: string, from: number, to: number) =>
    request<MetricsResponse>(
      `/api/hosts/${encodeURIComponent(id)}/containers/${encodeURIComponent(cid)}/metrics?metric=${encodeURIComponent(metric)}&from=${from}&to=${to}`,
    ),

  logs: (id: string, minLevel: LogLevelFilter, limit: number) =>
    request<LogsResponse>(
      `/api/hosts/${encodeURIComponent(id)}/logs?minLevel=${encodeURIComponent(minLevel)}&limit=${limit}`,
    ),

  enroll: (body: EnrollmentRequest) =>
    request<EnrollmentResponse>("/api/hosts/enrollment", { method: "POST", body }),

  projects: (hostId?: string) =>
    request<ProjectsResponse>(
      hostId
        ? `/api/projects?hostId=${encodeURIComponent(hostId)}`
        : "/api/projects",
    ),

  createProject: (hostId: string, body: CreateProjectRequest) =>
    request<ProjectResponse>(`/api/hosts/${encodeURIComponent(hostId)}/projects`, {
      method: "POST",
      body,
    }),

  project: (pid: string) =>
    request<ProjectResponse>(`/api/projects/${encodeURIComponent(pid)}`),

  patchProject: (pid: string, body: PatchProjectRequest) =>
    request<ProjectResponse>(`/api/projects/${encodeURIComponent(pid)}`, {
      method: "PATCH",
      body,
    }),

  deleteProject: (pid: string, force = false) =>
    request<void>(
      `/api/projects/${encodeURIComponent(pid)}${force ? "?force=1" : ""}`,
      { method: "DELETE", body: {} },
    ),

  startDeploy: (pid: string) =>
    request<DeployResponse>(`/api/projects/${encodeURIComponent(pid)}/deploy`, {
      method: "POST",
      body: {},
    }),

  projectDeploys: (pid: string) =>
    request<DeploysResponse>(`/api/projects/${encodeURIComponent(pid)}/deploys`),

  deploy: (did: string) =>
    request<DeployResponse>(`/api/deploys/${encodeURIComponent(did)}`),

  deployLogs: (did: string, after: number, limit?: number) => {
    const params = new URLSearchParams({ after: String(after) });
    if (limit != null) params.set("limit", String(limit));
    return request<DeployLogsResponse>(
      `/api/deploys/${encodeURIComponent(did)}/logs?${params.toString()}`,
    );
  },

  rollbackDeploy: (did: string) =>
    request<DeployResponse>(`/api/deploys/${encodeURIComponent(did)}/rollback`, {
      method: "POST",
      body: {},
    }),

  incidents: (query: IncidentsQuery = {}) => {
    const params = new URLSearchParams();
    if (query.state) params.set("state", query.state);
    if (query.hostId) params.set("hostId", query.hostId);
    if (query.limit != null) params.set("limit", String(query.limit));
    const qs = params.toString();
    return request<IncidentsResponse>(`/api/incidents${qs ? `?${qs}` : ""}`);
  },

  incident: (id: string) =>
    request<IncidentDetailResponse>(`/api/incidents/${encodeURIComponent(id)}`),

  alertSettings: () => request<AlertSettings>("/api/settings/alerts"),

  putAlertSettings: (body: AlertSettings) =>
    request<AlertSettings>("/api/settings/alerts", { method: "PUT", body }),

  mutes: () => request<MutesResponse>("/api/mutes"),

  createMute: (body: CreateMuteRequest) =>
    request<MuteLike>("/api/mutes", { method: "POST", body }),

  deleteMute: (id: string) =>
    request<void>(`/api/mutes/${encodeURIComponent(id)}`, { method: "DELETE", body: {} }),

  chatChannels: () => request<ChatChannelsResponse>("/api/chat/channels"),

  chatMessages: (query: ChatMessagesQuery = {}) => {
    const params = new URLSearchParams();
    if (query.after) params.set("after", query.after);
    if (query.limit != null) params.set("limit", String(query.limit));
    const qs = params.toString();
    return request<ChatMessagesResponse>(`/api/chat/messages${qs ? `?${qs}` : ""}`);
  },

  postChatMessage: (body: PostChatMessageRequest) =>
    request<PostChatMessageResponse>("/api/chat/messages", { method: "POST", body }),

  chatAction: (body: ChatActionRequest) =>
    request<ChatActionResponse>("/api/chat/actions", { method: "POST", body }),

  chatAccounts: () => request<ChatAccountsResponse>("/api/chat/accounts"),

  deleteChatAccount: (id: string) =>
    request<void>(`/api/chat/accounts/${encodeURIComponent(id)}`, {
      method: "DELETE",
      body: {},
    }),

  chatLink: (body: ChatLinkRequest) =>
    request<ChatLinkResponse>("/api/chat/link", { method: "POST", body }),

  hostSecurity: (id: string) =>
    request<HostSecurityResponse>(`/api/hosts/${encodeURIComponent(id)}/security`),

  hostVulnerabilities: (id: string) =>
    request<HostVulnerabilitiesResponse>(
      `/api/hosts/${encodeURIComponent(id)}/vulnerabilities`,
    ),

  forceContainerScan: (hostId: string, cid: string) =>
    request<{ accepted: boolean; scanId: string }>(
      `/api/hosts/${encodeURIComponent(hostId)}/containers/${encodeURIComponent(cid)}/scan`,
      { method: "POST", body: {} },
    ),

  hostOutbound: (id: string, limit?: number) => {
    const qs = limit != null ? `?limit=${limit}` : "";
    return request<HostOutboundResponse>(
      `/api/hosts/${encodeURIComponent(id)}/outbound${qs}`,
    );
  },

  hostImageUpdates: (id: string) =>
    request<HostImageUpdatesResponse>(
      `/api/hosts/${encodeURIComponent(id)}/image-updates`,
    ),

  updateSettings: () => request<UpdateSettings>("/api/settings/updates"),

  putUpdateSettings: (body: UpdateSettings) =>
    request<UpdateSettings>("/api/settings/updates", { method: "PUT", body }),
};

interface MuteLike {
  id: string;
  hostId: string | null;
  rule: string;
  until: string;
  createdAt: string;
}

interface UserLike {
  id: string;
  email: string;
  role: string;
}
