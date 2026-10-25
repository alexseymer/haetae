import { useEffect, useState, type ReactNode } from "react";
import { Navigate, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { api, setUnauthorizedHandler } from "./api/client";
import { useAuth } from "./auth";
import { Layout } from "./components/Layout";
import { HostPage } from "./pages/Host";
import { HostsPage } from "./pages/Hosts";
import { LoginPage } from "./pages/Login";
import { SettingsPage } from "./pages/Settings";
import { SetupPage } from "./pages/Setup";

function RequireAuth({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  const location = useLocation();
  if (!user) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return children;
}

export function App() {
  const { user, setSession, clearSession } = useAuth();
  const navigate = useNavigate();
  const [bootstrapping, setBootstrapping] = useState(true);
  const [setupRequired, setSetupRequired] = useState(false);

  useEffect(() => {
    setUnauthorizedHandler(() => {
      clearSession();
      navigate("/login", { replace: true });
    });
    return () => setUnauthorizedHandler(null);
  }, [clearSession, navigate]);

  useEffect(() => {
    let cancelled = false;
    const boot = async () => {
      try {
        const status = await api.status();
        if (cancelled) return;
        setSetupRequired(status.setupRequired);
        if (!status.setupRequired) {
          try {
            const me = await api.me();
            if (!cancelled) setSession(me.user, me.orgName);
          } catch {
            // not signed in
          }
        }
      } catch {
        // status failed — still show login
      } finally {
        if (!cancelled) setBootstrapping(false);
      }
    };
    void boot();
    return () => {
      cancelled = true;
    };
  }, [setSession]);

  // Completing setup sets the session but does not re-fetch /api/status. Clear
  // the flag so sign-out can reach /login instead of bouncing back to /setup.
  useEffect(() => {
    if (user && setupRequired) {
      setSetupRequired(false);
    }
  }, [user, setupRequired]);

  if (bootstrapping) {
    return (
      <div className="page-narrow">
        <p className="muted">Loading…</p>
      </div>
    );
  }

  return (
    <Routes>
      <Route
        path="/setup"
        element={
          setupRequired || !user ? (
            setupRequired ? (
              <SetupPage />
            ) : (
              <Navigate to="/login" replace />
            )
          ) : (
            <Navigate to="/" replace />
          )
        }
      />
      <Route
        path="/login"
        element={
          setupRequired ? (
            <Navigate to="/setup" replace />
          ) : user ? (
            <Navigate to="/" replace />
          ) : (
            <LoginPage />
          )
        }
      />
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route path="/" element={<HostsPage />} />
        <Route path="/hosts/:id" element={<HostPage />} />
        <Route path="/settings" element={<SettingsPage />} />
      </Route>
      <Route
        path="*"
        element={<Navigate to={setupRequired ? "/setup" : user ? "/" : "/login"} replace />}
      />
    </Routes>
  );
}
