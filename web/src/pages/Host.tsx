import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type {
  Container,
  HostSummary,
  LogEvent,
  LogLevelFilter,
  LogStats,
  MetricPoint,
} from "../api/types";
import { Badge, OnlineBadge } from "../components/Badge";
import { Chart } from "../components/Chart";
import { usePolling } from "../hooks/usePolling";
import { formatDateTime, formatPercent, formatRelativeTime, withheldPercent } from "../lib/format";
import { HostDeployTab } from "./HostDeployTab";

type Tab = "overview" | "containers" | "logs" | "deploy";

const TABS: Array<{ id: Tab; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "containers", label: "Containers" },
  { id: "logs", label: "Logs" },
  { id: "deploy", label: "Deploy" },
];

const RANGES: Array<{ id: string; label: string; ms: number }> = [
  { id: "1h", label: "1h", ms: 3600_000 },
  { id: "6h", label: "6h", ms: 6 * 3600_000 },
  { id: "24h", label: "24h", ms: 24 * 3600_000 },
  { id: "7d", label: "7d", ms: 7 * 24 * 3600_000 },
  { id: "30d", label: "30d", ms: 30 * 24 * 3600_000 },
];

function pointsOf(pts: MetricPoint[]): Array<{ t: number; v: number }> {
  return pts.map((p) => ({ t: p.t, v: p.v }));
}

function levelBadge(level: string) {
  const l = level.toLowerCase();
  if (l === "error" || l === "fatal") return <Badge variant="danger">{level}</Badge>;
  if (l === "warn" || l === "warning") return <Badge variant="warn">{level}</Badge>;
  return <Badge variant="info">{level}</Badge>;
}

