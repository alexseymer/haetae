import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { ApiError, api } from "../api/client";
import type {
  AlertSettings,
  ChatAccount,
  ChatChannel,
  LinkableChannel,
  Mute,
} from "../api/m6Types";
import type { Role, User } from "../api/types";
import { useAuth } from "../auth";
import { Badge } from "../components/Badge";
import { channelEnvHint } from "../lib/chatChannels";
import {
  hasAlertSettingsErrors,
  validateAlertSettings,
  type AlertSettingsErrors,
} from "../lib/alertSettings";
import { OperatorSettingsSection } from "./OperatorSettings";
import { formatDateTime, formatRelativeTime } from "../lib/format";

type SettingsTab = "account" | "alerts" | "chat" | "operator";

const TABS: Array<{ id: SettingsTab; label: string }> = [
  { id: "account", label: "Account" },
  { id: "alerts", label: "Alerts" },
  { id: "chat", label: "Chat channels" },
  { id: "operator", label: "AI operator" },
];

const LINKABLE: Array<{ id: LinkableChannel; label: string }> = [
  { id: "telegram", label: "Link Telegram" },
  { id: "slack", label: "Link Slack" },
  { id: "email", label: "Link email" },
  { id: "signal", label: "Link Signal" },
];

const DEFAULT_SETTINGS: AlertSettings = {
  timeZone: "UTC",
  briefingHour: 8,
  cpuPct: 90,
  memPct: 90,
  diskPct: 90,
  sustainedS: 300,
  offlineAfterS: 120,
};

