import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { HELP, docUrl, type HelpTerm } from "../lib/help";

interface InfoTipProps {
  term: HelpTerm;
}

const MARGIN = 8;
const POPOVER_WIDTH = 320;

/**
 * A small "?" button that opens a short explanation. It is a popover, not a
 * dialog: it never takes focus, never scrolls, and closes on Esc, on a click
 * outside, and when focus leaves it.
 */
export function InfoTip({ term }: InfoTipProps) {
  const entry = HELP[term];
  const popId = useId();
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number; width: number } | null>(null);
  const wrapRef = useRef<HTMLSpanElement>(null);
  const btnRef = useRef<HTMLButtonElement>(null);

  const place = useCallback(() => {
    const btn = btnRef.current;
    if (!btn) return;
    const r = btn.getBoundingClientRect();
    const vw = document.documentElement.clientWidth;
    const width = Math.min(POPOVER_WIDTH, vw - 2 * MARGIN);
    const left = Math.max(MARGIN, Math.min(r.left, vw - width - MARGIN));
    setPos({ top: r.bottom + 6, left, width });
  }, []);

  useLayoutEffect(() => {
    if (open) place();
  }, [open, place]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent | TouchEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      // Capture phase: a surrounding dialog must not also close on this Esc.
      e.stopPropagation();
      e.preventDefault();
      setOpen(false);
      btnRef.current?.focus();
    };
    const onFocusIn = (e: FocusEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("touchstart", onDown);
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("focusin", onFocusIn);
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("touchstart", onDown);
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("focusin", onFocusIn);
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [open, place]);

  return (
    <span className="infotip" ref={wrapRef}>
      <button
        ref={btnRef}
        type="button"
        className="infotip-btn"
        aria-expanded={open}
        aria-controls={open ? popId : undefined}
        aria-label={`What is ${entry.title}?`}
        onClick={() => setOpen((o) => !o)}
      >
        ?
      </button>
      {open ? (
        <div
          id={popId}
          role="group"
          aria-label={entry.title}
          className="infotip-pop"
          style={pos ? { top: pos.top, left: pos.left, width: pos.width } : undefined}
        >
          <strong className="infotip-title">{entry.title}</strong>
          <p>{entry.body}</p>
          {"doc" in entry && entry.doc ? (
            <a href={docUrl(entry.doc)} target="_blank" rel="noopener noreferrer">
              Read more in the docs
            </a>
          ) : null}
        </div>
      ) : null}
    </span>
  );
}
