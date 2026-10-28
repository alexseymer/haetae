import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, api } from "../api/client";
import type {
  ContainerVuln,
  HostImageUpdatesResponse,
  HostOutboundResponse,
  HostSecurityResponse,
  HostVulnerabilitiesResponse,
  MisconfigFinding,
} from "../api/securityTypes";
import { useAuth } from "../auth";
import { Badge } from "../components/Badge";
import { EmptyState } from "../components/EmptyState";
import { InfoTip } from "../components/InfoTip";
import { SeverityBadge } from "../components/SeverityBadge";
import { usePolling } from "../hooks/usePolling";
import { formatDateTime, formatRelativeTime } from "../lib/format";
import { INTROS } from "../lib/help";
import {
  containerScanStatusLine,
  imageUpdateDecisionLabel,
  isScanFailedStatus,
  misconfigKindLabel,
  vulnIdHref,
} from "../lib/security";

interface HostSecurityTabProps {
  hostId: string;
  active: boolean;
}

export function HostSecurityTab({ hostId, active }: HostSecurityTabProps) {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const [security, setSecurity] = useState<HostSecurityResponse | null>(null);
  const [vulns, setVulns] = useState<HostVulnerabilitiesResponse | null>(null);
  const [outbound, setOutbound] = useState<HostOutboundResponse | null>(null);
  const [updates, setUpdates] = useState<HostImageUpdatesResponse | null>(null);
  const [autoPull, setAutoPull] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [pendingScan, setPendingScan] = useState<Record<string, boolean>>({});
  const [scanMsg, setScanMsg] = useState<Record<string, string>>({});
  const [autoPullBusy, setAutoPullBusy] = useState(false);
  const mountedRef = useRef(true);
  const genRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    if (!hostId) return;
    const gen = ++genRef.current;
    try {
      const [sec, v, out, ups, settings] = await Promise.all([
        api.hostSecurity(hostId),
        api.hostVulnerabilities(hostId),
        api.hostOutbound(hostId, 200),
        api.hostImageUpdates(hostId),
        api.updateSettings(),
      ]);
      if (gen !== genRef.current || !mountedRef.current) return;
      setSecurity(sec);
      setVulns(v);
      setOutbound(out);
      setUpdates(ups);
      setAutoPull(settings.autoPull);
      setError(null);
    } catch (err) {
      if (gen !== genRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load security");
    }
  }, [hostId]);

  usePolling(load, 15_000, active && !!hostId, hostId);

  const onScan = async (cid: string) => {
    setPendingScan((p) => ({ ...p, [cid]: true }));
    setScanMsg((m) => ({ ...m, [cid]: "" }));
    try {
      await api.forceContainerScan(hostId, cid);
      setScanMsg((m) => ({ ...m, [cid]: "Scan requested" }));
      void load();
    } catch (err) {
      setScanMsg((m) => ({
        ...m,
        [cid]: err instanceof ApiError ? err.message : "Scan failed to start",
      }));
    } finally {
      setPendingScan((p) => ({ ...p, [cid]: false }));
    }
  };

  const onAutoPull = async (next: boolean) => {
    setAutoPullBusy(true);
    try {
      const res = await api.putUpdateSettings({ autoPull: next });
      if (!mountedRef.current) return;
      setAutoPull(res.autoPull);
    } catch (err) {
      if (!mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to update setting");
    } finally {
      if (mountedRef.current) setAutoPullBusy(false);
    }
  };

  if (!active) return null;

  const misByContainer = groupMisconfigs(security?.misconfigs ?? []);
  const conntrack = security?.conntrack;
  const degraded =
    conntrack &&
    (conntrack.state === "degraded" || conntrack.state === "unavailable");

  return (
    <div
      className="stack"
      role="tabpanel"
      id="panel-security"
      aria-labelledby="tab-security"
    >
      <p className="tab-intro">{INTROS.hostSecurity}</p>
      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}

      <section className="stack" aria-labelledby="sec-vulns-heading">
        <div className="row heading-row">
          <h2 id="sec-vulns-heading">Vulnerabilities</h2>
          <InfoTip term="severity" />
        </div>
        {vulns && !vulns.db.configured ? (
          <p className="muted" role="status">
            Vulnerability database not configured
          </p>
        ) : null}
        {vulns?.db.status === "failed" ? (
          <p className="muted" role="status">
            Vulnerability database update failed
            {vulns.db.error ? `: ${vulns.db.error}` : ""}
          </p>
        ) : null}
        {!vulns ? (
          <p className="muted">Loading…</p>
        ) : vulns.containers.length === 0 ? (
          <EmptyState title="No running containers to scan">
            Haetae scans the software in running containers. Start a container or deploy an
            app and the first scan runs on its own.
          </EmptyState>
        ) : (
          vulns.containers.map((c) => (
            <VulnCard
              key={c.containerId}
              c={c}
              isAdmin={isAdmin}
              expanded={!!expanded[c.containerId]}
              pending={!!pendingScan[c.containerId]}
              message={scanMsg[c.containerId] ?? ""}
              onToggle={() =>
                setExpanded((e) => ({
                  ...e,
                  [c.containerId]: !e[c.containerId],
                }))
              }
              onScan={() => void onScan(c.containerId)}
            />
          ))
        )}
      </section>

      <section className="stack" aria-labelledby="sec-mis-heading">
        <h2 id="sec-mis-heading">Misconfigurations</h2>
        {!security ? (
          <p className="muted">Loading…</p>
        ) : Object.keys(misByContainer).length === 0 ? (
          <EmptyState title="No misconfigurations found">
            Haetae checks for risky settings such as published ports, a mounted Docker socket,
            running as root and expiring certificates. None are open right now.
          </EmptyState>
        ) : (
          Object.entries(misByContainer).map(([name, items]) => (
            <div key={name} className="stack" style={{ gap: "0.35rem" }}>
              <h3 style={{ margin: 0, fontSize: "1rem" }}>{name || "(host)"}</h3>
              <ul className="stack" style={{ listStyle: "none", padding: 0, margin: 0, gap: "0.35rem" }}>
                {items.map((m, i) => (
                  <li key={`${m.kind}-${m.detail}-${i}`} className="row" style={{ gap: "0.5rem" }}>
                    <SeverityBadge severity={m.severity} />
                    <span>
                      {misconfigKindLabel(m.kind)}
                      {m.detail ? `: ${m.detail}` : ""}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          ))
        )}
      </section>

      <section className="stack" aria-labelledby="sec-out-heading">
        <h2 id="sec-out-heading">Outbound connections</h2>
        {degraded ? (
          <div className="app-error" role="status" aria-live="polite">
            Conntrack {conntrack.state}
            {conntrack.reason ? `: ${conntrack.reason}` : ""} <InfoTip term="degraded" />
          </div>
        ) : null}
        {conntrack && conntrack.notMonitored.length > 0 ? (
          <p className="muted" role="status">
            Not monitored (host network): {conntrack.notMonitored.join(", ")}{" "}
            <InfoTip term="notMonitored" />
          </p>
        ) : null}
        {!outbound ? (
          <p className="muted">Loading…</p>
        ) : outbound.conns.length === 0 ? (
          <EmptyState title="No outbound connections recorded yet">
            Connections made by your containers to other servers are listed here as they happen.
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Container</th>
                  <th>Destination</th>
                  <th>Port</th>
                  <th>Proto</th>
                  <th>Count</th>
                  <th>Last seen</th>
                </tr>
              </thead>
              <tbody>
                {outbound.conns.map((c) => (
                  <tr key={`${c.containerId}-${c.dstIp}-${c.dstPort}-${c.proto}`}>
                    <td>{c.containerName || c.containerId}</td>
                    <td>{c.dstIp}</td>
                    <td>{c.dstPort}</td>
                    <td>{c.proto}</td>
                    <td>{c.count}</td>
                    <td title={formatDateTime(c.lastSeen)}>
                      {formatRelativeTime(c.lastSeen)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="stack" aria-labelledby="sec-img-heading">
        <div className="row heading-row">
          <h2 id="sec-img-heading">Image updates</h2>
          <InfoTip term="autoPull" />
        </div>
        {isAdmin ? (
          <label className="row" style={{ gap: "0.5rem" }}>
            <input
              type="checkbox"
              checked={autoPull}
              disabled={autoPullBusy}
              onChange={(e) => void onAutoPull(e.target.checked)}
            />
            <span>
              Automatically pull eligible image updates (never restarts
              containers; waits 48 hours unless a verified critical CVE fix)
            </span>
          </label>
        ) : (
          <p className="muted">
            Auto-pull is {autoPull ? "on" : "off"} (admins can change this).
          </p>
        )}
        {!updates ? (
          <p className="muted">Loading…</p>
        ) : updates.updates.length === 0 ? (
          <EmptyState title="No image update decisions yet">
            Once a running image has been checked against its registry, the decision appears here.
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Container</th>
                  <th>Image</th>
                  <th>Decision</th>
                  <th>Decided</th>
                </tr>
              </thead>
              <tbody>
                {updates.updates.map((u) => (
                  <tr key={u.containerId}>
                    <td>{u.containerName || u.containerId}</td>
                    <td>{u.image}</td>
                    <td>{imageUpdateDecisionLabel(u.decision)}</td>
                    <td title={formatDateTime(u.decidedAt)}>
                      {formatRelativeTime(u.decidedAt)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}

function VulnCard({
  c,
  isAdmin,
  expanded,
  pending,
  message,
  onToggle,
  onScan,
}: {
  c: ContainerVuln;
  isAdmin: boolean;
  expanded: boolean;
  pending: boolean;
  message: string;
  onToggle: () => void;
  onScan: () => void;
}) {
  const statusLine = containerScanStatusLine(c, formatRelativeTime, formatDateTime);
  const failed = isScanFailedStatus(c.scan.status) || c.match.status === "failed";
  const showCounts = c.scan.status === "ok" && c.match.status === "ok";

  return (
    <div className="stack" style={{ gap: "0.35rem", borderBottom: "1px solid var(--color-border, #ddd)", paddingBottom: "0.75rem" }}>
      <div className="row" style={{ justifyContent: "space-between", flexWrap: "wrap" }}>
        <div className="row" style={{ gap: "0.5rem", flexWrap: "wrap" }}>
          <strong>{c.name || c.containerId}</strong>
          <span className="muted">{c.image}</span>
          {showCounts ? (
            <>
              <Badge variant="danger">{`critical ${c.counts.critical}`}</Badge>
              <Badge variant="danger">{`high ${c.counts.high}`}</Badge>
              <Badge variant="warn">{`medium ${c.counts.medium}`}</Badge>
              <Badge variant="info">{`low ${c.counts.low}`}</Badge>
            </>
          ) : null}
        </div>
        {isAdmin ? (
          <button
            type="button"
            className="btn btn-secondary"
            disabled={pending || c.scan.status === "pending"}
            onClick={onScan}
          >
            {pending ? "Scanning…" : "Scan now"}
          </button>
        ) : null}
      </div>
      <p className={failed ? "app-error" : "muted"} role="status" style={{ margin: 0 }}>
        {statusLine}
      </p>
      {failed ? <InfoTip term="scanFailed" /> : null}
      {c.scan.status === "ok" && c.match.status !== "ok" && !failed ? (
        <InfoTip term="matchingPending" />
      ) : null}
      {showCounts && c.findings.length === 0 ? (
        <p className="muted" style={{ margin: 0 }}>
          No known vulnerabilities found in the last completed scan.
        </p>
      ) : null}
      {message ? (
        <p className="muted" role="status" style={{ margin: 0 }}>
          {message}
        </p>
      ) : null}
      {showCounts && c.findings.length > 0 ? (
        <>
          <button type="button" className="btn btn-secondary" onClick={onToggle}>
            {expanded ? "Hide findings" : `Show findings (${c.findings.length})`}
          </button>
          {expanded ? <FindingsTable findings={c.findings} /> : null}
        </>
      ) : null}
    </div>
  );
}

function FindingsTable({
  findings,
}: {
  findings: ContainerVuln["findings"];
}) {
  return (
    <div className="table-wrap">
      <table className="data">
        <thead>
          <tr>
            <th>CVE</th>
            <th>Severity</th>
            <th>Package</th>
            <th>Version</th>
            <th>Fixed</th>
            <th>Description</th>
          </tr>
        </thead>
        <tbody>
          {findings.map((f) => {
            const href = vulnIdHref(f.vulnId, f.urls);
            return (
              <tr key={`${f.vulnId}-${f.package}-${f.version}`}>
                <td>
                  {href ? (
                    <a href={href} rel="noopener noreferrer" target="_blank">
                      {f.vulnId}
                    </a>
                  ) : (
                    f.vulnId
                  )}
                </td>
                <td>
                  <SeverityBadge severity={f.severity} />
                </td>
                <td>{f.package}</td>
                <td>{f.version}</td>
                <td>{f.fixedVersion || "—"}</td>
                <td>{f.description}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function groupMisconfigs(ms: MisconfigFinding[]): Record<string, MisconfigFinding[]> {
  const out: Record<string, MisconfigFinding[]> = {};
  for (const m of ms) {
    const key = m.containerName || m.containerId || "(host)";
    (out[key] ??= []).push(m);
  }
  return out;
}
