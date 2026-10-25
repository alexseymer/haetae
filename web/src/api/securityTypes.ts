export type ScanStatus =
  | "ok"
  | "failed"
  | "timeout"
  | "oom"
  | "unsupported"
  | "pending"
  | "none";

export type MatchStatus = "ok" | "failed" | "none";

export interface SeveritySummary {
  critical: number;
  high: number;
  medium: number;
  low: number;
}

export interface ConntrackState {
  state: string;
  reason: string;
  notMonitored: string[];
}

export interface MisconfigFinding {
  containerId: string;
  containerName: string;
  kind: string;
  severity: string;
  detail: string;
  firstSeen: string;
  lastSeen: string;
}

export interface HostSecurityResponse {
  conntrack: ConntrackState;
  misconfigs: MisconfigFinding[];
  summary: SeveritySummary;
  scanFailed?: boolean;
}

export interface VulnFinding {
  vulnId: string;
  package: string;
  version: string;
  fixedVersion: string;
  severity: string;
  description: string;
  urls: string[];
}

export interface ContainerVuln {
  containerId: string;
  name: string;
  image: string;
  imageId: string;
  scan: {
    status: ScanStatus;
    error: string;
    at: string | null;
    tool: string;
  };
  match: {
    status: MatchStatus;
    error: string;
    dbBuiltAt: string | null;
  };
  counts: SeveritySummary & { unknown: number };
  findings: VulnFinding[];
}

export interface VulnDBState {
  status: string;
  builtAt: string | null;
  checkedAt: string | null;
  error: string;
  configured: boolean;
}

export interface HostVulnerabilitiesResponse {
  containers: ContainerVuln[];
  db: VulnDBState;
}

export interface OutboundConn {
  containerId: string;
  containerName: string;
  dstIp: string;
  dstPort: number;
  proto: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
}

export interface HostOutboundResponse {
  conns: OutboundConn[];
}

export interface ImageUpdate {
  containerId: string;
  containerName: string;
  image: string;
  runningDigest: string;
  candidateDigest: string;
  candidateCreated: string | null;
  decision: string;
  reason: string;
  decidedAt: string;
}

export interface HostImageUpdatesResponse {
  updates: ImageUpdate[];
}

export interface UpdateSettings {
  autoPull: boolean;
}
