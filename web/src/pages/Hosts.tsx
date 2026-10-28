import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { SeveritySummary } from "../api/securityTypes";
import type { HostSummary } from "../api/types";
import { useAuth } from "../auth";
import { Badge, OnlineBadge } from "../components/Badge";
import { AddHostDialog } from "../components/AddHostDialog";
import { EmptyState } from "../components/EmptyState";
import { GettingStarted } from "../components/GettingStarted";
import { InfoTip } from "../components/InfoTip";
import { PageHeader } from "../components/PageHeader";
import { usePolling } from "../hooks/usePolling";
import { formatDateTime, formatRelativeTime } from "../lib/format";
import { INTROS } from "../lib/help";
import { hostSecurityBadgeText } from "../lib/security";

type HostSecBadge = { scanFailed: boolean; matchPending: number; summary: SeveritySummary };

function SecurityStateTip({ text }: { text: string }) {
  if (text === "scan failed") return <InfoTip term="scanFailed" />;
  if (text === "matching pending") return <InfoTip term="matchingPending" />;
  return null;
}

export function HostsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const [hosts, setHosts] = useState<HostSummary[] | null>(null);
  const [secBadges, setSecBadges] = useState<Record<string, HostSecBadge>>({});
  const [openIncidentCount, setOpenIncidentCount] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const mountedRef = useRef(true);
  const loadGenRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    const gen = ++loadGenRef.current;
    let list: HostSummary[] = [];
    try {
      const res = await api.hosts();
      if (gen !== loadGenRef.current || !mountedRef.current) return;
      list = res.hosts;
      setHosts(res.hosts);
      setError(null);
    } catch (err) {
      if (gen !== loadGenRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load hosts");
      return;
    }
    try {
      const incs = await api.incidents({ state: "open", limit: 100 });
      if (gen !== loadGenRef.current || !mountedRef.current) return;
      setOpenIncidentCount(incs.incidents.length);
    } catch {
      if (gen !== loadGenRef.current || !mountedRef.current) return;
      setOpenIncidentCount(0);
    }
    try {
      const pairs = await Promise.all(
        list.map(async (h) => {
          try {
            const sec = await api.hostSecurity(h.id);
            return [h.id, { scanFailed: !!sec.scanFailed, matchPending: sec.matchPending ?? 0, summary: sec.summary }] as const;
          } catch {
            return null;
          }
        }),
      );
      if (gen !== loadGenRef.current || !mountedRef.current) return;
      const next: Record<string, HostSecBadge> = {};
      for (const p of pairs) {
        if (p) next[p[0]] = p[1];
      }
      setSecBadges(next);
    } catch {
      // badges are best-effort
    }
  }, []);

  usePolling(load, 10_000, true);

  const openDialog = () => setDialogOpen(true);

  const closeDialog = () => {
    setDialogOpen(false);
    void load();
  };

  return (
    <div className="page">
      <PageHeader
        title="Hosts"
        intro={INTROS.hosts}
        meta={
          openIncidentCount > 0 ? (
            <Link to="/incidents" className="incident-count-badge">
              {openIncidentCount} open{" "}
              {openIncidentCount === 1 ? "incident" : "incidents"}
            </Link>
          ) : null
        }
        actions={
          isAdmin ? (
            <button type="button" className="btn" onClick={openDialog}>
              Add host
            </button>
          ) : null
        }
      />

      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}

      {isAdmin && hosts ? <GettingStarted hosts={hosts} onAddHost={openDialog} /> : null}

      {hosts && hosts.length === 0 ? (
        <EmptyState
          title="No hosts yet"
          action={
            isAdmin ? (
              <button type="button" className="btn" onClick={openDialog}>
                Add your first host
              </button>
            ) : null
          }
          doc={{ name: "enroll", label: "How enrolling works" }}
        >
          {isAdmin
            ? "Haetae needs a small agent on each server it watches. Add a host to get a one-time command to run on the server. The agent connects out to Haetae, so no inbound port is needed."
            : "Ask an admin to add the first host."}
        </EmptyState>
      ) : null}

      {hosts && hosts.length > 0 ? (
        <>
          <div className="hosts-table table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Status</th>
                  <th>
                    Security <InfoTip term="securityBadge" />
                  </th>
                  <th>OS</th>
                  <th>Containers</th>
                  <th>Last seen</th>
                  <th>Agent</th>
                </tr>
              </thead>
              <tbody>
                {hosts.map((h) => {
                  const badge = secBadges[h.id];
                  const badgeText = hostSecurityBadgeText(
                    badge?.scanFailed,
                    badge?.summary ?? null,
                    badge?.matchPending,
                  );
                  return (
                  <tr key={h.id}>
                    <td>
                      <Link to={`/hosts/${h.id}`}>{h.name}</Link>
                    </td>
                    <td>
                      <OnlineBadge online={h.connected} />
                    </td>
                    <td>
                      {badgeText ? (
                        <span className="row badge-row">
                          <Link to={`/hosts/${h.id}?tab=security`}>
                            <Badge variant="danger">{badgeText}</Badge>
                          </Link>
                          <SecurityStateTip text={badgeText} />
                        </span>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                    <td>{h.os}</td>
                    <td>
                      {h.containers.running}/{h.containers.total}
                    </td>
                    <td title={formatDateTime(h.lastSeen)}>
                      {formatRelativeTime(h.lastSeen)}
                    </td>
                    <td>{h.agentVersion}</td>
                  </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <div className="host-cards">
            {hosts.map((h) => {
              const badge = secBadges[h.id];
              const badgeText = hostSecurityBadgeText(
                badge?.scanFailed,
                badge?.summary ?? null,
                badge?.matchPending,
              );
              return (
                <div key={h.id} className="host-card">
                  <Link to={`/hosts/${h.id}`}>
                    <div className="row" style={{ justifyContent: "space-between" }}>
                      <strong>{h.name}</strong>
                      <OnlineBadge online={h.connected} />
                    </div>
                    <div className="muted">
                      {h.os} · {h.containers.running}/{h.containers.total} containers
                    </div>
                    <div className="muted" title={formatDateTime(h.lastSeen)}>
                      Last seen {formatRelativeTime(h.lastSeen)} · agent {h.agentVersion}
                    </div>
                  </Link>
                  {badgeText ? (
                    <div className="row badge-row" style={{ marginTop: "0.35rem" }}>
                      <Link to={`/hosts/${h.id}?tab=security`}>
                        <Badge variant="danger">{badgeText}</Badge>
                      </Link>
                      <SecurityStateTip text={badgeText} />
                    </div>
                  ) : null}
                </div>
              );
            })}
          </div>
        </>
      ) : null}

      {!hosts && !error ? <p className="muted">Loading hosts…</p> : null}

      <AddHostDialog open={dialogOpen} onClose={closeDialog} />
    </div>
  );
}
