import { staggerIndex } from "../lib/motion";

type Variant = "table" | "tiles" | "block" | "lines" | "page";

interface LoadingProps {
  /** Announced to screen readers, e.g. "Loading hosts…". */
  label?: string;
  variant?: Variant;
  rows?: number;
}

/** A single shimmering placeholder bar. */
export function Skeleton({ className = "" }: { className?: string }) {
  return <span className={`skeleton ${className}`.trim()} aria-hidden="true" />;
}

/**
 * Skeleton loader with an accessible status message. Replaces "Loading…" text:
 * sighted users see the shape of what is coming, assistive tech hears the label.
 */
export function Loading({ label = "Loading…", variant = "lines", rows = 4 }: LoadingProps) {
  const n = Math.max(1, rows);
  return (
    <div className="skeleton-group" role="status" aria-busy="true">
      <span className="sr-only">{label}</span>
      {variant === "page" ? <Skeleton className="skeleton-title" /> : null}
      {variant === "tiles" || variant === "page" ? (
        <div className="skeleton-tiles">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="skeleton-tile" />
          ))}
        </div>
      ) : null}
      {variant === "table" || variant === "page" ? (
        <div>
          {Array.from({ length: n }, (_, i) => (
            <span key={i} className="stagger-item" data-stagger={staggerIndex(i)}>
              <Skeleton className="skeleton-row" />
            </span>
          ))}
        </div>
      ) : null}
      {variant === "block" ? <Skeleton className="skeleton-block" /> : null}
      {variant === "lines" ? (
        <>
          <Skeleton className="skeleton-line w-90" />
          <Skeleton className="skeleton-line w-70" />
          <Skeleton className="skeleton-line w-50" />
        </>
      ) : null}
    </div>
  );
}
