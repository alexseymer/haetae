import type { DigestResponse } from "../api/digestTypes";
import { formatDateTime } from "../lib/format";
import { isSafeEvidencePath } from "../lib/evidenceLink";
import { Badge } from "./Badge";

interface DigestCardProps {
  digest: DigestResponse;
  /** Shows the "Needs attention" badge when the digest asks for it. */
  showAttention?: boolean;
}

/**
 * A log summary or security digest. Statements are rendered exactly as the
 * backend sent them. Evidence links are plain anchors and only for allowlisted
 * app paths (same rule as chat evidence); anything else is shown as text.
 */
export function DigestCard({ digest, showAttention = false }: DigestCardProps) {
  const byId = new Map(digest.evidence.map((e) => [e.id, e]));
  return (
    <article className="card digest-card" aria-label={digest.title}>
      <header className="digest-head">
        <h3 className="digest-title">{digest.title}</h3>
        {showAttention && digest.needsAttention ? (
          <Badge variant="danger">Needs attention</Badge>
        ) : null}
      </header>
      <p className="muted digest-meta">
        Generated {formatDateTime(digest.generatedAt)}
      </p>
      {!digest.usedAI && digest.reason ? (
        <p className="digest-note" role="note">
          {digest.reason}
        </p>
      ) : null}
      {digest.statements.length > 0 ? (
        <ul className="digest-statements">
          {digest.statements.map((st, i) => {
            const refs = st.evidence
              .map((id) => byId.get(id))
              .filter((e): e is NonNullable<typeof e> => e !== undefined);
            return (
              <li key={i}>
                <span>{st.text}</span>
                {refs.length > 0 ? (
                  <ul className="digest-evidence" aria-label="Evidence">
                    {refs.map((ev) => (
                      <li key={ev.id}>
                        {isSafeEvidencePath(ev.link) ? (
                          <a href={ev.link}>{ev.title || ev.link}</a>
                        ) : (
                          <span>{ev.title}</span>
                        )}
                      </li>
                    ))}
                  </ul>
                ) : null}
              </li>
            );
          })}
        </ul>
      ) : null}
    </article>
  );
}
