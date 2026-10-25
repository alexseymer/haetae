export interface Point {
  t: number;
  v: number;
}

export interface NiceScale {
  min: number;
  max: number;
  ticks: number[];
  step: number;
}

/** Round a step to a "nice" number (1, 2, 5 × 10^n). */
export function niceNum(range: number, round: boolean): number {
  if (range <= 0 || !Number.isFinite(range)) return 1;
  const exp = Math.floor(Math.log10(range));
  const frac = range / Math.pow(10, exp);
  let niceFrac: number;
  if (round) {
    if (frac < 1.5) niceFrac = 1;
    else if (frac < 3) niceFrac = 2;
    else if (frac < 7) niceFrac = 5;
    else niceFrac = 10;
  } else {
    if (frac <= 1) niceFrac = 1;
    else if (frac <= 2) niceFrac = 2;
    else if (frac <= 5) niceFrac = 5;
    else niceFrac = 10;
  }
  return niceFrac * Math.pow(10, exp);
}

/** Build a nice y-scale covering dataMin..dataMax with ~tickCount ticks. */
export function niceScale(
  dataMin: number,
  dataMax: number,
  tickCount = 5,
  fixedMax?: number,
): NiceScale {
  let min = dataMin;
  let max = fixedMax !== undefined ? fixedMax : dataMax;

  if (!Number.isFinite(min) || !Number.isFinite(max)) {
    return { min: 0, max: 1, ticks: [0, 1], step: 1 };
  }

  if (fixedMax !== undefined) {
    min = Math.min(0, dataMin);
    max = fixedMax;
  }

  if (min === max) {
    if (min === 0) {
      max = 1;
    } else if (min > 0) {
      min = 0;
      max = max * 1.1;
    } else {
      max = 0;
      min = min * 1.1;
    }
  }

  const range = niceNum(max - min, false);
  const step = niceNum(range / Math.max(1, tickCount - 1), true);
  const niceMin = Math.floor(min / step) * step;
  let niceMax = Math.ceil(max / step) * step;
  if (fixedMax !== undefined) {
    niceMax = Math.max(niceMax, fixedMax);
  }

  const ticks: number[] = [];
  // Avoid infinite loops from floating point
  const maxTicks = 20;
  for (let i = 0, v = niceMin; v <= niceMax + step * 0.5 && i < maxTicks; i += 1, v = niceMin + i * step) {
    const rounded = Number(v.toPrecision(12));
    ticks.push(rounded);
  }

  return {
    min: ticks[0] ?? niceMin,
    max: ticks[ticks.length - 1] ?? niceMax,
    ticks,
    step,
  };
}

export function scaleLinear(
  value: number,
  domainMin: number,
  domainMax: number,
  rangeMin: number,
  rangeMax: number,
): number {
  if (domainMax === domainMin) return (rangeMin + rangeMax) / 2;
  const t = (value - domainMin) / (domainMax - domainMin);
  return rangeMin + t * (rangeMax - rangeMin);
}

/** Median of positive spacings between consecutive timestamps. */
export function medianSpacing(points: Point[]): number | null {
  if (points.length < 2) return null;
  const gaps: number[] = [];
  for (let i = 1; i < points.length; i += 1) {
    const prev = points[i - 1];
    const cur = points[i];
    if (!prev || !cur) continue;
    const d = cur.t - prev.t;
    if (d > 0) gaps.push(d);
  }
  if (gaps.length === 0) return null;
  gaps.sort((a, b) => a - b);
  const mid = Math.floor(gaps.length / 2);
  if (gaps.length % 2 === 0) {
    const a = gaps[mid - 1];
    const b = gaps[mid];
    if (a === undefined || b === undefined) return null;
    return (a + b) / 2;
  }
  return gaps[mid] ?? null;
}

/**
 * Split a series into contiguous segments. A gap is introduced when the
 * spacing between consecutive points exceeds 3× the median spacing.
 */
export function splitOnGaps(points: Point[], gapFactor = 3): Point[][] {
  if (points.length === 0) return [];
  if (points.length === 1) return [points];

  const median = medianSpacing(points);
  const segments: Point[][] = [];
  let current: Point[] = [points[0]!];

  for (let i = 1; i < points.length; i += 1) {
    const prev = points[i - 1]!;
    const cur = points[i]!;
    const gap = cur.t - prev.t;
    if (median != null && gap > gapFactor * median) {
      segments.push(current);
      current = [cur];
    } else {
      current.push(cur);
    }
  }
  segments.push(current);
  return segments;
}

export function dataExtent(series: Point[][]): { minT: number; maxT: number; minV: number; maxV: number } | null {
  let minT = Infinity;
  let maxT = -Infinity;
  let minV = Infinity;
  let maxV = -Infinity;
  let any = false;
  for (const pts of series) {
    for (const p of pts) {
      any = true;
      if (p.t < minT) minT = p.t;
      if (p.t > maxT) maxT = p.t;
      if (p.v < minV) minV = p.v;
      if (p.v > maxV) maxV = p.v;
    }
  }
  if (!any) return null;
  return { minT, maxT, minV, maxV };
}

/** Choose ~n time tick timestamps between minT and maxT. */
export function timeTicks(minT: number, maxT: number, count = 4): number[] {
  if (!Number.isFinite(minT) || !Number.isFinite(maxT) || maxT <= minT) {
    return [minT];
  }
  const ticks: number[] = [];
  for (let i = 0; i < count; i += 1) {
    const t = minT + ((maxT - minT) * i) / (count - 1);
    ticks.push(t);
  }
  return ticks;
}

export function nearestPointIndex(points: Point[], t: number): number {
  if (points.length === 0) return -1;
  let best = 0;
  let bestDist = Infinity;
  for (let i = 0; i < points.length; i += 1) {
    const p = points[i]!;
    const d = Math.abs(p.t - t);
    if (d < bestDist) {
      bestDist = d;
      best = i;
    }
  }
  return best;
}
