import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { prefersReducedMotion } from "../lib/motion";
import { Icon } from "./icons";

interface ToastItem {
  id: number;
  text: string;
  leaving: boolean;
}

interface ToastApi {
  /** Shows a short confirmation, e.g. "Command copied". */
  notify: (text: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const SHOW_MS = 3200;
const LEAVE_MS = 220;

/** Without a provider (unit tests of single components) notify does nothing. */
const NOOP: ToastApi = { notify: () => undefined };

export function useToast(): ToastApi {
  return useContext(ToastContext) ?? NOOP;
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const nextId = useRef(1);
  const timers = useRef<number[]>([]);

  useEffect(() => {
    const list = timers.current;
    return () => list.forEach((t) => window.clearTimeout(t));
  }, []);

  const notify = useCallback((text: string) => {
    const id = nextId.current++;
    setItems((cur) => [...cur.slice(-3), { id, text, leaving: false }]);
    const leave = window.setTimeout(() => {
      if (prefersReducedMotion()) {
        setItems((cur) => cur.filter((t) => t.id !== id));
        return;
      }
      setItems((cur) => cur.map((t) => (t.id === id ? { ...t, leaving: true } : t)));
      timers.current.push(
        window.setTimeout(() => setItems((cur) => cur.filter((t) => t.id !== id)), LEAVE_MS),
      );
    }, SHOW_MS);
    timers.current.push(leave);
  }, []);

  const api = useMemo(() => ({ notify }), [notify]);

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="toast-region" role="status" aria-live="polite" aria-atomic="false">
        {items.map((t) => (
          <div key={t.id} className={`toast${t.leaving ? " leaving" : ""}`}>
            <Icon name="check" size={16} />
            <span>{t.text}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
