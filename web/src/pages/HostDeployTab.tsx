import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { CreateProjectRequest, Project } from "../api/types";
import { useAuth } from "../auth";
import { Badge } from "../components/Badge";
import { EmptyState } from "../components/EmptyState";
import { ProjectForm } from "../components/ProjectForm";
import {
  deployStatusLabel,
  deployStatusVariant,
  domainHref,
  shortSha,
  sourceSummary,
  toDeployBrief,
} from "../lib/deploy";
import { formatRelativeTime } from "../lib/format";
import { INTROS } from "../lib/help";

interface HostDeployTabProps {
  hostId: string;
  active: boolean;
}

export function HostDeployTab({ hostId, active }: HostDeployTabProps) {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [formOpen, setFormOpen] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
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
      const res = await api.projects(hostId);
      if (gen !== genRef.current || !mountedRef.current) return;
      const enriched = await Promise.all(
        res.projects.map(async (p) => {
          if (p.lastDeploy !== undefined) return p;
          try {
            const deps = await api.projectDeploys(p.id);
            const first = deps.deploys[0];
            return { ...p, lastDeploy: first ? toDeployBrief(first) : null };
          } catch {
            return { ...p, lastDeploy: null };
          }
        }),
      );
      if (gen !== genRef.current || !mountedRef.current) return;
      setProjects(enriched);
      setError(null);
    } catch (err) {
      if (gen !== genRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load projects");
    }
  }, [hostId]);

  useEffect(() => {
    if (active) void load();
  }, [active, load]);

  const onCreate = async (payload: { create?: CreateProjectRequest }) => {
    if (!payload.create) return;
    setSubmitting(true);
    setFormError(null);
    try {
      await api.createProject(hostId, payload.create);
      setFormOpen(false);
      await load();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : "Create failed");
    } finally {
      setSubmitting(false);
    }
  };

  const openForm = () => {
    setFormError(null);
    setFormOpen(true);
  };

  if (!active) return null;

  return (
    <div
      className="stack"
      role="tabpanel"
      id="panel-deploy"
      aria-labelledby="tab-deploy"
    >
      <p className="tab-intro">{INTROS.hostDeploy}</p>

      {formOpen ? (
        <ProjectForm
          mode="create"
          serverError={formError}
          submitting={submitting}
          onClose={() => setFormOpen(false)}
          onSubmit={onCreate}
        />
      ) : (
        <div className="row" style={{ justifyContent: "space-between" }}>
          <h2 style={{ margin: 0 }}>Projects</h2>
          {isAdmin ? (
            <button type="button" className="btn" onClick={openForm}>
              New project
            </button>
          ) : null}
        </div>
      )}

      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}

      {!formOpen && projects && projects.length === 0 ? (
        <EmptyState
          title="Deploy your first app"
          action={
            isAdmin ? (
              <button type="button" className="btn" onClick={openForm}>
                Create your first project
              </button>
            ) : null
          }
          doc={{ name: "deploy", label: "How deploys work" }}
        >
          {isAdmin
            ? "A project is a Docker Compose app on this host. Point it at a Git repository or paste a compose file, then press Deploy. Deploys run with the privileges the compose file asks for, which is the same as root on the server."
            : "A project is a Docker Compose app on this host. Only admins can deploy."}
        </EmptyState>
      ) : null}

      {!formOpen && projects && projects.length > 0 ? (
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Name</th>
                <th>Source</th>
                <th>Domain</th>
                <th>Auto-deploy</th>
                <th>Last deploy</th>
              </tr>
            </thead>
            <tbody>
              {projects.map((p) => {
                const href = domainHref(p.domain);
                const last = p.lastDeploy;
                return (
                  <tr key={p.id}>
                    <td>
                      <Link to={`/projects/${p.id}`}>{p.name}</Link>
                    </td>
                    <td className="mono">{sourceSummary(p.source)}</td>
                    <td>
                      {href ? (
                        <a href={href} rel="noopener noreferrer" target="_blank">
                          {p.domain}
                        </a>
                      ) : (
                        p.domain || "—"
                      )}
                    </td>
                    <td>
                      {p.autoDeploy ? (
                        <Badge variant="info">Auto-deploy</Badge>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td>
                      {last ? (
                        <span className="row" style={{ gap: "0.5rem" }}>
                          <Badge variant={deployStatusVariant(last.status)}>
                            {deployStatusLabel(last.status)}
                          </Badge>
                          <span
                            className="muted"
                            title={last.startedAt ?? undefined}
                          >
                            {formatRelativeTime(last.startedAt)}
                          </span>
                          <span className="mono muted">{shortSha(last.commit)}</span>
                        </span>
                      ) : (
                        "—"
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : null}

      {!projects && !error ? <p className="muted">Loading…</p> : null}

    </div>
  );
}
