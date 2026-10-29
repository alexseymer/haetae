/**
 * True when animation should be skipped. Environments without matchMedia
 * (jsdom, very old browsers) count as reduced so values simply appear.
 */
export function prefersReducedMotion(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return true;
  try {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return true;
  }
}

/** Largest stagger step with its own CSS delay rule (see motion.css). */
export const MAX_STAGGER = 12;

/**
 * Entrance delay slot for staggered lists: pair with className="stagger-item"
 * and spread the result as data-stagger. Delays live in CSS, so no inline styles.
 */
export function staggerIndex(index: number): number {
  return Math.max(0, Math.min(index, MAX_STAGGER));
}
