import type { ReactNode } from "react";
import { Icon, type IconName } from "./icons";

interface PageHeaderProps {
  title: string;
  /** One or two plain sentences saying what the page is for. */
  intro?: string;
  /** Badges or links shown right after the title. */
  meta?: ReactNode;
  /** The primary action(s), right aligned. */
  actions?: ReactNode;
  /** Decorative icon tile before the title. */
  icon?: IconName;
}

/** Title, intro and primary action, the same on every page. */
export function PageHeader({ title, intro, meta, actions, icon }: PageHeaderProps) {
  return (
    <div className="row page-header">
      <div className="page-header-main">
        <div className="row page-header-title">
          {icon ? (
            <span className="page-header-icon">
              <Icon name={icon} size={20} />
            </span>
          ) : null}
          <h1>{title}</h1>
          {meta}
        </div>
        {intro ? <p className="page-intro">{intro}</p> : null}
      </div>
      {actions ? <div className="row page-header-actions">{actions}</div> : null}
    </div>
  );
}
