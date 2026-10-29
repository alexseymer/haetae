import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { isApiError, api } from "../api/client";
import { useAuth } from "../auth";
import { LogoMark } from "../components/Logo";

export function LoginPage() {
  const navigate = useNavigate();
  const { setSession } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const res = await api.login({ email, password });
      let orgName = "";
      try {
        const me = await api.me();
        orgName = me.orgName;
        setSession(me.user, me.orgName);
      } catch {
        setSession(res.user, orgName);
      }
      navigate("/", { replace: true });
    } catch (err) {
      if (isApiError(err)) {
        if (err.status === 429 && err.retryAfter != null) {
          setError(`Too many attempts, try again in ${err.retryAfter} seconds`);
        } else {
          setError(err.message);
        }
      } else {
        setError("Sign in failed");
      }
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
      <h1>Sign in</h1>
      <form onSubmit={(e) => void onSubmit(e)} className="card">
        <div className="field">
          <label htmlFor="login-email">Email</label>
          <input
            id="login-email"
            name="email"
            type="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
        </div>
        <div className="field">
          <label htmlFor="login-password">Password</label>
          <input
            id="login-password"
            name="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </div>
        {error ? (
          <div className="app-error" role="alert" aria-live="assertive">
            {error}
          </div>
        ) : null}
        <button type="submit" className="btn" disabled={submitting}>
          {submitting ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
