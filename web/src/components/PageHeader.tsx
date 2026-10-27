import type { ReactNode } from "react";

interface PageHeaderProps {
  title: string;
  /** One or two plain sentences saying what the page is for. */
  intro?: string;
  /** Badges or links shown right after the title. */
  meta?: ReactNode;
  /** The primary action(s), right aligned. */
  actions?: ReactNode;
}

/** Title, intro and primary action, the same on every page. */
export function PageHeader({ title, intro, meta, actions }: PageHeaderProps) {
  return (
    <div className="row page-header">
      <div className="page-header-main">
        <div className="row page-header-title">
          <h1>{title}</h1>
          {meta}
        </div>
        {intro ? <p className="page-intro">{intro}</p> : null}
      </div>
      {actions ? <div className="row page-header-actions">{actions}</div> : null}
    </div>
  );
}
