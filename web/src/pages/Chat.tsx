import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { ApiError, api } from "../api/client";
import type { ChatMessage } from "../api/m6Types";
import { useAuth } from "../auth";
import { usePolling } from "../hooks/usePolling";
import { isSafeEvidencePath } from "../lib/evidenceLink";
import { formatDateTime, formatRelativeTime } from "../lib/format";

export function ChatPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [body, setBody] = useState("");
  const [composerError, setComposerError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [usedActions, setUsedActions] = useState<Record<string, true>>({});
  const [actionReplies, setActionReplies] = useState<Record<string, string>>({});
  const [actionBusy, setActionBusy] = useState<string | null>(null);

  const mountedRef = useRef(true);
  const genRef = useRef(0);
  const cursorRef = useRef<string | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const initialLoadDone = useRef(false);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    cursorRef.current = cursor;
  }, [cursor]);

  const load = useCallback(async () => {
    const gen = ++genRef.current;
    const after = cursorRef.current ?? undefined;
    try {
      const res = await api.chatMessages({
        after,
        limit: initialLoadDone.current ? 50 : 100,
      });
      if (gen !== genRef.current || !mountedRef.current) return;
      setError(null);
      if (!initialLoadDone.current) {
        setMessages(res.messages);
        initialLoadDone.current = true;
      } else if (res.messages.length > 0) {
        setMessages((prev) => {
          const seen = new Set(prev.map((m) => m.id));
          const added = res.messages.filter((m) => !seen.has(m.id));
          return added.length ? [...prev, ...added] : prev;
        });
      }
      const nextCursor =
        res.next ??
        (res.messages.length > 0 ? res.messages[res.messages.length - 1]!.id : null);
      if (nextCursor) {
        setCursor(nextCursor);
        cursorRef.current = nextCursor;
      } else if (!after && res.messages.length > 0) {
        const last = res.messages[res.messages.length - 1]!.id;
        setCursor(last);
        cursorRef.current = last;
      }
    } catch (err) {
      if (gen !== genRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load messages");
    }
  }, []);

  usePolling(load, 3_000, true);

  useEffect(() => {
    const el = listRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
  }, [messages]);

  const onSend = async (e: FormEvent) => {
    e.preventDefault();
    const text = body.trim();
    if (!text) return;
    setComposerError(null);
    setSending(true);
    try {
      const res = await api.postChatMessage({ body: text });
      setBody("");
      if (res.reply) {
        setActionReplies((prev) => ({ ...prev, __composer: res.reply! }));
      }
      await load();
    } catch (err) {
      setComposerError(err instanceof ApiError ? err.message : "Failed to send");
    } finally {
      setSending(false);
    }
  };

  const onAction = async (messageId: string, action: string) => {
    const key = `${messageId}:${action}`;
    if (usedActions[key]) return;
    setActionBusy(key);
    try {
      const res = await api.chatAction({ messageId, action });
      setUsedActions((prev) => ({ ...prev, [key]: true }));
      setActionReplies((prev) => ({ ...prev, [key]: res.reply }));
    } catch (err) {
      setActionReplies((prev) => ({
        ...prev,
        [key]: err instanceof ApiError ? err.message : "Action failed",
      }));
    } finally {
      setActionBusy(null);
    }
  };

  return (
    <div className="page chat-page">
      <h1>Chat</h1>

      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}

      <div className="chat-log card" role="log" aria-live="polite" ref={listRef}>
        {messages.length === 0 && !error ? (
          <p className="muted">No messages yet.</p>
        ) : null}
        {messages.map((m) => {
          const kindClass =
            m.kind === "incident"
              ? "chat-msg chat-msg-incident"
              : m.kind === "briefing"
                ? "chat-msg chat-msg-briefing"
                : "chat-msg";
          return (
            <article key={m.id} className={kindClass} data-direction={m.direction}>
              <header className="chat-msg-meta muted">
                <span>{m.kind}</span>
                <span title={formatDateTime(m.createdAt)}>
                  {formatRelativeTime(m.createdAt)}
                </span>
              </header>
              <div className="chat-msg-body">{m.body}</div>
              {m.evidence && m.evidence.length > 0 ? (
                <ul className="chat-evidence">
                  {m.evidence.map((ev, i) => {
                    const safe = isSafeEvidencePath(ev.path);
                    return (
                      <li key={`${m.id}-ev-${i}`}>
                        {safe ? (
                          <a href={ev.path}>{ev.label || ev.path}</a>
                        ) : (
                          <span>
                            {ev.label ? `${ev.label}: ` : ""}
                            {ev.path}
                          </span>
                        )}
                      </li>
                    );
                  })}
                </ul>
              ) : null}
              {isAdmin && m.actions && m.actions.length > 0 ? (
                <div className="chat-actions row">
                  {m.actions.map((act) => {
                    const key = `${m.id}:${act.id}`;
                    const used = !!usedActions[key];
                    return (
                      <button
                        key={act.id}
                        type="button"
                        className="btn btn-secondary"
                        disabled={used || actionBusy === key}
                        onClick={() => void onAction(m.id, act.id)}
                      >
                        {act.label}
                      </button>
                    );
                  })}
                </div>
              ) : null}
              {m.actions?.map((act) => {
                const key = `${m.id}:${act.id}`;
                const reply = actionReplies[key];
                return reply ? (
                  <p key={key} className="chat-action-reply muted" role="status">
                    {reply}
                  </p>
                ) : null;
              })}
            </article>
          );
        })}
      </div>

      {actionReplies.__composer ? (
        <p className="muted" role="status">
          {actionReplies.__composer}
        </p>
      ) : null}

      <form className="chat-composer card" onSubmit={(e) => void onSend(e)}>
        <div className="field">
          <label htmlFor="chat-body">Message</label>
          <textarea
            id="chat-body"
            name="body"
            rows={3}
            value={body}
            onChange={(e) => setBody(e.target.value)}
            required
          />
        </div>
        {composerError ? (
          <div className="app-error" role="alert" aria-live="assertive">
            {composerError}
          </div>
        ) : null}
        <button type="submit" className="btn" disabled={sending || !body.trim()}>
          {sending ? "Sending…" : "Send"}
        </button>
      </form>
    </div>
  );
}