export function HostPage() {
  const { id = "" } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const tabParam = searchParams.get("tab");
  const tab: Tab =
    tabParam === "containers" ||
    tabParam === "logs" ||
    tabParam === "overview" ||
    tabParam === "deploy"
      ? tabParam
      : "overview";

  const [host, setHost] = useState<HostSummary | null>(null);
  const [logStats, setLogStats] = useState<LogStats | null>(null);
  const [openIncidentCount, setOpenIncidentCount] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [rangeId, setRangeId] = useState("1h");
  const rangeMs = RANGES.find((r) => r.id === rangeId)?.ms ?? 3600_000;

  const [metrics, setMetrics] = useState<Record<string, MetricPoint[]>>({});
  const [metricsLoading, setMetricsLoading] = useState(false);
  const [metricsError, setMetricsError] = useState<string | null>(null);

  const [containers, setContainers] = useState<Container[]>([]);
  const [selectedCid, setSelectedCid] = useState<string | null>(null);
  const [cMetrics, setCMetrics] = useState<Record<string, MetricPoint[]>>({});
  const [cMetricsLoading, setCMetricsLoading] = useState(false);

  const [logLevel, setLogLevel] = useState<LogLevelFilter>("warn");
  const [events, setEvents] = useState<LogEvent[]>([]);
  const [logsError, setLogsError] = useState<string | null>(null);

  const mountedRef = useRef(true);
  const metricsGenRef = useRef(0);
  const metricsLoadedOnceRef = useRef(false);
  const logsGenRef = useRef(0);
  const cMetricsGenRef = useRef(0);
  const containersGenRef = useRef(0);
  const hostGenRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const setTab = (next: Tab) => {
    setSearchParams({ tab: next }, { replace: true });
  };

  const loadHost = useCallback(async () => {
    const gen = ++hostGenRef.current;
    try {
      const res = await api.host(id);
      if (gen !== hostGenRef.current || !mountedRef.current) return;
      setHost(res.host);
      setLogStats(res.logStats);
      setError(null);
    } catch (err) {
      if (gen !== hostGenRef.current || !mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load host");
      return;
    }
    try {
      const incs = await api.incidents({ state: "open", hostId: id, limit: 10 });
      if (gen !== hostGenRef.current || !mountedRef.current) return;
      setOpenIncidentCount(incs.incidents.length);
    } catch {
      if (gen !== hostGenRef.current || !mountedRef.current) return;
      setOpenIncidentCount(0);
    }
  }, [id]);

  useEffect(() => {
    void loadHost();
  }, [loadHost]);

  const loadMetrics = useCallback(async () => {
    if (!id) return;
    const gen = ++metricsGenRef.current;
    if (!metricsLoadedOnceRef.current) setMetricsLoading(true);
    setMetricsError(null);
    const to = Date.now();
    const from = to - rangeMs;
    const names = [
      "cpu_pct",
      "load1",
      "mem_used",
      "mem_total",
      "disk_used",
      "disk_total",
      "net_rx_bps",
      "net_tx_bps",
    ] as const;
    try {
      const results = await Promise.all(
        names.map(async (m) => {
          const res = await api.hostMetrics(id, m, from, to);
          return [m, res.points] as const;
        }),
      );
      if (gen !== metricsGenRef.current || !mountedRef.current) return;
      const next: Record<string, MetricPoint[]> = {};
      for (const [m, pts] of results) next[m] = pts;
      setMetrics(next);
    } catch (err) {
      if (gen !== metricsGenRef.current || !mountedRef.current) return;
      setMetricsError(err instanceof ApiError ? err.message : "Failed to load metrics");
    } finally {
      if (gen === metricsGenRef.current && mountedRef.current) {
        setMetricsLoading(false);
        metricsLoadedOnceRef.current = true;
      }
    }
  }, [id, rangeMs]);

  useEffect(() => {
    metricsLoadedOnceRef.current = false;
  }, [id, rangeMs]);

  usePolling(loadMetrics, 10_000, tab === "overview" && !!id, `${id}:${rangeMs}`);

  useEffect(() => {
    if (tab !== "containers" || !id) return;
    const gen = ++containersGenRef.current;
    void api
      .containers(id)
      .then((res) => {
        if (gen !== containersGenRef.current || !mountedRef.current) return;
        setContainers(res.containers);
      })
      .catch((err: unknown) => {
        if (gen !== containersGenRef.current || !mountedRef.current) return;
        setError(err instanceof ApiError ? err.message : "Failed to load containers");
      });
  }, [tab, id]);

  useEffect(() => {
    if (!selectedCid || !id) {
      setCMetrics({});
      return;
    }
    const gen = ++cMetricsGenRef.current;
    const load = async () => {
      setCMetricsLoading(true);
      const to = Date.now();
      const from = to - rangeMs;
      const names = [
        "cpu_pct",
        "mem_usage",
        "mem_limit",
        "net_rx_bps",
        "net_tx_bps",
        "blk_read_bps",
        "blk_write_bps",
      ] as const;
      try {
        const results = await Promise.all(
          names.map(async (m) => {
            const res = await api.containerMetrics(id, selectedCid, m, from, to);
            return [m, res.points] as const;
          }),
        );
        if (gen !== cMetricsGenRef.current || !mountedRef.current) return;
        const next: Record<string, MetricPoint[]> = {};
        for (const [m, pts] of results) next[m] = pts;
        setCMetrics(next);
      } catch {
        if (gen !== cMetricsGenRef.current || !mountedRef.current) return;
        setCMetrics({});
      } finally {
        if (gen === cMetricsGenRef.current && mountedRef.current) {
          setCMetricsLoading(false);
        }
      }
    };
    void load();
  }, [selectedCid, id, rangeMs]);

  const loadLogs = useCallback(async () => {
    if (!id) return;
    const gen = ++logsGenRef.current;
    try {
      const res = await api.logs(id, logLevel, 100);
      if (gen !== logsGenRef.current || !mountedRef.current) return;
      setEvents(res.events);
      setLogsError(null);
    } catch (err: unknown) {
      if (gen !== logsGenRef.current || !mountedRef.current) return;
      setLogsError(err instanceof ApiError ? err.message : "Failed to load logs");
    }
  }, [id, logLevel]);

  usePolling(loadLogs, 10_000, tab === "logs" && !!id, `${id}:${logLevel}`);

  const byStack = useMemo(() => {
    const map = new Map<string, Container[]>();
    for (const c of containers) {
      const key = c.stack || "(no stack)";
      const list = map.get(key) ?? [];
      list.push(c);
      map.set(key, list);
    }
    return [...map.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [containers]);

  const containerNameById = useMemo(() => {
    const m = new Map<string, string>();
    for (const c of containers) m.set(c.id, c.name);
    return m;
  }, [containers]);

  const memTotalPts = metrics.mem_total ?? [];
  const diskTotalPts = metrics.disk_total ?? [];
  const memTotalMax = memTotalPts.length > 0 ? memTotalPts[memTotalPts.length - 1]?.v : undefined;
  const diskTotalMax = diskTotalPts.length > 0 ? diskTotalPts[diskTotalPts.length - 1]?.v : undefined;
  const selected = containers.find((c) => c.id === selectedCid) ?? null;

  const onTabListKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const idx = TABS.findIndex((t) => t.id === tab);
    if (idx < 0) return;
    let next = idx;
    if (e.key === "ArrowRight") {
      next = (idx + 1) % TABS.length;
    } else if (e.key === "ArrowLeft") {
      next = (idx - 1 + TABS.length) % TABS.length;
    } else if (e.key === "Home") {
      next = 0;
    } else if (e.key === "End") {
      next = TABS.length - 1;
    } else {
      return;
    }
    e.preventDefault();
    const nextTab = TABS[next]!;
    setTab(nextTab.id);
    const btn = e.currentTarget.querySelector<HTMLElement>(`#tab-${nextTab.id}`);
    btn?.focus();
  };

  return (
    <div className="page">
      <p>
        <Link to="/">← Hosts</Link>
      </p>
      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}
      {host ? (
        <>
          <div className="row" style={{ marginBottom: "0.5rem" }}>
            <h1 style={{ margin: 0 }}>{host.name}</h1>
            <OnlineBadge online={host.connected} />
            {openIncidentCount > 0 ? (
              <Link to="/incidents" className="incident-count-badge">
                {openIncidentCount} open{" "}
                {openIncidentCount === 1 ? "incident" : "incidents"}
              </Link>
            ) : null}
          </div>
          <p className="muted">
            {host.hostname} · {host.os} / {host.kernel} / {host.arch} · Docker{" "}
            {host.dockerVersion} · agent {host.agentVersion}
          </p>
        </>
      ) : (
        <p className="muted">Loading…</p>
      )}

      <div
        className="tabs"
        role="tablist"
        aria-label="Host sections"
        onKeyDown={onTabListKeyDown}
      >
        {TABS.map(({ id: idTab, label }) => (
          <button
            key={idTab}
            id={`tab-${idTab}`}
            type="button"
            role="tab"
            className="tab"
            aria-selected={tab === idTab}
            aria-controls={`panel-${idTab}`}
            tabIndex={tab === idTab ? 0 : -1}
            onClick={() => setTab(idTab)}
          >
            {label}
          </button>
        ))}
      </div>

      {tab === "overview" ? (
        <div
          className="stack"
          role="tabpanel"
          id="panel-overview"
          aria-labelledby="tab-overview"
        >
          <div className="range-select" role="group" aria-label="Time range">
            {RANGES.map((r) => (
              <button
                key={r.id}
                type="button"
                className="btn btn-secondary"
                aria-pressed={rangeId === r.id}
                onClick={() => setRangeId(r.id)}
              >
                {r.label}
              </button>
            ))}
          </div>
          <div className="grid-charts">
            <Chart
              title="CPU %"
              unit="%"
              yMax={100}
              loading={metricsLoading}
              error={metricsError}
              series={[
                {
                  label: "CPU",
                  points: pointsOf(metrics.cpu_pct ?? []),
                  color: "var(--color-chart-1)",
                },
              ]}
            />
            <Chart
              title="Load"
              unit="plain"
              loading={metricsLoading}
              error={metricsError}
              series={[
                {
                  label: "load1",
                  points: pointsOf(metrics.load1 ?? []),
                  color: "var(--color-chart-2)",
                },
              ]}
            />
            <Chart
              title="Memory"
              unit="bytes"
              yMax={memTotalMax}
              loading={metricsLoading}
              error={metricsError}
              series={[
                {
                  label: "Used",
                  points: pointsOf(metrics.mem_used ?? []),
                  color: "var(--color-chart-1)",
                },
                {
                  label: "Total",
                  points: pointsOf(metrics.mem_total ?? []),
                  color: "var(--color-chart-2)",
                },
              ]}
            />
            <Chart
              title="Disk"
              unit="bytes"
              yMax={diskTotalMax}
              loading={metricsLoading}
              error={metricsError}
              series={[
                {
                  label: "Used",
                  points: pointsOf(metrics.disk_used ?? []),
                  color: "var(--color-chart-3)",
                },
                {
                  label: "Total",
                  points: pointsOf(metrics.disk_total ?? []),
                  color: "var(--color-chart-2)",
                },
              ]}
            />
            <Chart
              title="Network"
              unit="bytes/s"
              loading={metricsLoading}
              error={metricsError}
              series={[
                {
                  label: "In",
                  points: pointsOf(metrics.net_rx_bps ?? []),
                  color: "var(--color-chart-1)",
                },
                {
                  label: "Out",
                  points: pointsOf(metrics.net_tx_bps ?? []),
                  color: "var(--color-chart-4)",
                },
              ]}
            />
          </div>
          <div className="card">
            <h2>Log pipeline</h2>
            {logStats ? (
              <div className="stat-grid">
                <div className="stat">
                  <div className="stat-label">Lines in</div>
                  <div className="stat-value">{logStats.linesIn}</div>
                </div>
                <div className="stat">
                  <div className="stat-label">Withheld</div>
                  <div className="stat-value">
                    {formatPercent(withheldPercent(logStats.linesIn, logStats.linesSkipped))}
                  </div>
                </div>
                <div className="stat">
                  <div className="stat-label">Events sent</div>
                  <div className="stat-value">{logStats.eventsOut}</div>
                </div>
                <div className="stat">
                  <div className="stat-label">Redaction failures</div>
                  <div className="stat-value">{logStats.redactionFailures}</div>
                </div>
                <div className="stat">
                  <div className="stat-label">Dropped</div>
                  <div className="stat-value">{logStats.linesDropped}</div>
                </div>
              </div>
            ) : (
              <p className="muted">No pipeline stats yet.</p>
            )}
          </div>
        </div>
      ) : null}

      {tab === "containers" ? (
        <div
          className="stack"
          role="tabpanel"
          id="panel-containers"
          aria-labelledby="tab-containers"
        >
          <div className="range-select" role="group" aria-label="Time range">
            {RANGES.map((r) => (
              <button
                key={r.id}
                type="button"
                className="btn btn-secondary"
                aria-pressed={rangeId === r.id}
                onClick={() => setRangeId(r.id)}
              >
                {r.label}
              </button>
            ))}
          </div>
          {byStack.length === 0 ? (
            <div className="empty-state">No containers.</div>
          ) : (
            byStack.map(([stack, list]) => (
              <div key={stack}>
                <h2>{stack}</h2>
                <div className="table-wrap">
                  <table className="data">
                    <thead>
                      <tr>
                        <th>Service</th>
                        <th>Image</th>
                        <th>State</th>
                        <th>Health</th>
                        <th>Restarts</th>
                        <th>Ports</th>
                      </tr>
                    </thead>
                    <tbody>
                      {list.map((c) => (
                        <tr
                          key={c.id}
                          className="clickable"
                          tabIndex={0}
                          onClick={() => setSelectedCid(c.id)}
                          onKeyDown={(e) => {
                            if (e.key === "Enter" || e.key === " ") {
                              e.preventDefault();
                              setSelectedCid(c.id);
                            }
                          }}
                        >
                          <td>{c.service || c.name}</td>
                          <td className="mono">
                            {c.image}
                            {c.imageVersion ? `:${c.imageVersion}` : ""}
                          </td>
                          <td>
                            <Badge variant={c.state === "running" ? "online" : "offline"}>
                              {c.state}
                            </Badge>
                          </td>
                          <td>{c.health || "—"}</td>
                          <td>
                            {c.restartCount}
                            {c.oomKilled ? (
                              <>
                                {" "}
                                <Badge variant="danger">OOM</Badge>
                              </>
                            ) : null}
                          </td>
                          <td>
                            {c.ports.length === 0
                              ? "—"
                              : c.ports
                                  .map((p) => `${p.public || ""}→${p.private}/${p.proto}`)
                                  .join(", ")}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            ))
          )}
          {selected ? (
            <div className="panel">
              <div className="row" style={{ justifyContent: "space-between" }}>
                <h2>{selected.name}</h2>
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={() => setSelectedCid(null)}
                >
                  Close
                </button>
              </div>
              <div className="grid-charts">
                <Chart
                  title="CPU %"
                  unit="%"
                  yMax={100}
                  loading={cMetricsLoading}
                  series={[
                    {
                      label: "CPU",
                      points: pointsOf(cMetrics.cpu_pct ?? []),
                      color: "var(--color-chart-1)",
                    },
                  ]}
                />
                <Chart
                  title="Memory"
                  unit="bytes"
                  yMax={(cMetrics.mem_limit ?? [])[(cMetrics.mem_limit ?? []).length - 1]?.v}
                  loading={cMetricsLoading}
                  series={[
                    {
                      label: "Usage",
                      points: pointsOf(cMetrics.mem_usage ?? []),
                      color: "var(--color-chart-1)",
                    },
                    {
                      label: "Limit",
                      points: pointsOf(cMetrics.mem_limit ?? []),
                      color: "var(--color-chart-2)",
                    },
                  ]}
                />
                <Chart
                  title="Network"
                  unit="bytes/s"
                  loading={cMetricsLoading}
                  series={[
                    {
                      label: "In",
                      points: pointsOf(cMetrics.net_rx_bps ?? []),
                      color: "var(--color-chart-1)",
                    },
                    {
                      label: "Out",
                      points: pointsOf(cMetrics.net_tx_bps ?? []),
                      color: "var(--color-chart-4)",
                    },
                  ]}
                />
                <Chart
                  title="Block I/O"
                  unit="bytes/s"
                  loading={cMetricsLoading}
                  series={[
                    {
                      label: "Read",
                      points: pointsOf(cMetrics.blk_read_bps ?? []),
                      color: "var(--color-chart-2)",
                    },
                    {
                      label: "Write",
                      points: pointsOf(cMetrics.blk_write_bps ?? []),
                      color: "var(--color-chart-3)",
                    },
                  ]}
                />
              </div>
            </div>
          ) : null}
        </div>
      ) : null}

      {tab === "logs" ? (
        <div className="stack" role="tabpanel" id="panel-logs" aria-labelledby="tab-logs">
          <div className="field" style={{ maxWidth: 240 }}>
            <label htmlFor="log-level">Minimum level</label>
            <select
              id="log-level"
              value={logLevel}
              onChange={(e) => setLogLevel(e.target.value as LogLevelFilter)}
            >
              <option value="warn">warn+</option>
              <option value="error">error+</option>
              <option value="info">info+</option>
              <option value="all">all</option>
            </select>
          </div>
          {logsError ? (
            <div className="app-error" role="alert" aria-live="assertive">
              {logsError}
            </div>
          ) : null}
          {events.length === 0 && !logsError ? (
            <div className="empty-state">No events</div>
          ) : (
            <ul style={{ listStyle: "none", padding: 0, margin: 0 }} className="stack">
              {[...events]
                .sort((a, b) => Date.parse(b.lastSeen) - Date.parse(a.lastSeen))
                .map((ev) => (
                  <li key={ev.id} className="card">
                    <div className="row">
                      {levelBadge(ev.level)}
                      {ev.count > 1 ? <span className="muted">×{ev.count}</span> : null}
                      <span className="muted">
                        {containerNameById.get(ev.containerId) ?? ev.containerId}
                      </span>
                    </div>
                    <div className="muted" style={{ fontSize: "0.85rem" }}>
                      First {formatRelativeTime(ev.firstSeen)} ({formatDateTime(ev.firstSeen)}) ·
                      Last {formatRelativeTime(ev.lastSeen)} ({formatDateTime(ev.lastSeen)})
                    </div>
                    <pre className="log-sample">{ev.sample}</pre>
                    <p className="muted" style={{ fontSize: "0.8rem", marginBottom: 0 }}>
                      Samples are redacted and merged.
                    </p>
                  </li>
                ))}
            </ul>
          )}
        </div>
      ) : null}

      <HostDeployTab hostId={id} active={tab === "deploy"} />
    </div>
  );
}