export function SettingsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const [tab, setTab] = useState<SettingsTab>("account");

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

  const [settings, setSettings] = useState<AlertSettings>(DEFAULT_SETTINGS);
  const [settingsErrors, setSettingsErrors] = useState<AlertSettingsErrors>({});
  const [settingsMsg, setSettingsMsg] = useState<string | null>(null);
  const [settingsError, setSettingsError] = useState<string | null>(null);
  const [settingsSubmitting, setSettingsSubmitting] = useState(false);
  const [settingsLoaded, setSettingsLoaded] = useState(false);

  const [mutes, setMutes] = useState<Mute[]>([]);
  const [mutesError, setMutesError] = useState<string | null>(null);

  const [channels, setChannels] = useState<ChatChannel[]>([]);
  const [accounts, setAccounts] = useState<ChatAccount[]>([]);
  const [chatError, setChatError] = useState<string | null>(null);
  const [linkCode, setLinkCode] = useState<string | null>(null);
  const [linkExpiresAt, setLinkExpiresAt] = useState<string | null>(null);
  const [linkInstructions, setLinkInstructions] = useState<string | null>(null);
  const [linkChannel, setLinkChannel] = useState<string | null>(null);
  const [linkError, setLinkError] = useState<string | null>(null);
  const [linkBusy, setLinkBusy] = useState(false);
  const [nowTick, setNowTick] = useState(() => Date.now());

  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    if (!linkExpiresAt) return;
    const t = setInterval(() => setNowTick(Date.now()), 1000);
    return () => clearInterval(t);
  }, [linkExpiresAt]);

  const loadUsers = useCallback(async () => {
    try {
      const res = await api.users();
      if (!mountedRef.current) return;
      setUsers(res.users);
      setUsersError(null);
    } catch (err) {
      if (!mountedRef.current) return;
      setUsersError(err instanceof ApiError ? err.message : "Failed to load users");
    }
  }, []);

  const loadAlerts = useCallback(async () => {
    try {
      const [s, m] = await Promise.all([api.alertSettings(), api.mutes()]);
      if (!mountedRef.current) return;
      setSettings(s);
      setSettingsLoaded(true);
      setMutes(m.mutes);
      setSettingsError(null);
      setMutesError(null);
    } catch (err) {
      if (!mountedRef.current) return;
      setSettingsError(err instanceof ApiError ? err.message : "Failed to load alert settings");
    }
  }, []);

  const loadChat = useCallback(async () => {
    try {
      const [ch, ac] = await Promise.all([api.chatChannels(), api.chatAccounts()]);
      if (!mountedRef.current) return;
      setChannels(ch.channels);
      setAccounts(ac.accounts);
      setChatError(null);
    } catch (err) {
      if (!mountedRef.current) return;
      setChatError(err instanceof ApiError ? err.message : "Failed to load chat settings");
    }
  }, []);

  useEffect(() => {
    void loadUsers();
  }, [loadUsers]);

  useEffect(() => {
    if (tab === "alerts") void loadAlerts();
    if (tab === "chat") void loadChat();
  }, [tab, loadAlerts, loadChat]);

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

  const onSaveSettings = async (e: FormEvent) => {
    e.preventDefault();
    const errors = validateAlertSettings(settings);
    setSettingsErrors(errors);
    if (hasAlertSettingsErrors(errors)) return;
    setSettingsError(null);
    setSettingsMsg(null);
    setSettingsSubmitting(true);
    try {
      const saved = await api.putAlertSettings({
        ...settings,
        timeZone: settings.timeZone.trim(),
      });
      setSettings(saved);
      setSettingsMsg("Alert settings saved.");
    } catch (err) {
      setSettingsError(err instanceof ApiError ? err.message : "Failed to save settings");
    } finally {
      setSettingsSubmitting(false);
    }
  };

  const onRemoveMute = async (id: string) => {
    try {
      await api.deleteMute(id);
      setMutes((prev) => prev.filter((m) => m.id !== id));
    } catch (err) {
      setMutesError(err instanceof ApiError ? err.message : "Failed to remove mute");
    }
  };

  const clearLinkCode = () => {
    setLinkCode(null);
    setLinkExpiresAt(null);
    setLinkInstructions(null);
    setLinkChannel(null);
  };

  const onLink = async (channel: LinkableChannel) => {
    setLinkError(null);
    setLinkBusy(true);
    clearLinkCode();
    try {
      const res = await api.chatLink({ channel });
      // Keep code in React state only — never sessionStorage/localStorage.
      setLinkCode(res.code);
      setLinkExpiresAt(res.expiresAt);
      setLinkInstructions(res.instructions);
      setLinkChannel(channel);
    } catch (err) {
      setLinkError(err instanceof ApiError ? err.message : "Failed to create link code");
    } finally {
      setLinkBusy(false);
    }
  };

  const onUnlink = async (id: string) => {
    try {
      await api.deleteChatAccount(id);
      setAccounts((prev) => prev.filter((a) => a.id !== id));
    } catch (err) {
      setChatError(err instanceof ApiError ? err.message : "Failed to unlink account");
    }
  };

  const onTabListKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const idx = TABS.findIndex((t) => t.id === tab);
    if (idx < 0) return;
    let nextIdx = idx;
    if (e.key === "ArrowRight") {
      nextIdx = (idx + 1) % TABS.length;
    } else if (e.key === "ArrowLeft") {
      nextIdx = (idx - 1 + TABS.length) % TABS.length;
    } else if (e.key === "Home") {
      nextIdx = 0;
    } else if (e.key === "End") {
      nextIdx = TABS.length - 1;
    } else {
      return;
    }
    e.preventDefault();
    const nextTab = TABS[nextIdx]!;
    setTab(nextTab.id);
    const btn = e.currentTarget.querySelector<HTMLElement>(`#tab-settings-${nextTab.id}`);
    btn?.focus();
  };

  const setNum =
    (key: keyof AlertSettings) =>
    (e: ChangeEvent<HTMLInputElement>) => {
      const n = Number(e.target.value);
      setSettings((s) => ({ ...s, [key]: n }));
      setSettingsErrors((prev) => {
        const nextErr = { ...prev };
        delete nextErr[key as keyof AlertSettingsErrors];
        return nextErr;
      });
    };

  const linkRemainingMs = linkExpiresAt
    ? Math.max(0, Date.parse(linkExpiresAt) - nowTick)
    : 0;
  const linkExpired = linkExpiresAt != null && linkRemainingMs <= 0;

  return (
    <div className="page">
      <h1>Settings</h1>

      <div
        className="tabs"
        role="tablist"
        aria-label="Settings sections"
        onKeyDown={onTabListKeyDown}
      >
        {TABS.map(({ id: idTab, label }) => (
          <button
            key={idTab}
            id={`tab-settings-${idTab}`}
            type="button"
            role="tab"
            className="tab"
            aria-selected={tab === idTab}
            aria-controls={`panel-settings-${idTab}`}
            tabIndex={tab === idTab ? 0 : -1}
            onClick={() => setTab(idTab)}
          >
            {label}
          </button>
        ))}
      </div>

      {tab === "account" ? (
        <div
          role="tabpanel"
          id="panel-settings-account"
          aria-labelledby="tab-settings-account"
        >
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
      ) : null}

      {tab === "alerts" ? (
        <div
          role="tabpanel"
          id="panel-settings-alerts"
          aria-labelledby="tab-settings-alerts"
        >
          <section className="card" style={{ marginBottom: "1.5rem" }}>
            <h2>Alert thresholds</h2>
            {!settingsLoaded && !settingsError ? (
              <p className="muted">Loading…</p>
            ) : (
              <form onSubmit={(e) => void onSaveSettings(e)}>
                <fieldset disabled={!isAdmin} className="settings-fieldset">
                  <div className="field">
                    <label htmlFor="alert-tz">Time zone</label>
                    <input
                      id="alert-tz"
                      name="timeZone"
                      value={settings.timeZone}
                      onChange={(e) => {
                        setSettings((s) => ({ ...s, timeZone: e.target.value }));
                        setSettingsErrors((prev) => {
                          const n = { ...prev };
                          delete n.timeZone;
                          return n;
                        });
                      }}
                      required
                    />
                    {settingsErrors.timeZone ? (
                      <p className="field-error" role="alert">
                        {settingsErrors.timeZone}
                      </p>
                    ) : (
                      <p className="field-hint">IANA name, e.g. UTC or Europe/Berlin</p>
                    )}
                  </div>
                  <div className="field">
                    <label htmlFor="alert-hour">Briefing hour (0–23)</label>
                    <input
                      id="alert-hour"
                      name="briefingHour"
                      type="number"
                      min={0}
                      max={23}
                      value={settings.briefingHour}
                      onChange={setNum("briefingHour")}
                      required
                    />
                    {settingsErrors.briefingHour ? (
                      <p className="field-error" role="alert">
                        {settingsErrors.briefingHour}
                      </p>
                    ) : null}
                  </div>
                  <div className="field">
                    <label htmlFor="alert-cpu">CPU %</label>
                    <input
                      id="alert-cpu"
                      name="cpuPct"
                      type="number"
                      min={1}
                      max={100}
                      value={settings.cpuPct}
                      onChange={setNum("cpuPct")}
                      required
                    />
                    {settingsErrors.cpuPct ? (
                      <p className="field-error" role="alert">
                        {settingsErrors.cpuPct}
                      </p>
                    ) : null}
                  </div>
                  <div className="field">
                    <label htmlFor="alert-mem">Memory %</label>
                    <input
                      id="alert-mem"
                      name="memPct"
                      type="number"
                      min={1}
                      max={100}
                      value={settings.memPct}
                      onChange={setNum("memPct")}
                      required
                    />
                    {settingsErrors.memPct ? (
                      <p className="field-error" role="alert">
                        {settingsErrors.memPct}
                      </p>
                    ) : null}
                  </div>
                  <div className="field">
                    <label htmlFor="alert-disk">Disk %</label>
                    <input
                      id="alert-disk"
                      name="diskPct"
                      type="number"
                      min={1}
                      max={100}
                      value={settings.diskPct}
                      onChange={setNum("diskPct")}
                      required
                    />
                    {settingsErrors.diskPct ? (
                      <p className="field-error" role="alert">
                        {settingsErrors.diskPct}
                      </p>
                    ) : null}
                  </div>
                  <div className="field">
                    <label htmlFor="alert-sustained">Sustained seconds</label>
                    <input
                      id="alert-sustained"
                      name="sustainedS"
                      type="number"
                      min={10}
                      max={86400}
                      value={settings.sustainedS}
                      onChange={setNum("sustainedS")}
                      required
                    />
                    {settingsErrors.sustainedS ? (
                      <p className="field-error" role="alert">
                        {settingsErrors.sustainedS}
                      </p>
                    ) : null}
                  </div>
                  <div className="field">
                    <label htmlFor="alert-offline">Offline after seconds</label>
                    <input
                      id="alert-offline"
                      name="offlineAfterS"
                      type="number"
                      min={30}
                      max={86400}
                      value={settings.offlineAfterS}
                      onChange={setNum("offlineAfterS")}
                      required
                    />
                    {settingsErrors.offlineAfterS ? (
                      <p className="field-error" role="alert">
                        {settingsErrors.offlineAfterS}
                      </p>
                    ) : null}
                  </div>
                </fieldset>
                {settingsError ? (
                  <div className="app-error" role="alert" aria-live="assertive">
                    {settingsError}
                  </div>
                ) : null}
                {settingsMsg ? (
                  <p role="status" aria-live="polite">
                    {settingsMsg}
                  </p>
                ) : null}
                {isAdmin ? (
                  <button type="submit" className="btn" disabled={settingsSubmitting}>
                    {settingsSubmitting ? "Saving…" : "Save alert settings"}
                  </button>
                ) : (
                  <p className="muted">Only admins can change alert settings.</p>
                )}
              </form>
            )}
          </section>

          <section className="card">
            <h2>Mutes</h2>
            {mutesError ? (
              <div className="app-error" role="alert" aria-live="assertive">
                {mutesError}
              </div>
            ) : null}
            {mutes.length === 0 ? (
              <p className="muted">No active mutes.</p>
            ) : (
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      <th>Host</th>
                      <th>Rule</th>
                      <th>Until</th>
                      {isAdmin ? <th>Actions</th> : null}
                    </tr>
                  </thead>
                  <tbody>
                    {mutes.map((m) => (
                      <tr key={m.id}>
                        <td>{m.hostId ?? "(org)"}</td>
                        <td>{m.rule || "(all)"}</td>
                        <td title={formatDateTime(m.until)}>
                          {formatRelativeTime(m.until)}
                        </td>
                        {isAdmin ? (
                          <td>
                            <button
                              type="button"
                              className="btn btn-secondary"
                              onClick={() => void onRemoveMute(m.id)}
                            >
                              Remove
                            </button>
                          </td>
                        ) : null}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </div>
      ) : null}

      {tab === "operator" ? (
        <div
          role="tabpanel"
          id="panel-settings-operator"
          aria-labelledby="tab-settings-operator"
        >
          <OperatorSettingsSection isAdmin={isAdmin} />
        </div>
      ) : null}

      {tab === "chat" ? (
        <div
          role="tabpanel"
          id="panel-settings-chat"
          aria-labelledby="tab-settings-chat"
        >
          {chatError ? (
            <div className="app-error" role="alert" aria-live="assertive">
              {chatError}
            </div>
          ) : null}

          <section className="card" style={{ marginBottom: "1.5rem" }}>
            <h2>Channels</h2>
            {channels.length === 0 ? (
              <p className="muted">Loading channels…</p>
            ) : (
              <ul className="channel-list">
                {channels.map((ch) => {
                  const hint = channelEnvHint(ch.name);
                  return (
                    <li key={ch.name}>
                      <strong>{ch.name}</strong>
                      {": "}
                      {ch.enabled ? (
                        <Badge variant="online">enabled</Badge>
                      ) : (
                        <>
                          <Badge variant="offline">not configured</Badge>
                          {hint ? (
                            <span className="muted">
                              {" "}
                              Set env var {hint} on the server.
                            </span>
                          ) : null}
                        </>
                      )}
                    </li>
                  );
                })}
              </ul>
            )}
          </section>

          <section className="card" style={{ marginBottom: "1.5rem" }}>
            <h2>Linked accounts</h2>
            {accounts.length === 0 ? (
              <p className="muted">No linked accounts.</p>
            ) : (
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      <th>Channel</th>
                      <th>External id</th>
                      <th>Linked</th>
                      <th>Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {accounts.map((a) => (
                      <tr key={a.id}>
                        <td>{a.channel}</td>
                        <td className="mono">{a.externalId}</td>
                        <td title={formatDateTime(a.createdAt)}>
                          {formatRelativeTime(a.createdAt)}
                        </td>
                        <td>
                          <button
                            type="button"
                            className="btn btn-secondary"
                            onClick={() => void onUnlink(a.id)}
                          >
                            Unlink
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>

          <section className="card">
            <h2>Link a channel</h2>
            <div className="row" style={{ marginBottom: "1rem" }}>
              {LINKABLE.map(({ id: ch, label }) => (
                <button
                  key={ch}
                  type="button"
                  className="btn btn-secondary"
                  disabled={linkBusy}
                  onClick={() => void onLink(ch)}
                >
                  {label}
                </button>
              ))}
            </div>
            {linkError ? (
              <div className="app-error" role="alert" aria-live="assertive">
                {linkError}
              </div>
            ) : null}
            {linkCode && linkExpiresAt ? (
              <div className="link-code-panel">
                <p>
                  One-time code for {linkChannel}:{" "}
                  <code className="mono link-code">{linkCode}</code>
                </p>
                <p className="muted" aria-live="polite">
                  {linkExpired
                    ? "Code expired."
                    : `Expires in ${formatCountdown(linkRemainingMs)} (${formatDateTime(linkExpiresAt)}).`}
                </p>
                {linkInstructions ? (
                  <pre className="mono">{linkInstructions}</pre>
                ) : null}
                <button type="button" className="btn btn-ghost" onClick={clearLinkCode}>
                  Clear code
                </button>
              </div>
            ) : null}
          </section>
        </div>
      ) : null}
    </div>
  );
}

function formatCountdown(ms: number): string {
  const totalSec = Math.ceil(ms / 1000);
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}
