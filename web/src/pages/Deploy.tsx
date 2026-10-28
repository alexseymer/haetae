import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { Deploy as DeployType } from "../api/types";
import { PageHeader } from "../components/PageHeader";
import { DeployLogViewer } from "../components/DeployLogViewer";
import { DeployResult } from "../components/DeployResult";
import { PhaseStepper } from "../components/PhaseStepper";
import { isTerminalStatus } from "../lib/deploy";
import { INTROS } from "../lib/help";
import { usePolling } from "../hooks/usePolling";

export function DeployPage() {
  const { id = "" } = useParams();
  const [deploy, setDeploy] = useState<DeployType | null>(null);
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
    if (!id) return;
    const gen = ++genRef.current;
    try {
      const res = await api.deploy(id);
      if (gen !== genRef.current || !mountedRef.current) return;
      setDeploy(res.deploy);
      setError(null);
    } catch (err) {
      if (gen !== genRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load deploy");
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  const active = !!deploy && !isTerminalStatus(deploy.status);
  usePolling(load, 2000, active, id);

  return (
    <div className="page">
      <p>
        {deploy ? (
          <Link to={`/projects/${deploy.projectId}`}>← {deploy.projectName || "Project"}</Link>
        ) : (
          <Link to="/">← Hosts</Link>
        )}
      </p>
      {error ? (
        <div className="app-error" role="alert">
          {error}
        </div>
      ) : null}
      {deploy ? (
        <div className="stack">
          <PageHeader title={`Deploy ${deploy.id.slice(0, 8)}`} intro={INTROS.deploy} />
          <PhaseStepper status={deploy.status} />
          <DeployResult deploy={deploy} />
          <DeployLogViewer
            deployId={deploy.id}
            forceDone={isTerminalStatus(deploy.status)}
          />
        </div>
      ) : !error ? (
        <p className="muted">Loading…</p>
      ) : null}
    </div>
  );
}
