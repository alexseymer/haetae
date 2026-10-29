import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, api } from "../api/client";
import type { DeployLogLine, DeployStatus } from "../api/types";
import { usePolling } from "../hooks/usePolling";
import { stripAnsi } from "../lib/ansi";
import { useToast } from "./Toast";

const MAX_VISIBLE = 5000;
const POLL_MS = 1500;
const WITHHELD = "[output withheld: redaction failed]";

interface DeployLogViewerProps {
  deployId: string;
  /** When true, stop polling even if the last response said not done. */
  forceDone?: boolean;
}

function lineText(line: DeployLogLine): string {
  if (line.withheld) return WITHHELD;
  const cleaned = stripAnsi(line.text);
  if (line.stream === "stderr") return `stderr: ${cleaned}`;
  return cleaned;
}

export function DeployLogViewer({ deployId, forceDone = false }: DeployLogViewerProps) {
  const [lines, setLines] = useState<DeployLogLine[]>([]);
  const [done, setDone] = useState(false);
  const [status, setStatus] = useState<DeployStatus | string>("");
  const [error, setError] = useState<string | null>(null);
  const [showAll, setShowAll] = useState(false);
  const [stickBottom, setStickBottom] = useState(true);
  const [copied, setCopied] = useState(false);
  const toast = useToast();

  const afterRef = useRef(0);
  const genRef = useRef(0);
  const mountedRef = useRef(true);
  const logRef = useRef<HTMLPreElement>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    setLines([]);
    setDone(false);
    setStatus("");
    setError(null);
    setShowAll(false);
    setStickBottom(true);
    afterRef.current = 0;
    genRef.current += 1;
  }, [deployId]);

  const load = useCallback(async () => {
    if (!deployId) return;
    const gen = genRef.current;
    const after = afterRef.current;
    try {
      const res = await api.deployLogs(deployId, after);
      if (gen !== genRef.current || !mountedRef.current) return;
      if (res.logs.length > 0) {
        setLines((prev) => {
          const next = [...prev];
          for (const l of res.logs) {
            if (!next.some((x) => x.seq === l.seq)) next.push(l);
          }
          next.sort((a, b) => a.seq - b.seq);
          return next;
        });
        const last = res.logs[res.logs.length - 1];
        if (last && last.seq >= afterRef.current) {
          afterRef.current = last.seq;
        }
      }
      setStatus(res.status);
      if (res.done || forceDone) setDone(true);
      setError(null);
    } catch (err) {
      if (gen !== genRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load logs");
    }
  }, [deployId, forceDone]);

  const polling = !done && !forceDone && !!deployId;
  usePolling(load, POLL_MS, polling, deployId);

  useEffect(() => {
    if (!stickBottom || !logRef.current) return;
    logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [lines, stickBottom]);

  const onScroll = () => {
    const el = logRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    setStickBottom(atBottom);
  };

  const visible = showAll ? lines : lines.slice(-MAX_VISIBLE);
  const hiddenCount = lines.length - visible.length;
  const text = visible.map(lineText).join("\n");

  const onCopy = async () => {
    try {
      await navigator.clipboard?.writeText(text);
      setCopied(true);
      toast.notify("Build log copied to clipboard");
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      // ignore
    }
  };

  return (
    <div className="deploy-log">
      <div className="row" style={{ justifyContent: "space-between" }}>
        <h3 style={{ margin: 0 }}>Build log</h3>
        <div className="row">
          {!stickBottom ? (
            <button
              type="button"
              className="btn btn-secondary"
              onClick={() => {
                setStickBottom(true);
                if (logRef.current) {
                  logRef.current.scrollTop = logRef.current.scrollHeight;
                }
              }}
            >
              Jump to latest
            </button>
          ) : null}
          <button type="button" className="btn btn-secondary" onClick={() => void onCopy()}>
            {copied ? "Copied" : "Copy log"}
          </button>
        </div>
      </div>
      {error ? (
        <div className="app-error" role="alert">
          {error}
        </div>
      ) : null}
      {hiddenCount > 0 && !showAll ? (
        <p>
          <button type="button" className="btn btn-ghost" onClick={() => setShowAll(true)}>
            Show all ({lines.length} lines)
          </button>
        </p>
      ) : null}
      <pre
        ref={logRef}
        className="deploy-log-view mono"
        role="log"
        aria-live="off"
        aria-label="Deploy build log"
        onScroll={onScroll}
      >
        {text || (done ? "(no output)" : "Waiting for output…")}
      </pre>
      {status ? (
        <p className="muted" style={{ fontSize: "0.85rem" }}>
          Status: {status}
          {done ? " · complete" : " · streaming"}
        </p>
      ) : null}
    </div>
  );
}
