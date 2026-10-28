import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { ApiError, api } from "../api/client";
import type { ChatEvidence, ChatMessage } from "../api/m6Types";
import { Link } from "react-router-dom";
import { useAuth } from "../auth";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { usePolling } from "../hooks/usePolling";
import { Badge } from "../components/Badge";
import { actionNoteText, groupByDay, isActionNote, kindLabel } from "../lib/chatFeed";
import { severityVariant } from "../lib/severity";
import { isSafeEvidencePath } from "../lib/evidenceLink";
import { formatDateTime, formatRelativeTime } from "../lib/format";
import { INTROS } from "../lib/help";

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
  const [noChannel, setNoChannel] = useState(false);
  const [aiOff, setAiOff] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [awaiting, setAwaiting] = useState(false);
  const answersAtSend = useRef(0);

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
    // Best effort: only used to suggest linking a channel.
    void api
      .chatAccounts()
      .then((res) => {
        if (mountedRef.current) setNoChannel(res.accounts.length === 0);
      })
      .catch(() => undefined);
    void api
      .operatorSettings()
      .then((res) => {
        if (mountedRef.current) setAiOff(!res.available);
      })
      .catch(() => undefined);
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
      setLoaded(true);
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

  const answerCount = messages.filter((m) => m.kind === "answer").length;
  useEffect(() => {
    if (awaiting && answerCount > answersAtSend.current) setAwaiting(false);
  }, [awaiting, answerCount]);
  useEffect(() => {
    if (!awaiting) return;
    const t = window.setTimeout(() => setAwaiting(false), 120_000);
    return () => window.clearTimeout(t);
  }, [awaiting]);

  const onSend = async (e: FormEvent) => {
    e.preventDefault();
    const text = body.trim();
    if (!text) return;
    setComposerError(null);
    setSending(true);
    answersAtSend.current = answerCount;
    try {
      const res = await api.postChatMessage({ body: text });
      setBody("");
      if (res.reply) {
        setActionReplies((prev) => ({ ...prev, __composer: res.reply! }));
      } else {
        setActionReplies((prev) => {
          const { __composer: _drop, ...rest } = prev;
          return rest;
        });
        setAwaiting(true);
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
      setUsedActions((prev) => {
        const next = { ...prev, [key]: true as const };
        // Approve and decline are one decision: pressing one retires both.
        if (action === "approve_action") next[`${messageId}:decline_action`] = true;
        if (action === "decline_action") next[`${messageId}:approve_action`] = true;
        return next;
      });
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

  const renderTime = (m: ChatMessage) => (
    <time dateTime={m.createdAt} title={formatDateTime(m.createdAt)}>
      {formatRelativeTime(m.createdAt)}
    </time>
  );

  const renderEvidence = (m: ChatMessage, card: boolean) =>
    m.evidence && m.evidence.length > 0 ? (
      <ul className={card ? "chat-evidence chat-evidence-inline" : "chat-evidence"}>
        {m.evidence.map((ev: ChatEvidence, i) => {
          const safe = isSafeEvidencePath(ev.path);
          const label = card ? cardLinkLabel(ev.label) : ev.label || ev.path;
          return (
            <li key={`${m.id}-ev-${i}`}>
              {safe ? (
                <a href={ev.path}>{label}</a>
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
    ) : null;

  const renderActions = (m: ChatMessage) => (
    <>
      {isAdmin && m.actions && m.actions.length > 0 ? (
        <div className="chat-actions row" role="group" aria-label="Actions">
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
      {!isAdmin && m.actions?.some((a) => a.id === "approve_action") ? (
        <p className="muted">Ask an administrator to approve.</p>
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
    </>
  );

  const renderMessage = (m: ChatMessage) => {
    if (isActionNote(m)) {
      return (
        <p key={m.id} className="chat-note muted" data-direction={m.direction}>
          {actionNoteText(m.body)} · {renderTime(m)}
        </p>
      );
    }
    if (m.kind === "incident" && m.meta) {
      const meta = m.meta;
      const resolved = meta.state === "resolved";
      return (
        <article
          key={m.id}
          className={`chat-msg chat-msg-incident chat-card${resolved ? " chat-card-resolved" : ""}`}
          data-direction={m.direction}
        >
          <header className="chat-card-head">
            <Badge variant={severityVariant(meta.severity)}>
              {meta.severity.toUpperCase()}
            </Badge>
            {resolved ? <Badge variant="online">Resolved</Badge> : null}
            <strong className="chat-card-title">{meta.title}</strong>
            <span className="chat-msg-meta-time muted">{renderTime(m)}</span>
          </header>
          {meta.host ? <p className="chat-card-host muted">Host: {meta.host}</p> : null}
          {meta.summary && meta.summary.length > 0 ? (
            <ul className="chat-card-summary">
              {meta.summary.map((line, i) => (
                <li key={`${m.id}-s-${i}`}>{line}</li>
              ))}
            </ul>
          ) : null}
          {renderEvidence(m, true)}
          {renderActions(m)}
        </article>
      );
    }
    const kindClass =
      m.kind === "incident"
        ? "chat-msg chat-msg-incident"
        : m.kind === "briefing"
          ? "chat-msg chat-msg-briefing"
          : m.kind === "answer"
            ? "chat-msg chat-msg-answer"
            : "chat-msg";
    return (
      <article
        key={m.id}
        className={`${kindClass}${m.direction === "in" ? " chat-msg-mine" : ""}`}
        data-direction={m.direction}
      >
        <header className="chat-msg-meta muted">
          <span className="chat-kind">{kindLabel(m)}</span>
          {renderTime(m)}
        </header>
        <div className="chat-msg-body">{m.body}</div>
        {renderEvidence(m, false)}
        {renderActions(m)}
      </article>
    );
  };

  return (
    <div className="page chat-page">
      <PageHeader title="Chat" intro={INTROS.chat} />

      {noChannel ? (
        <p className="muted" role="note">
          No chat channel is linked to your account yet, so incident messages only show up here.{" "}
          <Link to="/settings?tab=chat">Link Telegram, Slack, email or Signal</Link> to get them
          in your chat app too.
        </p>
      ) : null}

      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}

      <div className="chat-log card" role="log" aria-live="polite" aria-label="Chat feed" ref={listRef}>
        {!loaded && !error ? (
          <p className="muted" role="status">
            Loading messages…
          </p>
        ) : null}
        {loaded && messages.length === 0 && !error ? (
          <EmptyState title="Nothing here yet">
            Alerts, the daily briefing and your questions show up here. Ask something below, for
            example "why was shop-api slow this morning?".
          </EmptyState>
        ) : null}
        {groupByDay(messages).map((group) => (
          <section key={group.key} className="chat-day-group" aria-label={group.label}>
            <p className="chat-day" role="separator">
              <span>{group.label}</span>
            </p>
            {group.messages.map((m) => renderMessage(m))}
          </section>
        ))}
        {awaiting || sending ? (
          <p className="chat-thinking muted" role="status">
            Thinking...
          </p>
        ) : null}
      </div>

      {actionReplies.__composer ? (
        <p className="muted" role="status">
          {actionReplies.__composer}
        </p>
      ) : null}

      <form className="chat-composer card" onSubmit={(e) => void onSend(e)}>
        <div className="field">
          <label htmlFor="chat-body">Ask the operator</label>
          <textarea
            id="chat-body"
            name="body"
            rows={3}
            value={body}
            placeholder="Ask about your servers, for example: why was shop-api slow this morning?"
            aria-describedby={aiOff ? "chat-ai-off" : undefined}
            onChange={(e) => setBody(e.target.value)}
            required
          />
        </div>
        {aiOff ? (
          <p id="chat-ai-off" className="muted" role="note">
            The operator is off or not configured: you can still read alerts here. Turn it on in{" "}
            <Link to="/settings?tab=operator">Settings &gt; AI operator</Link>.
          </p>
        ) : null}
        {composerError ? (
          <div className="app-error" role="alert" aria-live="assertive">
            {composerError}
          </div>
        ) : null}
        <div className="row">
          <button type="submit" className="btn" disabled={sending || awaiting || !body.trim()}>
            {sending ? "Sending…" : "Send"}
          </button>
        </div>
      </form>
    </div>
  );
}

function cardLinkLabel(label: string): string {
  if (label === "Host") return "View host";
  if (label === "Incident") return "View incident";
  return label;
}
