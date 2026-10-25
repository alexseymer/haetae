import { describe, expect, it } from "vitest";
import {
  formatBytes,
  formatBytesPerSec,
  formatPercent,
  formatRelativeTime,
  withheldPercent,
} from "./format";

describe("formatBytes", () => {
  it("formats binary units", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(1024 ** 3)).toBe("1.0 GB");
  });
});

describe("formatBytesPerSec", () => {
  it("appends /s", () => {
    expect(formatBytesPerSec(2048)).toBe("2.0 KB/s");
  });
});

describe("formatPercent", () => {
  it("formats with one decimal", () => {
    expect(formatPercent(12.34)).toBe("12.3%");
  });
});

describe("formatRelativeTime", () => {
  const now = Date.parse("2024-01-01T12:00:00Z");

  it("handles null and just now", () => {
    expect(formatRelativeTime(null, now)).toBe("never");
    expect(formatRelativeTime(new Date(now - 2000).toISOString(), now)).toBe("just now");
  });

  it("formats minutes and hours", () => {
    expect(formatRelativeTime(new Date(now - 3 * 60_000).toISOString(), now)).toBe("3 mins ago");
    expect(formatRelativeTime(new Date(now - 2 * 3600_000).toISOString(), now)).toBe("2 hrs ago");
  });
});

describe("withheldPercent", () => {
  it("computes ratio", () => {
    expect(withheldPercent(100, 25)).toBe(25);
    expect(withheldPercent(0, 10)).toBe(0);
  });
});
