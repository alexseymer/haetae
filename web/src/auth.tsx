import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { User } from "./api/types";
import posthog, { isPostHogEnabled, posthogLogger } from "./posthog";

interface AuthState {
  user: User | null;
  orgName: string | null;
  setSession: (user: User, orgName: string) => void;
  clearSession: () => void;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [orgName, setOrgName] = useState<string | null>(null);
  const identifiedUserId = useRef<string | null>(null);

  const setSession = useCallback((u: User, org: string) => {
    if (isPostHogEnabled && identifiedUserId.current !== u.id) {
      if (identifiedUserId.current !== null) {
        posthog.reset();
      }
      posthog.identify(u.id, { email: u.email, role: u.role });
      posthogLogger.info("authenticated session established");
      identifiedUserId.current = u.id;
    }
    setUser(u);
    setOrgName(org);
  }, []);

  const clearSession = useCallback(() => {
    if (isPostHogEnabled && identifiedUserId.current !== null) {
      posthogLogger.info("authenticated session cleared");
      posthog.reset();
      identifiedUserId.current = null;
    }
    setUser(null);
    setOrgName(null);
  }, []);

  const value = useMemo(
    () => ({ user, orgName, setSession, clearSession }),
    [user, orgName, setSession, clearSession],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
