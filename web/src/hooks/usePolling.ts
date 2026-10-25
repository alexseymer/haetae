import { useEffect, useRef } from "react";

/**
 * Call `fn` immediately and then every `intervalMs` while the document is visible.
 * Pauses when document.visibilityState !== "visible".
 * When becoming visible again, starts the interval (which runs `fn` once) without a
 * second immediate call.
 * `restartKey` remounts the effect (immediate run + new interval) when it changes.
 */
export function usePolling(
  fn: () => void | Promise<void>,
  intervalMs: number,
  enabled = true,
  restartKey?: unknown,
): void {
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    if (!enabled) return;

    let timer: ReturnType<typeof setInterval> | null = null;
    let cancelled = false;

    const run = () => {
      if (cancelled) return;
      void fnRef.current();
    };

    const start = () => {
      if (timer != null) return;
      run();
      timer = setInterval(() => {
        if (document.visibilityState === "visible") {
          run();
        }
      }, intervalMs);
    };

    const stop = () => {
      if (timer != null) {
        clearInterval(timer);
        timer = null;
      }
    };

    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        // start() already runs once; do not call run() again (double fetch).
        start();
      } else {
        stop();
      }
    };

    if (document.visibilityState === "visible") {
      start();
    }

    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      cancelled = true;
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [intervalMs, enabled, restartKey]);
}
