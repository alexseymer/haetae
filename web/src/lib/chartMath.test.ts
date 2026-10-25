import { describe, expect, it } from "vitest";
import {
  dataExtent,
  medianSpacing,
  niceNum,
  niceScale,
  scaleLinear,
  splitOnGaps,
  timeTicks,
} from "./chartMath";

describe("niceNum", () => {
  it("rounds to nice fractions", () => {
    expect(niceNum(100, true)).toBe(100);
    expect(niceNum(12, true)).toBe(10);
    expect(niceNum(12, false)).toBe(20);
  });
});

describe("niceScale", () => {
  it("produces nice ticks covering the data", () => {
    const s = niceScale(3, 87, 5);
    expect(s.ticks.length).toBeGreaterThan(1);
    expect(s.min).toBeLessThanOrEqual(3);
    expect(s.max).toBeGreaterThanOrEqual(87);
    for (let i = 1; i < s.ticks.length; i += 1) {
      expect(s.ticks[i]! - s.ticks[i - 1]!).toBeCloseTo(s.step, 8);
    }
  });

  it("respects fixed max", () => {
    const s = niceScale(0, 40, 5, 100);
    expect(s.max).toBeGreaterThanOrEqual(100);
  });

  it("handles empty-equivalent equal min/max", () => {
    const s = niceScale(0, 0, 5);
    expect(s.max).toBeGreaterThan(s.min);
  });
});

describe("scaleLinear", () => {
  it("maps domain to range", () => {
    expect(scaleLinear(0, 0, 10, 0, 100)).toBe(0);
    expect(scaleLinear(10, 0, 10, 0, 100)).toBe(100);
    expect(scaleLinear(5, 0, 10, 0, 100)).toBe(50);
  });
});

describe("gap detection", () => {
  it("computes median spacing", () => {
    const pts = [
      { t: 0, v: 1 },
      { t: 10, v: 1 },
      { t: 20, v: 1 },
      { t: 30, v: 1 },
    ];
    expect(medianSpacing(pts)).toBe(10);
  });

  it("splits when gap exceeds 3x median", () => {
    const pts = [
      { t: 0, v: 1 },
      { t: 10, v: 2 },
      { t: 20, v: 3 },
      { t: 100, v: 4 },
      { t: 110, v: 5 },
    ];
    const segs = splitOnGaps(pts);
    expect(segs).toHaveLength(2);
    expect(segs[0]).toHaveLength(3);
    expect(segs[1]).toHaveLength(2);
  });

  it("returns empty for empty data", () => {
    expect(splitOnGaps([])).toEqual([]);
    expect(dataExtent([])).toBeNull();
  });
});

describe("dataExtent", () => {
  it("skips NaN and Infinity", () => {
    const ext = dataExtent([
      [
        { t: 1, v: 10 },
        { t: NaN, v: 20 },
        { t: 2, v: Infinity },
        { t: 3, v: -Infinity },
        { t: 4, v: 30 },
      ],
    ]);
    expect(ext).toEqual({ minT: 1, maxT: 4, minV: 10, maxV: 30 });
  });

  it("handles a single finite point", () => {
    expect(dataExtent([[{ t: 5, v: 7 }]])).toEqual({
      minT: 5,
      maxT: 5,
      minV: 7,
      maxV: 7,
    });
  });

  it("handles negative values", () => {
    const ext = dataExtent([
      [
        { t: 0, v: -10 },
        { t: 1, v: -2 },
      ],
    ]);
    expect(ext).toEqual({ minT: 0, maxT: 1, minV: -10, maxV: -2 });
  });

  it("returns null when all points are non-finite", () => {
    expect(dataExtent([[{ t: NaN, v: 1 }, { t: 1, v: Infinity }]])).toBeNull();
  });
});

describe("timeTicks", () => {
  it("returns a single tick when count is 1", () => {
    expect(timeTicks(100, 200, 1)).toEqual([100]);
  });

  it("spaces ticks evenly for count > 1", () => {
    expect(timeTicks(0, 100, 5)).toEqual([0, 25, 50, 75, 100]);
  });
});
