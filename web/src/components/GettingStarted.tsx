import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { HostSummary } from "../api/types";
import { usePolling } from "../hooks/usePolling";
import { Icon } from "./icons";

const STORAGE_KEY = "haetae.getting-started.collapsed";

function readCollapsed(): boolean {
  try {
    return window.localStorage.getItem(STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}

function writeCollapsed(v: boolean): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, v ? "1" : "0");
  } catch {
    // storage blocked: the checklist still works, it just forgets the choice
  }
}

interface Progress {
  project: boolean;
  chat: boolean;
  operator: boolean;
}

interface GettingStartedProps {
  hosts: HostSummary[];
  onAddHost: () => void;
}

/**
 * First-run checklist for admins. Each step is computed from real data and the
 * checklist disappears once the three required steps are done. The AI operator
 * step is optional and never keeps the checklist on screen.
 */
export function GettingStarted({ hosts, onAddHost }: GettingStartedProps) {
  const [progress, setProgress] = useState<Progress | null>(null);
  const [collapsed, setCollapsed] = useState(readCollapsed);
  // The bar grows from empty on first paint (CSS transition on width).
  const [grown, setGrown] = useState(false);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    const [projects, accounts, operator] = await Promise.allSettled([
      api.projects(),
      api.chatAccounts(),
      api.operatorSettings(),
    ]);
    if (!mountedRef.current) return;
    setProgress({
      project: projects.status === "fulfilled" && projects.value.projects.length > 0,
      chat: accounts.status === "fulfilled" && accounts.value.accounts.length > 0,
      operator: operator.status === "fulfilled" && operator.value.aiEnabled,
    });
  }, []);

  usePolling(load, 15_000, true);

  const ready = progress !== null;
  useEffect(() => {
    if (!ready) return;
    const id = window.requestAnimationFrame(() => setGrown(true));
    return () => window.cancelAnimationFrame(id);
  }, [ready]);

  if (!progress) return null;

  const hasHost = hosts.length > 0;
  const first = hosts[0];
  const requiredDone = hasHost && progress.project && progress.chat;
  if (requiredDone) return null;

  const steps: Array<{
    id: string;
    title: string;
    text: string;
    done: boolean;
    optional?: boolean;
    action: ReactNode;
  }> = [
    {
      id: "host",
      title: "Add a host",
      text: "Enroll a server with one command.",
      done: hasHost,
      action: (
        <button type="button" className="btn btn-secondary" onClick={onAddHost}>
          Add a host
        </button>
      ),
    },
    {
      id: "deploy",
      title: "Deploy an app",
      text: "Run a Compose app from Git or a pasted file.",
      done: progress.project,
      action: first ? (
        <Link className="btn btn-secondary" to={`/hosts/${first.id}?tab=deploy`}>
          Deploy an app
        </Link>
      ) : (
        <span className="muted">Add a host first</span>
      ),
    },
    {
      id: "chat",
      title: "Connect a chat channel",
      text: "Get incident messages and the daily briefing in Telegram, Slack, email or Signal.",
      done: progress.chat,
      action: (
        <Link className="btn btn-secondary" to="/settings?tab=chat">
          Connect a channel
        </Link>
      ),
    },
    {
      id: "operator",
      title: "Turn on the AI operator",
      text: "Optional. Ask questions in plain language and get explanations for incidents.",
      done: progress.operator,
      optional: true,
      action: (
        <Link className="btn btn-secondary" to="/settings?tab=operator">
          Set up the operator
        </Link>
      ),
    },
  ];
  const doneCount = steps.filter((s) => s.done && !s.optional).length;

  const toggle = () => {
    const next = !collapsed;
    setCollapsed(next);
    writeCollapsed(next);
  };

  return (
    <section className="card getting-started" aria-labelledby="getting-started-title">
      <div className="row getting-started-head">
        <h2 id="getting-started-title">Getting started</h2>
        <span className="muted">{doneCount} of 3 required steps done</span>
        <button
          type="button"
          className="btn btn-ghost"
          aria-expanded={!collapsed}
          aria-controls="getting-started-steps"
          onClick={toggle}
        >
          {collapsed ? "Show checklist" : "Hide checklist"}
        </button>
      </div>
      <div
        className="progress"
        role="progressbar"
        aria-label="Required steps done"
        aria-valuemin={0}
        aria-valuemax={3}
        aria-valuenow={doneCount}
      >
        <span className="progress-fill" style={{ width: `${grown ? Math.round((doneCount / 3) * 100) : 0}%` }} />
      </div>
      {!collapsed ? (
        <ol className="checklist" id="getting-started-steps">
          {steps.map((s) => (
            <li key={s.id} className={`checklist-item${s.done ? " checklist-done" : ""}`}>
              <span className="checklist-mark" aria-hidden="true">
                {s.done ? <Icon name="check" size={14} /> : null}
              </span>
              <div className="checklist-text">
                <strong>{s.title}</strong>
                {s.optional ? <span className="muted"> (optional)</span> : null}
                <span className="sr-only">{s.done ? ": done" : ": not done"}</span>
                <p className="muted">{s.text}</p>
              </div>
              <div className="checklist-action">{s.done ? <span className="muted">Done</span> : s.action}</div>
            </li>
          ))}
        </ol>
      ) : null}
    </section>
  );
}
