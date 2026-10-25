import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ApiError, api } from "../api/client";
import type { Role, User } from "../api/types";
import { useAuth } from "../auth";
import { Badge } from "../components/Badge";

export function SettingsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [pwMsg, setPwMsg] = useState<string | null>(null);
  const [pwError, setPwError] = useState<string | null>(null);
  const [pwSubmitting, setPwSubmitting] = useState(false);

  const [users, setUsers] = useState<User[]>([]);
  const [usersError, setUsersError] = useState<string | null>(null);

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("member");
  const [addError, setAddError] = useState<string | null>(null);
  const [addSubmitting, setAddSubmitting] = useState(false);

  const loadUsers = useCallback(async () => {
    try {
      const res = await api.users();
      setUsers(res.users);
      setUsersError(null);
    } catch (err) {
      setUsersError(err instanceof ApiError ? err.message : "Failed to load users");
    }
  }, []);

  useEffect(() => {
    void loadUsers();
  }, [loadUsers]);

  const onPassword = async (e: FormEvent) => {
    e.preventDefault();
    setPwError(null);
    setPwMsg(null);
    setPwSubmitting(true);
    try {
      await api.changePassword({ current, new: next });
      setPwMsg("Password updated.");
      setCurrent("");
      setNext("");
    } catch (err) {
      setPwError(err instanceof ApiError ? err.message : "Failed to change password");
    } finally {
      setPwSubmitting(false);
    }
  };

  const onAddUser = async (e: FormEvent) => {
    e.preventDefault();
    setAddError(null);
    setAddSubmitting(true);
    try {
      await api.createUser({ email, password, role });
      setEmail("");
      setPassword("");
      setRole("member");
      await loadUsers();
    } catch (err) {
      setAddError(err instanceof ApiError ? err.message : "Failed to add user");
    } finally {
      setAddSubmitting(false);
    }
  };

  return (
    <div className="page">
      <h1>Settings</h1>

      <section className="card" style={{ marginBottom: "1.5rem" }}>
        <h2>Change password</h2>
        <form onSubmit={(e) => void onPassword(e)}>
          <div className="field">
            <label htmlFor="current-password">Current password</label>
            <input
              id="current-password"
              name="current"
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              required
            />
          </div>
          <div className="field">
            <label htmlFor="new-password">New password</label>
            <input
              id="new-password"
              name="new"
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              required
            />
          </div>
          {pwError ? (
            <div className="app-error" role="alert" aria-live="assertive">
              {pwError}
            </div>
          ) : null}
          {pwMsg ? (
            <p role="status" aria-live="polite">
              {pwMsg}
            </p>
          ) : null}
          <button type="submit" className="btn" disabled={pwSubmitting}>
            {pwSubmitting ? "Saving…" : "Update password"}
          </button>
        </form>
      </section>

      <section className="card" style={{ marginBottom: "1.5rem" }}>
        <h2>Users</h2>
        {usersError ? (
          <div className="app-error" role="alert" aria-live="assertive">
            {usersError}
          </div>
        ) : null}
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Email</th>
                <th>Role</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id}>
                  <td>{u.email}</td>
                  <td>
                    <Badge variant="info">{u.role}</Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      {isAdmin ? (
        <section className="card">
          <h2>Add user</h2>
          <form onSubmit={(e) => void onAddUser(e)}>
            <div className="field">
              <label htmlFor="new-user-email">Email</label>
              <input
                id="new-user-email"
                name="email"
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
              />
            </div>
            <div className="field">
              <label htmlFor="new-user-password">Password</label>
              <input
                id="new-user-password"
                name="password"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            <div className="field">
              <label htmlFor="new-user-role">Role</label>
              <select
                id="new-user-role"
                name="role"
                value={role}
                onChange={(e) => setRole(e.target.value as Role)}
              >
                <option value="member">member</option>
                <option value="admin">admin</option>
              </select>
            </div>
            {addError ? (
              <div className="app-error" role="alert" aria-live="assertive">
                {addError}
              </div>
            ) : null}
            <button type="submit" className="btn" disabled={addSubmitting}>
              {addSubmitting ? "Adding…" : "Add user"}
            </button>
          </form>
        </section>
      ) : null}
    </div>
  );
}
