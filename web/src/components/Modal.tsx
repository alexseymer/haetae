import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";

import { prefersReducedMotion } from "../lib/motion";

/** Length of the exit animation; keep in sync with .modal-backdrop.closing. */
const EXIT_MS = 150;

interface ModalProps {
  title: string;
  open: boolean;
  onClose: () => void;
  children: ReactNode;
}

/**
 * Dialogs never scroll: their content is designed to fit (split into steps,
 * moved to a page, or shortened). CSS clips instead of scrolling, and tests
 * assert that nothing is actually clipped (see test/dialogGuard.ts and the
 * real-browser check in test/e2e/ui.spec.mjs).
 */
const FOCUSABLE =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function Modal({ title, open, onClose, children }: ModalProps) {
  const titleId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const backdropRef = useRef<HTMLDivElement>(null);
  const previouslyFocused = useRef<HTMLElement | null>(null);

  // Keep rendering for the exit animation. While closing the dialog is hidden
  // from assistive tech and a reopening starts a fresh instance (new key), so
  // one-time content such as an install token never reappears.
  const [visible, setVisible] = useState(open);
  const session = useRef(0);
  const wasOpen = useRef(false);
  if (open && !wasOpen.current) session.current += 1;
  wasOpen.current = open;
  if (open && !visible) setVisible(true);
  if (!open && visible && prefersReducedMotion()) setVisible(false);

  useEffect(() => {
    if (open || !visible) return;
    const t = window.setTimeout(() => setVisible(false), EXIT_MS);
    return () => window.clearTimeout(t);
  }, [open, visible]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  // Inert siblings + initial focus share one effect so cleanup clears inert
  // before restoring focus (focusing an inert control is a no-op).
  useEffect(() => {
    if (!open) return;
    previouslyFocused.current = document.activeElement as HTMLElement | null;
    const backdrop = backdropRef.current;
    const parent = backdrop?.parentElement;
    const siblings =
      backdrop && parent
        ? Array.from(parent.children).filter(
            (el): el is HTMLElement => el !== backdrop && el instanceof HTMLElement,
          )
        : [];
    for (const el of siblings) {
      el.inert = true;
    }
    const panel = panelRef.current;
    if (panel) {
      const focusables = panel.querySelectorAll<HTMLElement>(FOCUSABLE);
      const first = focusables[0];
      (first ?? panel).focus();
    }
    return () => {
      for (const el of siblings) {
        el.inert = false;
      }
      previouslyFocused.current?.focus?.();
    };
  }, [open]);

  if (!open && !visible) return null;

  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "Tab" || !panelRef.current) return;
    const focusables = Array.from(panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
      (el) => !el.hasAttribute("disabled") && el.tabIndex !== -1,
    );
    if (focusables.length === 0) {
      e.preventDefault();
      return;
    }
    const first = focusables[0]!;
    const last = focusables[focusables.length - 1]!;
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  return (
    <div
      key={session.current}
      ref={backdropRef}
      className={`modal-backdrop${open ? "" : " closing"}`}
      aria-hidden={open ? undefined : true}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={panelRef}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        <div className="modal-header">
          <h2 id={titleId}>{title}</h2>
          <button type="button" className="btn btn-ghost" onClick={onClose} aria-label="Close">
            Close
          </button>
        </div>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  );
}
