import type { ReactNode } from "react";
import { docUrl, type DocName } from "../lib/help";

interface EmptyStateProps {
  title: string;
  children?: ReactNode;
  /** Primary button or link. */
  action?: ReactNode;
  doc?: { name: DocName; label: string };
}

/** Says what is missing and what to do about it. Not a heading, so page outlines stay stable. */
export function EmptyState({ title, children, action, doc }: EmptyStateProps) {
  return (
    <div className="empty-state">
      <p className="empty-state-title">{title}</p>
      {children ? <p className="empty-state-body">{children}</p> : null}
      {action ? <div className="empty-state-action">{action}</div> : null}
      {doc ? (
        <p className="empty-state-doc">
          <a href={docUrl(doc.name)} target="_blank" rel="noopener noreferrer">
            {doc.label}
          </a>
        </p>
      ) : null}
    </div>
  );
}
