import type { ReactNode } from "react";

export type Variant = "online" | "offline" | "warn" | "danger" | "info";

interface BadgeProps {
  variant: Variant;
  children: ReactNode;
  withDot?: boolean;
}

export function Badge({ variant, children, withDot = false }: BadgeProps) {
  return (
    <span className={`badge badge-${variant}`}>
      {withDot ? (
        <span className={`badge-dot${variant === "online" ? " pulse-dot" : ""}`} aria-hidden="true" />
      ) : null}
      {children}
    </span>
  );
}

export function OnlineBadge({ online }: { online: boolean }) {
  return (
    <Badge variant={online ? "online" : "offline"} withDot>
      {online ? "Online" : "Offline"}
    </Badge>
  );
}
