import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { AlertDetail, IncidentSummary } from "../api/m6Types";
import { useAuth } from "../auth";
import { EmptyState } from "../components/EmptyState";
import { InfoTip } from "../components/InfoTip";
import { PageHeader } from "../components/PageHeader";
import { SeverityBadge } from "../components/SeverityBadge";
import { evidenceToText } from "../lib/evidenceText";
import { formatDateTime, formatRelativeTime } from "../lib/format";
import { INTROS } from "../lib/help";

export function IncidentDetailPage() {
  const { id = "" } = useParams();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  const [incident, setIncident] = useState<IncidentSummary | null>(null);
  const [alerts, setAlerts] = useState<AlertDetail[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [muteDays, setMuteDays] = useState(30);
  const [muteMsg, setMuteMsg] = useState<string | null>(null);
  const [muteError, setMuteError] = useState<string | null>(null);
  const [muteSubmitting, setMuteSubmitting] = useState(false);
  const mountedRef = useRef(true);
  const genRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    if (!id) return;
    const gen = ++genRef.current;
    try {
      const res = await api.incident(id);
      if (gen !== genRef.current || !mountedRef.current) return;
      setIncident(res.incident);
      setAlerts(res.alerts);
      setError(null);
    } catch (err) {
      if (gen !== genRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load incident");
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  const onMute = async (e: FormEvent) => {
    e.preventDefault();
    if (!incident) return;
    setMuteError(null);
    setMuteMsg(null);
    setMuteSubmitting(true);
    try {
      await api.createMute({ hostId: incident.hostId, days: muteDays });
      setMuteMsg(`Muted this host for ${muteDays} days.`);
    } catch (err) {
      setMuteError(err instanceof ApiError ? err.message : "Failed to create mute");
    } finally {
      setMuteSubmitting(false);
    }
  };

  return (
    <div className="page">
      <p>
        <Link to="/incidents">← Incidents</Link>
      </p>

      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}

      {incident ? (
        <>
          <PageHeader
            title={incident.title}
            intro={INTROS.incidentDetail}
            meta={
              <>
                <SeverityBadge severity={incident.severity} />
                <InfoTip term="severity" />
              </>
            }
          />
          <p className="muted">
            Host{" "}
            <Link to={`/hosts/${incident.hostId}`}>{incident.hostName || incident.hostId}</Link>
            {" · "}
            {incident.state}
            {" · opened "}
            <span title={formatDateTime(incident.openedAt)}>
              {formatRelativeTime(incident.openedAt)}
            </span>
            {incident.resolvedAt ? (
              <>
                {" · resolved "}
                <span title={formatDateTime(incident.resolvedAt)}>
                  {formatRelativeTime(incident.resolvedAt)}
                </span>
              </>
            ) : null}
          </p>

          {isAdmin && incident.state === "open" ? (
            <section className="card" style={{ marginBottom: "1.5rem" }}>
              <h2>Mute host</h2>
              <p className="muted">
                A mute stops notifications for this host for a while. Its alerts are still
                recorded and shown.
              </p>
              <form onSubmit={(e) => void onMute(e)} className="row">
                <div className="field" style={{ marginBottom: 0 }}>
                  <label htmlFor="mute-days">Days</label>
                  <input
                    id="mute-days"
                    name="days"
                    type="number"
                    min={1}
                    max={90}
                    value={muteDays}
                    onChange={(e) => setMuteDays(Number(e.target.value))}
                    required
                  />
                </div>
                <button type="submit" className="btn" disabled={muteSubmitting}>
                  {muteSubmitting ? "Muting…" : `Mute this host for ${muteDays} days`}
                </button>
              </form>
              {muteError ? (
                <div className="app-error" role="alert" aria-live="assertive">
                  {muteError}
                </div>
              ) : null}
              {muteMsg ? (
                <p role="status" aria-live="polite">
                  {muteMsg}
                </p>
              ) : null}
            </section>
          ) : null}

          <div className="row heading-row">
            <h2>Alerts</h2>
            <InfoTip term="alert" />
          </div>
          {alerts.length === 0 ? (
            <EmptyState title="No alerts on this incident">
              Alerts that belong to this incident are listed here.
            </EmptyState>
          ) : (
            <div className="stack">
              {alerts.map((a) => {
                const evidenceText = evidenceToText(a.evidence);
                return (
                  <article key={a.id} className="card">
                    <div className="row" style={{ justifyContent: "space-between" }}>
                      <strong>{a.summary || a.rule}</strong>
                      <SeverityBadge severity={a.severity} />
                    </div>
                    <p className="muted">
                      {a.rule}
                      {a.container ? ` · ${a.container}` : ""}
                      {" · "}
                      {a.state}
                      {" · first "}
                      <span title={formatDateTime(a.firstAt)}>{formatRelativeTime(a.firstAt)}</span>
                      {" · last "}
                      <span title={formatDateTime(a.lastAt)}>{formatRelativeTime(a.lastAt)}</span>
                    </p>
                    {evidenceText ? (
                      <pre className="mono evidence-block">{evidenceText}</pre>
                    ) : null}
                  </article>
                );
              })}
            </div>
          )}
        </>
      ) : !error ? (
        <p className="muted">Loading…</p>
      ) : null}
    </div>
  );
}
