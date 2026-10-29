import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

export type Theme = "light" | "dark";

export const THEME_KEY = "haetae.theme";

function readStored(): Theme | null {
  try {
    const v = window.localStorage.getItem(THEME_KEY);
    return v === "light" || v === "dark" ? v : null;
  } catch {
    return null;
  }
}

function writeStored(t: Theme): void {
  try {
    window.localStorage.setItem(THEME_KEY, t);
  } catch {
    // storage blocked: the choice lasts for this visit only
  }
}

function systemTheme(): Theme {
  try {
    return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  } catch {
    return "light";
  }
}

/** Applies a remembered choice before first paint (called from main.tsx). */
export function applyStoredTheme(): void {
  const stored = readStored();
  if (stored) document.documentElement.dataset.theme = stored;
}

interface ThemeState {
  /** The theme in effect, whether chosen or following the system. */
  theme: Theme;
  toggle: () => void;
}

const ThemeContext = createContext<ThemeState | null>(null);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [chosen, setChosen] = useState<Theme | null>(readStored);
  const [system, setSystem] = useState<Theme>(systemTheme);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => setSystem(mq.matches ? "dark" : "light");
    mq.addEventListener?.("change", onChange);
    return () => mq.removeEventListener?.("change", onChange);
  }, []);

  const theme = chosen ?? system;

  useEffect(() => {
    const root = document.documentElement;
    if (chosen) root.dataset.theme = chosen;
    else delete root.dataset.theme;
  }, [chosen]);

  const toggle = useCallback(() => {
    const next: Theme = theme === "dark" ? "light" : "dark";
    setChosen(next);
    writeStored(next);
  }, [theme]);

  const value = useMemo(() => ({ theme, toggle }), [theme, toggle]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeState {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error("useTheme must be used within ThemeProvider");
  return ctx;
}
