import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { Deploy, PatchProjectRequest, Project as ProjectType } from "../api/types";
import { useAuth } from "../auth";
import { Badge } from "../components/Badge";
import { DeployHistory } from "../components/DeployHistory";
import { DeployLogViewer } from "../components/DeployLogViewer";
import { DeployResult } from "../components/DeployResult";
import { InfoTip } from "../components/InfoTip";
import { Modal } from "../components/Modal";
import { PageHeader } from "../components/PageHeader";
import { PhaseStepper } from "../components/PhaseStepper";
import { ProjectForm } from "../components/ProjectForm";
import { Loading } from "../components/Skeleton";
import {
  domainHref,
  isTerminalStatus,
  sourceSummary,
} from "../lib/deploy";
import { usePolling } from "../hooks/usePolling";
import { INTROS } from "../lib/help";

export function ProjectPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  const [project, setProject] = useState<ProjectType | null>(null);
  const [inlineCompose, setInlineCompose] = useState<string | undefined>();
  const [deploys, setDeploys] = useState<Deploy[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [selected, setSelected] = useState<Deploy | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const [editOpen, setEditOpen] = useState(false);
  const [editError, setEditError] = useState<string | null>(null);
  const [editSubmitting, setEditSubmitting] = useState(false);

  const [removeOpen, setRemoveOpen] = useState(false);
  const [removeForce, setRemoveForce] = useState(false);
  const [showForce, setShowForce] = useState(false);
  const [removeError, setRemoveError] = useState<string | null>(null);
  const [removing, setRemoving] = useState(false);
  const [deploying, setDeploying] = useState(false);

  const mountedRef = useRef(true);
  const projGenRef = useRef(0);
  const depsGenRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const loadProject = useCallback(async () => {
    if (!id) return;
    const gen = ++projGenRef.current;
    try {
      const res = await api.project(id);
      if (gen !== projGenRef.current || !mountedRef.current) return;
      setProject(res.project);
      setInlineCompose(res.inlineCompose);
      setError(null);
    } catch (err) {
      if (gen !== projGenRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load project");
    }
  }, [id]);

  const loadDeploys = useCallback(async () => {
    if (!id) return;
    const gen = ++depsGenRef.current;
    try {
      const res = await api.projectDeploys(id);
      if (gen !== depsGenRef.current || !mountedRef.current) return;
      setDeploys(res.deploys);
      setSelectedId((cur) => cur ?? res.deploys[0]?.id ?? null);
    } catch (err) {
      if (gen !== depsGenRef.current || !mountedRef.current) return;
      setActionError(err instanceof ApiError ? err.message : "Failed to load deploys");
    }
  }, [id]);

  useEffect(() => {
    void loadProject();
  }, [loadProject]);

  const hasActive = deploys.some((d) => !isTerminalStatus(d.status));
  usePolling(loadDeploys, 3000, !!id, id);

  useEffect(() => {
    if (!selectedId) {
      setSelected(null);
      return;
    }
    const fromList = deploys.find((d) => d.id === selectedId);
    if (fromList && isTerminalStatus(fromList.status)) {
      setSelected(fromList);
      return;
    }
    let cancelled = false;
    void api
      .deploy(selectedId)
      .then((res) => {
        if (!cancelled && mountedRef.current) setSelected(res.deploy);
      })
      .catch(() => {
        if (!cancelled && fromList) setSelected(fromList);
      });
    return () => {
      cancelled = true;
    };
  }, [selectedId, deploys, hasActive]);

  const onDeployNow = async () => {
    setDeploying(true);
    setActionError(null);
    try {
      const res = await api.startDeploy(id);
      setSelectedId(res.deploy.id);
      await loadDeploys();
    } catch (err) {
      setActionError(err instanceof ApiError ? err.message : "Deploy failed");
    } finally {
      setDeploying(false);
    }
  };

  const onToggleAuto = async () => {
    if (!project) return;
    setActionError(null);
    try {
      const res = await api.patchProject(id, { autoDeploy: !project.autoDeploy });
      setProject(res.project);
    } catch (err) {
      setActionError(err instanceof ApiError ? err.message : "Update failed");
    }
  };

  const onEdit = async (payload: { patch?: PatchProjectRequest }) => {
    if (!payload.patch) return;
    setEditSubmitting(true);
    setEditError(null);
    try {
      const res = await api.patchProject(id, payload.patch);
      setProject(res.project);
      setEditOpen(false);
    } catch (err) {
      setEditError(err instanceof ApiError ? err.message : "Save failed");
    } finally {
      setEditSubmitting(false);
    }
  };

  const onRemove = async () => {
    if (!project) return;
    setRemoving(true);
    setRemoveError(null);
    try {
      await api.deleteProject(id, removeForce);
      navigate(`/hosts/${project.hostId}`);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && !removeForce) {
        setShowForce(true);
        setRemoveError(err.message);
      } else {
        setRemoveError(err instanceof ApiError ? err.message : "Remove failed");
      }
    } finally {
      setRemoving(false);
    }
  };

  const onRollback = async (d: Deploy) => {
    const res = await api.rollbackDeploy(d.id);
    setSelectedId(res.deploy.id);
    await loadDeploys();
  };

  const domainLink = project ? domainHref(project.domain) : null;

  return (
    <div className="page">
      <p>
        {project ? (
          <Link to={`/hosts/${project.hostId}`}>← Host</Link>
        ) : (
          <Link to="/">← Hosts</Link>
        )}
      </p>

      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}

      {project ? (
        <>
          <PageHeader
            title={project.name}
            intro={INTROS.project}
            actions={
              isAdmin && !editOpen ? (
                <>
                  <button
                    type="button"
                    className="btn"
                    disabled={deploying}
                    onClick={() => void onDeployNow()}
                  >
                    {deploying ? "Starting…" : "Deploy now"}
                  </button>
                  <button
                    type="button"
                    className="btn btn-secondary"
                    onClick={() => {
                      setEditError(null);
                      setEditOpen(true);
                    }}
                  >
                    Edit
                  </button>
                  <button
                    type="button"
                    className="btn btn-secondary"
                    onClick={() => {
                      setRemoveForce(false);
                      setShowForce(false);
                      setRemoveError(null);
                      setRemoveOpen(true);
                    }}
                  >
                    Remove
                  </button>
                </>
              ) : null
            }
          />
          <p className="muted mono">{sourceSummary(project.source)}</p>
          {project.domain ? (
            <p>
              {domainLink ? (
                <a href={domainLink} rel="noopener noreferrer" target="_blank">
                  {project.domain}
                </a>
              ) : (
                project.domain
              )}
            </p>
          ) : null}

          {editOpen ? (
            <ProjectForm
              mode="edit"
              project={project}
              inlineCompose={inlineCompose}
              serverError={editError}
              submitting={editSubmitting}
              onClose={() => setEditOpen(false)}
              onSubmit={onEdit}
            />
          ) : (
            <>
          {project.source.kind === "git" && isAdmin ? (
            <div className="row checkbox-row">
              <label className="checkbox">
                <input
                  type="checkbox"
                  checked={project.autoDeploy}
                  onChange={() => void onToggleAuto()}
                />{" "}
                Auto-deploy
                {project.autoDeploy ? (
                  <Badge variant="info">On</Badge>
                ) : null}
              </label>
              <InfoTip term="autoDeploy" />
            </div>
          ) : project.autoDeploy ? (
            <Badge variant="info">Auto-deploy</Badge>
          ) : null}

          {actionError ? (
            <div className="app-error" role="alert">
              {actionError}
            </div>
          ) : null}

          <h2>Deploy history</h2>
          <DeployHistory
            deploys={deploys}
            selectedId={selectedId}
            onSelect={setSelectedId}
            isAdmin={!!isAdmin}
            onRollback={onRollback}
            busy={deploying}
          />

          {selected ? (
            <div className="panel deploy-detail" id={`deploy-${selected.id}`}>
              <div className="row" style={{ justifyContent: "space-between" }}>
                <h2 style={{ margin: 0 }}>Deploy {selected.id.slice(0, 8)}</h2>
                <Link to={`/deploys/${selected.id}`}>Open</Link>
              </div>
              <PhaseStepper status={selected.status} />
              <DeployResult deploy={selected} />
              <DeployLogViewer
                deployId={selected.id}
                forceDone={isTerminalStatus(selected.status)}
              />
            </div>
          ) : null}
            </>
          )}
        </>
      ) : !error ? (
        <Loading label="Loading…" variant="lines" />
      ) : null}

      <Modal title="Remove project" open={removeOpen} onClose={() => setRemoveOpen(false)}>
        <div className="stack">
          <p>
            Remove <strong>{project?.name}</strong>? Stops the containers. Volumes and data
            are kept.
          </p>
          {showForce ? (
            <label className="checkbox">
              <input
                type="checkbox"
                checked={removeForce}
                onChange={(e) => setRemoveForce(e.target.checked)}
              />{" "}
              Force delete records (host is offline)
            </label>
          ) : null}
          {removeError ? (
            <div className="app-error" role="alert">
              {removeError}
            </div>
          ) : null}
          <div className="row">
            <button
              type="button"
              className="btn"
              disabled={removing || (showForce && !removeForce)}
              onClick={() => void onRemove()}
            >
              {removing ? "Removing…" : "Remove"}
            </button>
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setRemoveOpen(false)}
            >
              Cancel
            </button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
