export type Role = "admin" | "member";

export interface User {
  id: string;
  email: string;
  role: Role;
}

export interface StatusResponse {
  setupRequired: boolean;
  version: string;
}

export interface SetupRequest {
  setupCode: string;
  orgName: string;
  email: string;
  password: string;
}

export interface SetupResponse {
  user: User;
  orgName: string;
}

export interface LoginRequest {
  email: string;
  password: string;
}

export interface LoginResponse {
  user: User;
}

export interface MeResponse {
  user: User;
  orgName: string;
}

export interface PasswordRequest {
  current: string;
  new: string;
}

export interface UsersResponse {
  users: User[];
}

export interface CreateUserRequest {
  email: string;
  password: string;
  role: Role;
}

export interface PortMapping {
  ip: string;
  private: number;
  public: number;
  proto: string;
}

export interface ContainerCounts {
  running: number;
  total: number;
}

export interface HostSummary {
  id: string;
  name: string;
  hostname: string;
  os: string;
  kernel: string;
  arch: string;
  dockerVersion: string;
  agentVersion: string;
  connected: boolean;
  lastSeen: string | null;
  enrolledAt: string;
  containers: ContainerCounts;
}

export interface HostsResponse {
  hosts: HostSummary[];
}

export interface LogStats {
  linesIn: number;
  linesSkipped: number;
  eventsOut: number;
  redactionFailures: number;
  linesDropped: number;
}

export interface HostDetailResponse {
  host: HostSummary;
  logStats: LogStats | null;
}

export interface Container {
  id: string;
  name: string;
  stack: string;
  service: string;
  image: string;
  imageVersion: string;
  state: string;
  status: string;
  health: string;
  restartCount: number;
  oomKilled: boolean;
  exitCode: number;
  ports: PortMapping[];
  createdAt: string | null;
  startedAt: string | null;
}

export interface ContainersResponse {
  containers: Container[];
}

export interface MetricPoint {
  t: number;
  v: number;
  max: number;
}

export interface MetricsResponse {
  metric: string;
  resolution: string;
  points: MetricPoint[];
}

export type LogLevelFilter = "warn" | "error" | "info" | "all";

export interface LogEvent {
  id: string;
  containerId: string;
  templateId: string;
  level: string;
  sample: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
}

export interface LogsResponse {
  events: LogEvent[];
}

export interface EnrollmentRequest {
  name: string;
  ttlHours: number;
}

export interface EnrollmentResponse {
  token: string;
  expiresAt: string;
  command: string;
  host: string;
}

export type HostMetricName =
  | "cpu_pct"
  | "load1"
  | "mem_used"
  | "mem_total"
  | "disk_used"
  | "disk_total"
  | "net_rx_bps"
  | "net_tx_bps";

export type ContainerMetricName =
  | "cpu_pct"
  | "mem_usage"
  | "mem_limit"
  | "net_rx_bps"
  | "net_tx_bps"
  | "blk_read_bps"
  | "blk_write_bps"
  | "pids";

export type SourceKind = "git" | "inline";

export type DeployStatus =
  | "queued"
  | "fetching"
  | "building"
  | "starting"
  | "checking"
  | "healthy"
  | "failed"
  | "rolling_back"
  | "rolled_back"
  | "rollback_failed";

export type DeployTrigger = "manual" | "push" | "rollback";

export interface ProjectSource {
  kind: SourceKind;
  gitUrl?: string;
  gitRef?: string;
  composePath?: string;
  inlineCompose?: string;
}

export interface DeployBrief {
  id: string;
  status: DeployStatus;
  commit: string;
  trigger: string;
  startedAt: string | null;
  finishedAt: string | null;
  url: string;
  error: string;
}

export interface Project {
  id: string;
  hostId: string;
  name: string;
  source: ProjectSource;
  domain: string;
  service: string;
  port: number;
  autoDeploy: boolean;
  hasToken: boolean;
  createdAt: string;
  updatedAt?: string;
  /** Present when the list endpoint includes it; otherwise filled client-side. */
  lastDeploy?: DeployBrief | null;
}

export interface ProjectsResponse {
  projects: Project[];
}

export interface ProjectResponse {
  project: Project;
  inlineCompose?: string;
}

export interface CreateProjectRequest {
  name: string;
  source: {
    kind: SourceKind;
    gitUrl?: string;
    gitRef?: string;
    composePath?: string;
    gitToken?: string;
    inlineCompose?: string;
  };
  domain: string;
  service: string;
  port: number;
  autoDeploy: boolean;
}

export interface PatchProjectRequest {
  source?: {
    kind?: SourceKind;
    gitUrl?: string;
    gitRef?: string;
    composePath?: string;
    gitToken?: string;
    inlineCompose?: string;
  };
  domain?: string;
  service?: string;
  port?: number;
  autoDeploy?: boolean;
}

export interface Deploy {
  id: string;
  projectId: string;
  projectName: string;
  status: DeployStatus;
  commit: string;
  trigger: string;
  rollbackOf: string | null;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  url: string;
  error: string;
  message: string;
}

export interface DeploysResponse {
  deploys: Deploy[];
}

export interface DeployResponse {
  deploy: Deploy;
}

export interface DeployLogLine {
  seq: number;
  stream: string;
  text: string;
  withheld: boolean;
}

export interface DeployLogsResponse {
  logs: DeployLogLine[];
  done: boolean;
  status: DeployStatus;
}
