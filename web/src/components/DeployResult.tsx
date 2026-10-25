import type { Deploy } from "../api/types";
import { safeHttpsHref } from "../lib/deploy";

interface DeployResultProps {
  deploy: Deploy;
}

export function DeployResult({ deploy }: DeployResultProps) {
  if (deploy.status === "healthy") {
    const href = safeHttpsHref(deploy.url);
    return (
      <div className="deploy-result deploy-result-ok" role="status">
        <p>
          Live at{" "}
          {href ? (
            <a href={href} rel="noopener noreferrer" target="_blank">
              {deploy.url}
            </a>
          ) : (
            <span>{deploy.url || "—"}</span>
          )}
        </p>
      </div>
    );
  }

  if (deploy.status === "failed") {
    return (
      <div className="deploy-result deploy-result-fail" role="status">
        <p className="muted">
          Output is redacted on the server&apos;s host before it is sent
        </p>
        <pre className="log-sample">{deploy.error || "Deploy failed"}</pre>
      </div>
    );
  }

  if (deploy.status === "rolled_back") {
    return (
      <div className="deploy-result deploy-result-fail" role="status">
        <p>
          The new version failed to start. The previous version was restored.
        </p>
        {deploy.error ? (
          <>
            <p className="muted">New version error</p>
            <pre className="log-sample">{deploy.error}</pre>
          </>
        ) : null}
        {deploy.message ? (
          <>
            <p className="muted">Rollback note</p>
            <pre className="log-sample">{deploy.message}</pre>
          </>
        ) : null}
      </div>
    );
  }

  if (deploy.status === "rollback_failed") {
    return (
      <div className="deploy-result deploy-result-fail" role="status">
        <p>Rollback failed.</p>
        <pre className="log-sample">{deploy.error || deploy.message || ""}</pre>
      </div>
    );
  }

  return null;
}
