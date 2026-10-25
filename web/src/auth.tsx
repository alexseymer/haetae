import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import type { User } from "./api/types";

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

  const setSession = useCallback((u: User, org: string) => {
    setUser(u);
    setOrgName(org);
  }, []);

  const clearSession = useCallback(() => {
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
