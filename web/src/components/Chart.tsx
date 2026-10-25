import { useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import {
  dataExtent,
  nearestPointIndex,
  niceScale,
  scaleLinear,
  splitOnGaps,
  timeTicks,
  type Point,
} from "../lib/chartMath";
import { formatByUnit, type UnitKind } from "../lib/format";

/** Max rows in the accessible fallback table; large series are truncated with a note. */
export const CHART_TABLE_MAX_ROWS = 200;

export interface ChartSeries {
  label: string;
  points: Point[];
  color: string; // CSS color token value, e.g. var(--color-chart-1)
}

interface ChartProps {
  title: string;
  series: ChartSeries[];
  unit: UnitKind;
  yMax?: number;
  loading?: boolean;
  error?: string | null;
  height?: number;
}

function formatTickTime(t: number): string {
  const d = new Date(t);
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function isFinitePoint(p: Point): boolean {
  return Number.isFinite(p.t) && Number.isFinite(p.v);
}

export function Chart({
  title,
  series,
  unit,
  yMax,
  loading = false,
  error = null,
  height = 200,
}: ChartProps) {
  const gid = useId();
  const svgRef = useRef<SVGSVGElement>(null);
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);

  const primaryPoints = series[0]?.points ?? [];
  const allPoints = useMemo(() => series.map((s) => s.points), [series]);
  const extent = useMemo(() => dataExtent(allPoints), [allPoints]);

  const layout = useMemo(() => {
    if (!extent) return null;
    const padL = 48;
    const padR = 12;
    const padT = 12;
    const padB = 28;
    const width = 560;
    const yScale = niceScale(extent.minV, extent.maxV, 5, yMax);
    const xTicks = timeTicks(extent.minT, extent.maxT, 4);
    const segments = series.map((s) => ({
      ...s,
      segments: splitOnGaps(s.points.filter(isFinitePoint)),
    }));
    return { padL, padR, padT, padB, width, height, yScale, xTicks, segments, extent };
  }, [extent, series, yMax, height]);

  const ariaSummary = useMemo(() => {
    if (loading) return `${title}: loading`;
    if (error) return `${title}: error ${error}`;
    if (!extent || primaryPoints.length === 0) return `${title}: no data yet`;
    const last = primaryPoints[primaryPoints.length - 1];
    return `${title}: ${primaryPoints.length} points, last value ${formatByUnit(last?.v ?? 0, unit)}`;
  }, [title, loading, error, extent, primaryPoints, unit]);

  const tablePoints = primaryPoints.slice(0, CHART_TABLE_MAX_ROWS);
  const tableTruncated = primaryPoints.length > CHART_TABLE_MAX_ROWS;

  if (loading) {
    return (
      <div className="chart">
        <div className="chart-title">{title}</div>
        <div className="chart-state" role="status">
          Loading…
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="chart">
        <div className="chart-title">{title}</div>
        <div className="chart-state app-error" role="alert">
          {error}
        </div>
      </div>
    );
  }

  if (!layout || primaryPoints.length === 0) {
    return (
      <div className="chart">
        <div className="chart-title">{title}</div>
        <div className="chart-state" role="img" aria-label={`${title}: No data yet`}>
          No data yet
        </div>
        <table className="sr-only">
          <caption>{title}</caption>
          <tbody>
            <tr>
              <td>No data yet</td>
            </tr>
          </tbody>
        </table>
      </div>
    );
  }

  const { padL, padR, padT, padB, width, yScale, xTicks, segments } = layout;
  const plotW = width - padL - padR;
  const plotH = height - padT - padB;
  const { minT, maxT } = layout.extent;

  const xOf = (t: number) => scaleLinear(t, minT, maxT, padL, padL + plotW);
  const yOf = (v: number) => scaleLinear(v, yScale.min, yScale.max, padT + plotH, padT);

  const activeIdx =
    hoverIdx != null && hoverIdx >= 0 && hoverIdx < primaryPoints.length ? hoverIdx : null;
  const activeT = activeIdx != null ? primaryPoints[activeIdx]?.t : null;

  const liveAnnouncement =
    activeIdx != null && activeT != null
      ? [
          formatTickTime(activeT),
          ...series.map((s) => {
            const p = s.points[activeIdx];
            return `${s.label}: ${p && isFinitePoint(p) ? formatByUnit(p.v, unit) : "—"}`;
          }),
        ].join(", ")
      : "";

  const onMove = (clientX: number) => {
    const svg = svgRef.current;
    if (!svg) return;
    const rect = svg.getBoundingClientRect();
    const x = ((clientX - rect.left) / rect.width) * width;
    const t = scaleLinear(x, padL, padL + plotW, minT, maxT);
    setHoverIdx(nearestPointIndex(primaryPoints, t));
  };

  const onKeyDown = (e: KeyboardEvent<SVGSVGElement>) => {
    if (primaryPoints.length === 0) return;
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      e.preventDefault();
      setHoverIdx((prev) => {
        const cur = prev ?? (e.key === "ArrowRight" ? -1 : primaryPoints.length);
        const next = e.key === "ArrowRight" ? cur + 1 : cur - 1;
        return Math.max(0, Math.min(primaryPoints.length - 1, next));
      });
    } else if (e.key === "Home") {
      e.preventDefault();
      setHoverIdx(0);
    } else if (e.key === "End") {
      e.preventDefault();
      setHoverIdx(primaryPoints.length - 1);
    } else if (e.key === "Escape") {
      setHoverIdx(null);
    }
  };

  // React style={{…}} is applied via the CSSOM (el.style.* / setProperty), which
  // CSP style-src 'self' does not block. Inline style attributes in static HTML
  // are blocked; these props are not. E2E asserts computed styles under the real
  // CSP header (see test/e2e/ui.spec.mjs).
  return (
    <div className="chart" style={{ position: "relative" }}>
      <div className="chart-title">{title}</div>
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={ariaSummary}
        tabIndex={0}
        onKeyDown={onKeyDown}
        onMouseMove={(e) => onMove(e.clientX)}
        onMouseLeave={() => setHoverIdx(null)}
        onTouchStart={(e) => {
          const t = e.touches[0];
          if (t) onMove(t.clientX);
        }}
        onTouchMove={(e) => {
          const t = e.touches[0];
          if (t) onMove(t.clientX);
        }}
      >
        {yScale.ticks.map((tick) => {
          const y = yOf(tick);
          return (
            <g key={`y-${tick}`}>
              <line
                x1={padL}
                x2={padL + plotW}
                y1={y}
                y2={y}
                stroke="var(--color-chart-grid)"
                strokeWidth={1}
              />
              <text
                x={padL - 6}
                y={y}
                textAnchor="end"
                dominantBaseline="middle"
                fill="var(--color-text-muted)"
                fontSize={10}
              >
                {formatByUnit(tick, unit)}
              </text>
            </g>
          );
        })}
        {xTicks.map((tick) => {
          const x = xOf(tick);
          return (
            <text
              key={`x-${tick}`}
              x={x}
              y={height - 8}
              textAnchor="middle"
              fill="var(--color-text-muted)"
              fontSize={10}
            >
              {formatTickTime(tick)}
            </text>
          );
        })}
        {segments.map((s, si) =>
          s.segments.map((seg, segi) => {
            if (seg.length === 0) return null;
            const d = seg
              .filter(isFinitePoint)
              .map((p, i) => `${i === 0 ? "M" : "L"}${xOf(p.t).toFixed(2)},${yOf(p.v).toFixed(2)}`)
              .join(" ");
            if (!d) return null;
            return (
              <path
                key={`${gid}-${si}-${segi}`}
                d={d}
                fill="none"
                stroke={s.color}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            );
          }),
        )}
        {activeT != null && Number.isFinite(activeT) && (
          <line
            x1={xOf(activeT)}
            x2={xOf(activeT)}
            y1={padT}
            y2={padT + plotH}
            stroke="var(--color-chart-crosshair)"
            strokeWidth={1}
            strokeDasharray="4 3"
          />
        )}
        {activeIdx != null &&
          series.map((s, i) => {
            const p = s.points[activeIdx];
            if (!p || !isFinitePoint(p)) return null;
            return (
              <circle
                key={`dot-${i}`}
                cx={xOf(p.t)}
                cy={yOf(p.v)}
                r={3.5}
                fill={s.color}
                stroke="var(--color-surface)"
                strokeWidth={1.5}
              />
            );
          })}
      </svg>
      <div className="sr-only" aria-live="polite">
        {liveAnnouncement}
      </div>
      {activeIdx != null && activeT != null && Number.isFinite(activeT) && (
        <div
          className="chart-tooltip"
          style={{
            left:
              Math.min(Math.max(8, (xOf(activeT) / width) * 100), 70) + "%",
            top: 36,
          }}
        >
          <div>{formatTickTime(activeT)}</div>
          {series.map((s) => {
            const p = s.points[activeIdx];
            return (
              <div key={s.label}>
                {s.label}: {p && isFinitePoint(p) ? formatByUnit(p.v, unit) : "—"}
              </div>
            );
          })}
        </div>
      )}
      <div className="chart-legend">
        {series.map((s) => (
          <span key={s.label}>
            <span className="chart-swatch" style={{ background: s.color }} />
            {s.label}
          </span>
        ))}
      </div>
      <table className="sr-only">
        <caption>{title}</caption>
        <thead>
          <tr>
            <th>Time</th>
            {series.map((s) => (
              <th key={s.label}>{s.label}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {tablePoints.map((p, i) => (
            <tr key={p.t}>
              <td>{formatTickTime(p.t)}</td>
              {series.map((s) => (
                <td key={s.label}>
                  {s.points[i] && isFinitePoint(s.points[i]!)
                    ? formatByUnit(s.points[i]!.v, unit)
                    : "—"}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {tableTruncated ? (
        <p className="sr-only">
          Showing first {CHART_TABLE_MAX_ROWS} of {primaryPoints.length} points.
        </p>
      ) : null}
    </div>
  );
}
