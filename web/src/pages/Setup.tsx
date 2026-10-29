import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, api } from "../api/client";
import { useAuth } from "../auth";
import { LogoMark } from "../components/Logo";

export function SetupPage() {
  const navigate = useNavigate();
  const { setSession } = useAuth();
  const [setupCode, setSetupCode] = useState("");
  const [orgName, setOrgName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const res = await api.setup({ setupCode, orgName, email, password });
      setSession(res.user, res.orgName);
      navigate("/", { replace: true });
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : "Setup failed";
      setError(msg);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="page-narrow">
      <div className="auth-brand">
        <LogoMark size={44} />
        <span className="brand-name">Haetae</span>
      </div>
      <h1>Set up Haetae</h1>
      <p className="muted">
        The setup code is printed in the server log and in the file{" "}
        <code>setup-code</code> in the server&apos;s data directory.
      </p>
      <form onSubmit={(e) => void onSubmit(e)} className="card">
        <div className="field">
          <label htmlFor="setup-code">Setup code</label>
          <input
            id="setup-code"
            name="setupCode"
            autoComplete="one-time-code"
            value={setupCode}
            onChange={(e) => setSetupCode(e.target.value)}
            required
          />
        </div>
        <div className="field">
          <label htmlFor="org-name">Organisation name</label>
          <input
            id="org-name"
            name="orgName"
            value={orgName}
            onChange={(e) => setOrgName(e.target.value)}
            required
          />
        </div>
        <div className="field">
          <label htmlFor="setup-email">Email</label>
          <input
            id="setup-email"
            name="email"
            type="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
        </div>
        <div className="field">
          <label htmlFor="setup-password">Password</label>
          <div className="password-row">
            <input
              id="setup-password"
              name="password"
              type={showPassword ? "text" : "password"}
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => setShowPassword((s) => !s)}
              aria-label={showPassword ? "Hide password" : "Show password"}
              aria-pressed={showPassword}
            >
              {showPassword ? "Hide" : "Show"}
            </button>
          </div>
        </div>
        {error ? (
          <div className="app-error" role="alert" aria-live="assertive">
            {error}
          </div>
        ) : null}
        <button type="submit" className="btn" disabled={submitting}>
          {submitting ? "Creating…" : "Create organisation"}
        </button>
      </form>
    </div>
  );
}
