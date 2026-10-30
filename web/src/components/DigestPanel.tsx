import { useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError } from "../api/client";
import type { DigestResponse } from "../api/digestTypes";
import { DigestCard } from "./DigestCard";
import { Loading } from "./Skeleton";

interface DigestPanelProps {
  heading: string;
  buttonLabel: string;
  showAttention?: boolean;
  /** Optional controls rendered before the button (for example the window choice). */
  controls?: ReactNode;
  run: () => Promise<DigestResponse>;
}

/** Button + loading skeleton + error + result card shared by both digests. */
export function DigestPanel({
  heading,
  buttonLabel,
  showAttention = false,
  controls,
  run,
}: DigestPanelProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [digest, setDigest] = useState<DigestResponse | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const onClick = async () => {
    setBusy(true);
    setError(null);
    try {
      const d = await run();
      if (mounted.current) setDigest(d);
    } catch (err) {
      if (mounted.current) {
        setDigest(null);
        setError(err instanceof ApiError ? err.message : "Could not create the digest");
      }
    } finally {
      if (mounted.current) setBusy(false);
    }
  };

  return (
    <section className="stack digest-panel" aria-label={heading}>
      <div className="digest-controls">
        <h3 className="digest-heading">{heading}</h3>
        {controls}
        <button type="button" className="btn btn-secondary" disabled={busy} onClick={() => void onClick()}>
          {buttonLabel}
        </button>
      </div>
      {busy ? <Loading label={`${heading}: working…`} variant="lines" rows={3} /> : null}
      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}
      {digest && !busy ? <DigestCard digest={digest} showAttention={showAttention} /> : null}
    </section>
  );
}
