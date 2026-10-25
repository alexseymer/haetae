import type {
  ContainersResponse,
  CreateUserRequest,
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
};

interface UserLike {
  id: string;
  email: string;
  role: string;
}
