import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { EnrollmentResponse, HostSummary } from "../api/types";
import { useAuth } from "../auth";
import { OnlineBadge } from "../components/Badge";
import { CopyBlock } from "../components/CopyBlock";
import { Modal } from "../components/Modal";
import { usePolling } from "../hooks/usePolling";
import { formatDateTime, formatRelativeTime } from "../lib/format";

export function HostsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const [hosts, setHosts] = useState<HostSummary[] | null>(null);
  const [openIncidentCount, setOpenIncidentCount] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [created, setCreated] = useState<EnrollmentResponse | null>(null);
  const [name, setName] = useState("");
  const [ttlHours, setTtlHours] = useState(24);
  const [formError, setFormError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
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
    try {
      const res = await api.hosts();
      if (gen !== loadGenRef.current || !mountedRef.current) return;
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
  }, []);

  usePolling(load, 10_000, true);

  const openDialog = () => {
    setCreated(null);
    setName("");
    setTtlHours(24);
    setFormError(null);
    setDialogOpen(true);
  };

  const closeDialog = () => {
    setDialogOpen(false);
    void load();
  };

  const onCreate = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setSubmitting(true);
    try {
      const res = await api.enroll({ name, ttlHours });
      setCreated(res);
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : "Enrollment failed");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="page">
      <div className="row" style={{ justifyContent: "space-between", marginBottom: "1rem" }}>
        <div className="row">
          <h1 style={{ margin: 0 }}>Hosts</h1>
          {openIncidentCount > 0 ? (
            <Link to="/incidents" className="incident-count-badge">
              {openIncidentCount} open{" "}
              {openIncidentCount === 1 ? "incident" : "incidents"}
            </Link>
          ) : null}
        </div>
        {isAdmin ? (
          <button type="button" className="btn" onClick={openDialog}>
            Add host
          </button>
        ) : null}
      </div>

      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}

      {hosts && hosts.length === 0 ? (
        <div className="empty-state">
          <p>No hosts yet.</p>
          <p>
            {isAdmin
              ? 'Click "Add host" to create a one-time enrollment token, then run the command on the server you want to manage.'
              : "Ask an admin to enroll the first host."}
          </p>
        </div>
      ) : null}

      {hosts && hosts.length > 0 ? (
        <>
          <div className="hosts-table table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Status</th>
                  <th>OS</th>
                  <th>Containers</th>
                  <th>Last seen</th>
                  <th>Agent</th>
                </tr>
              </thead>
              <tbody>
                {hosts.map((h) => (
                  <tr key={h.id}>
                    <td>
                      <Link to={`/hosts/${h.id}`}>{h.name}</Link>
                    </td>
                    <td>
                      <OnlineBadge online={h.connected} />
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
                ))}
              </tbody>
            </table>
          </div>
          <div className="host-cards">
            {hosts.map((h) => (
              <Link key={h.id} to={`/hosts/${h.id}`} className="host-card">
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
            ))}
          </div>
        </>
      ) : null}

      {!hosts && !error ? <p className="muted">Loading hosts…</p> : null}

      <Modal title="Add host" open={dialogOpen} onClose={closeDialog}>
        {created ? (
          <div className="stack">
            <p>
              Run this on the server. The token works once.
            </p>
            <CopyBlock text={created.command} />
            <p className="muted">
              Expires {formatDateTime(created.expiresAt)}
            </p>
            <button type="button" className="btn" onClick={closeDialog}>
              Done
            </button>
          </div>
        ) : (
          <form onSubmit={(e) => void onCreate(e)}>
            <div className="field">
              <label htmlFor="host-name">Host name</label>
              <input
                id="host-name"
                name="name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
              />
            </div>
            <div className="field">
              <label htmlFor="ttl-hours">Validity (hours)</label>
              <input
                id="ttl-hours"
                name="ttlHours"
                type="number"
                min={1}
                max={168}
                value={ttlHours}
                onChange={(e) => setTtlHours(Number(e.target.value))}
                required
              />
            </div>
            {formError ? (
              <div className="app-error" role="alert" aria-live="assertive">
                {formError}
              </div>
            ) : null}
            <div className="row">
              <button type="submit" className="btn" disabled={submitting}>
                {submitting ? "Creating…" : "Create token"}
              </button>
              <button type="button" className="btn btn-secondary" onClick={closeDialog}>
                Cancel
              </button>
            </div>
          </form>
        )}
      </Modal>
    </div>
  );
}
