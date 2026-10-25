import { useState } from "react";
import type { Deploy } from "../api/types";
import { Badge } from "./Badge";
import { Modal } from "./Modal";
import {
  canRollback,
  deployStatusLabel,
  deployStatusVariant,
  formatDuration,
  safeHttpsHref,
  shortSha,
  triggerLabel,
} from "../lib/deploy";
import { formatDateTime, formatRelativeTime } from "../lib/format";

interface DeployHistoryProps {
  deploys: Deploy[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  isAdmin: boolean;
  onRollback: (deploy: Deploy) => Promise<void>;
  busy?: boolean;
}

export function DeployHistory({
  deploys,
  selectedId,
  onSelect,
  isAdmin,
  onRollback,
  busy = false,
}: DeployHistoryProps) {
  const [confirm, setConfirm] = useState<Deploy | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const doRollback = async () => {
    if (!confirm) return;
    setSubmitting(true);
    setErr(null);
    try {
      await onRollback(confirm);
      setConfirm(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Rollback failed");
    } finally {
      setSubmitting(false);
    }
  };

  if (deploys.length === 0) {
    return <div className="empty-state">No deploys yet.</div>;
  }

  return (
    <>
      <div className="table-wrap">
        <table className="data">
          <thead>
            <tr>
              <th>Status</th>
              <th>Trigger</th>
              <th>Commit</th>
              <th>Started</th>
              <th>Duration</th>
              <th>URL</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {deploys.map((d) => {
              const href = safeHttpsHref(d.url);
              const showRollback = isAdmin && canRollback(d, deploys);
              return (
                <tr
                  key={d.id}
                  className="clickable"
                  tabIndex={0}
                  aria-selected={selectedId === d.id}
                  onClick={() => onSelect(d.id)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      onSelect(d.id);
                    }
                  }}
                >
                  <td>
                    <Badge variant={deployStatusVariant(d.status)}>
                      {deployStatusLabel(d.status)}
                    </Badge>
                  </td>
                  <td>{triggerLabel(d.trigger)}</td>
                  <td className="mono">{shortSha(d.commit)}</td>
                  <td title={formatDateTime(d.startedAt)}>
                    {formatRelativeTime(d.startedAt)}
                  </td>
                  <td>{formatDuration(d.startedAt, d.finishedAt)}</td>
                  <td>
                    {href ? (
                      <a
                        href={href}
                        rel="noopener noreferrer"
                        target="_blank"
                        onClick={(e) => e.stopPropagation()}
                      >
                        {d.url}
                      </a>
                    ) : (
                      d.url || "—"
                    )}
                  </td>
                  <td>
                    {showRollback ? (
                      <button
                        type="button"
                        className="btn btn-secondary"
                        disabled={busy}
                        onClick={(e) => {
                          e.stopPropagation();
                          setErr(null);
                          setConfirm(d);
                        }}
                      >
                        Rollback
                      </button>
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

      <Modal
        title="Confirm rollback"
        open={confirm != null}
        onClose={() => setConfirm(null)}
      >
        {confirm ? (
          <div className="stack">
            <p>
              Roll back to commit <span className="mono">{shortSha(confirm.commit)}</span>
              {confirm.commit.length > 7 ? (
                <span className="muted"> ({confirm.commit})</span>
              ) : null}
              ?
            </p>
            {err ? (
              <div className="app-error" role="alert">
                {err}
              </div>
            ) : null}
            <div className="row">
              <button
                type="button"
                className="btn"
                disabled={submitting}
                onClick={() => void doRollback()}
              >
                {submitting ? "Starting…" : "Roll back"}
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => setConfirm(null)}
              >
                Cancel
              </button>
            </div>
          </div>
        ) : null}
      </Modal>
    </>
  );
}
