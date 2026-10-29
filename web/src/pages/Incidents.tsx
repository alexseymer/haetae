import { useCallback, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { Link } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { IncidentState, IncidentSummary } from "../api/m6Types";
import { AnimatedNumber } from "../components/AnimatedNumber";
import { EmptyState } from "../components/EmptyState";
import { Icon } from "../components/icons";
import { InfoTip } from "../components/InfoTip";
import { PageHeader } from "../components/PageHeader";
import { Loading } from "../components/Skeleton";
import { SeverityBadge } from "../components/SeverityBadge";
import { usePolling } from "../hooks/usePolling";
import { formatDateTime, formatRelativeTime } from "../lib/format";
import { INTROS } from "../lib/help";
import { staggerIndex } from "../lib/motion";

type ListTab = IncidentState;

const TABS: Array<{ id: ListTab; label: string }> = [
  { id: "open", label: "Open" },
  { id: "resolved", label: "Resolved" },
];

export function IncidentsPage() {
  const [tab, setTab] = useState<ListTab>("open");
  const [incidents, setIncidents] = useState<IncidentSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const mountedRef = useRef(true);
  const genRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    const gen = ++genRef.current;
    try {
      const res = await api.incidents({ state: tab, limit: 100 });
      if (gen !== genRef.current || !mountedRef.current) return;
      setIncidents(res.incidents);
      setError(null);
    } catch (err) {
      if (gen !== genRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load incidents");
    }
  }, [tab]);

  usePolling(load, 15_000, true, tab);

  const onTabListKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const idx = TABS.findIndex((t) => t.id === tab);
    if (idx < 0) return;
    let next = idx;
    if (e.key === "ArrowRight") {
      next = (idx + 1) % TABS.length;
    } else if (e.key === "ArrowLeft") {
      next = (idx - 1 + TABS.length) % TABS.length;
    } else if (e.key === "Home") {
      next = 0;
    } else if (e.key === "End") {
      next = TABS.length - 1;
    } else {
      return;
    }
    e.preventDefault();
    const nextTab = TABS[next]!;
    setTab(nextTab.id);
    const btn = e.currentTarget.querySelector<HTMLElement>(`#tab-incidents-${nextTab.id}`);
    btn?.focus();
  };

  return (
    <div className="page">
      <PageHeader title="Incidents" icon="incidents" intro={INTROS.incidents} />

      <div
        className="tabs"
        role="tablist"
        aria-label="Incident lists"
        onKeyDown={onTabListKeyDown}
      >
        {TABS.map(({ id: idTab, label }) => (
          <button
            key={idTab}
            id={`tab-incidents-${idTab}`}
            type="button"
            role="tab"
            className="tab"
            aria-selected={tab === idTab}
            aria-controls={`panel-incidents-${idTab}`}
            tabIndex={tab === idTab ? 0 : -1}
            onClick={() => setTab(idTab)}
          >
            {label}
          </button>
        ))}
      </div>

      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}

      <div
        role="tabpanel"
        id={`panel-incidents-${tab}`}
        aria-labelledby={`tab-incidents-${tab}`}
      >
        {incidents && incidents.length === 0 ? (
          tab === "open" ? (
            <EmptyState art="calm" title="All quiet.">
              Incidents appear here when something needs attention.
            </EmptyState>
          ) : (
            <EmptyState art="calm" title="No resolved incidents yet.">
              Resolved incidents stay here as history.
            </EmptyState>
          )
        ) : null}

        {incidents && incidents.length > 0 ? (
          <div className="summary-grid">
            <div className={`summary-tile stagger-item${tab === "open" ? " summary-tile-alert" : ""}`} data-stagger={staggerIndex(0)}>
              <span className="summary-icon">
                {tab === "open" ? <span className="pulse-ring" aria-hidden="true" /> : <Icon name="check" size={20} />}
              </span>
              <div>
                <div className="summary-value">
                  <AnimatedNumber value={incidents.length} />
                </div>
                <div className="summary-label">{tab === "open" ? "Open now" : "Resolved"}</div>
              </div>
            </div>
            <div className="summary-tile stagger-item" data-stagger={staggerIndex(1)}>
              <span className="summary-icon">
                <Icon name="alert" size={20} />
              </span>
              <div>
                <div className="summary-value">
                  <AnimatedNumber
                    value={incidents.filter((i) => i.severity === "critical" || i.severity === "high").length}
                  />
                </div>
                <div className="summary-label">Critical or high</div>
              </div>
            </div>
            <div className="summary-tile stagger-item" data-stagger={staggerIndex(2)}>
              <span className="summary-icon">
                <Icon name="hosts" size={20} />
              </span>
              <div>
                <div className="summary-value">
                  <AnimatedNumber value={new Set(incidents.map((i) => i.hostId)).size} />
                </div>
                <div className="summary-label">Hosts affected</div>
              </div>
            </div>
          </div>
        ) : null}

        {incidents && incidents.length > 0 ? (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>
                    Severity <InfoTip term="severity" />
                  </th>
                  <th>Host</th>
                  <th>Title</th>
                  <th>Updated</th>
                  <th>
                    Alerts <InfoTip term="alert" />
                  </th>
                </tr>
              </thead>
              <tbody>
                {incidents.map((inc, i) => (
                  <tr key={inc.id} className="stagger-item" data-stagger={staggerIndex(i)}>
                    <td>
                      <SeverityBadge severity={inc.severity} />
                    </td>
                    <td>
                      <Link to={`/hosts/${inc.hostId}`}>{inc.hostName || inc.hostId}</Link>
                    </td>
                    <td>
                      <Link to={`/incidents/${inc.id}`}>{inc.title}</Link>
                    </td>
                    <td title={formatDateTime(inc.updatedAt)}>
                      {formatRelativeTime(inc.updatedAt)}
                    </td>
                    <td>{inc.alertCount}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}

        {!incidents && !error ? <Loading label="Loading incidents…" variant="table" /> : null}
      </div>
    </div>
  );
}
