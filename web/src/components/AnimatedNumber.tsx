import { useEffect, useRef, useState } from "react";
import { prefersReducedMotion } from "../lib/motion";

interface AnimatedNumberProps {
  value: number;
  /** Animation length in ms. */
  duration?: number;
}

/**
 * Counts up to `value`. Renders plain text (no wrapper element) so the final
 * number is always what the DOM and assistive tech see once the count ends,
 * and so surrounding text queries keep working. With reduced motion (or no
 * matchMedia) the value appears at once.
 */
export function AnimatedNumber({ value, duration = 700 }: AnimatedNumberProps) {
  const [shown, setShown] = useState(() => (prefersReducedMotion() ? value : 0));
  const fromRef = useRef(shown);

  useEffect(() => {
    if (prefersReducedMotion() || typeof requestAnimationFrame !== "function") {
      fromRef.current = value;
      setShown(value);
      return;
    }
    const from = fromRef.current;
    if (from === value) return;
    const start = performance.now();
    let raf = 0;
    const tick = (now: number) => {
      const t = Math.min(1, (now - start) / duration);
      const eased = 1 - Math.pow(1 - t, 3);
      const next = t >= 1 ? value : Math.round(from + (value - from) * eased);
      fromRef.current = next;
      setShown(next);
      if (t < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [value, duration]);

  return <>{shown}</>;
}
