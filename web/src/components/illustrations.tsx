export type IllustrationKind = "hosts" | "calm" | "chat" | "boxes" | "generic";

/**
 * Simple line art for empty states. Strokes use currentColor (the accent),
 * soft fills use the tint tokens so it works in light and dark.
 */
export function Illustration({ kind, className = "empty-state-art" }: { kind: IllustrationKind; className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 160 110"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <ellipse cx="80" cy="100" rx="52" ry="5" fill="var(--color-tint)" stroke="none" />
      {kind === "hosts" ? (
        <>
          <rect x="34" y="22" width="92" height="26" rx="6" fill="var(--color-surface)" />
          <rect x="34" y="54" width="92" height="26" rx="6" fill="var(--color-surface)" />
          <path d="M46 35h.01M46 67h.01M60 35h30M60 67h30" />
          <path d="M80 80v10M60 90h40" />
          <circle cx="118" cy="35" r="3" fill="var(--color-accent)" stroke="none" />
          <path d="M126 14l3 6 6 3-6 3-3 6-3-6-6-3 6-3 3-6Z" fill="var(--color-tint)" />
        </>
      ) : null}
      {kind === "calm" ? (
        <>
          <path d="M80 14 40 28v26c0 20 16 34 40 42 24-8 40-22 40-42V28L80 14Z" fill="var(--color-surface)" />
          <path d="m64 55 11 11 22-23" strokeWidth={3} />
          <path d="M132 24l2 4 4 2-4 2-2 4-2-4-4-2 4-2 2-4Z" fill="var(--color-tint)" />
          <path d="M24 70l2 4 4 2-4 2-2 4-2-4-4-2 4-2 2-4Z" fill="var(--color-tint)" />
        </>
      ) : null}
      {kind === "chat" ? (
        <>
          <path d="M30 22h70a8 8 0 0 1 8 8v26a8 8 0 0 1-8 8H62l-14 12V64h-18a8 8 0 0 1-8-8V30a8 8 0 0 1 8-8Z" fill="var(--color-surface)" />
          <path d="M44 38h42M44 50h24" />
          <path d="M112 50h18a8 8 0 0 1 8 8v18a8 8 0 0 1-8 8h-4v10l-12-10h-2" fill="var(--color-tint)" />
          <circle cx="124" cy="67" r="2" fill="currentColor" stroke="none" />
        </>
      ) : null}
      {kind === "boxes" ? (
        <>
          <path d="m50 36 20-10 20 10v22L70 68 50 58V36Z" fill="var(--color-surface)" />
          <path d="m50 36 20 10 20-10M70 46v22" />
          <path d="m88 56 20-10 20 10v22l-20 10-20-10V56Z" fill="var(--color-tint)" />
          <path d="m88 56 20 10 20-10M108 66v22" />
        </>
      ) : null}
      {kind === "generic" ? (
        <>
          <rect x="38" y="22" width="84" height="62" rx="8" fill="var(--color-surface)" />
          <path d="M38 38h84M52 30h.01M60 30h.01M68 30h.01M54 54h52M54 66h32" />
        </>
      ) : null}
    </svg>
  );
}
