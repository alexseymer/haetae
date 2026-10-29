import type { ReactNode } from "react";
import { docUrl, type DocName } from "../lib/help";
import { Illustration, type IllustrationKind } from "./illustrations";

interface EmptyStateProps {
  title: string;
  children?: ReactNode;
  /** Primary button or link. */
  action?: ReactNode;
  doc?: { name: DocName; label: string };
  /** Line-art picture above the title. */
  art?: IllustrationKind;
}

/** Says what is missing and what to do about it. Not a heading, so page outlines stay stable. */
export function EmptyState({ title, children, action, doc, art = "generic" }: EmptyStateProps) {
  return (
    <div className="empty-state">
      <Illustration kind={art} />
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
