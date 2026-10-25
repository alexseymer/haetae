const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"] as const;

export function formatBytes(value: number, digits = 1): string {
  if (!Number.isFinite(value)) return "—";
  const sign = value < 0 ? "-" : "";
  let n = Math.abs(value);
  let i = 0;
  while (n >= 1024 && i < BYTE_UNITS.length - 1) {
    n /= 1024;
    i += 1;
  }
  const rounded = i === 0 ? String(Math.round(n)) : n.toFixed(digits);
  return `${sign}${rounded} ${BYTE_UNITS[i]}`;
}

export function formatBytesPerSec(value: number, digits = 1): string {
  return `${formatBytes(value, digits)}/s`;
}

export function formatPercent(value: number, digits = 1): string {
  if (!Number.isFinite(value)) return "—";
  return `${value.toFixed(digits)}%`;
}

export function formatPlain(value: number, digits = 2): string {
  if (!Number.isFinite(value)) return "—";
  if (Math.abs(value) >= 1000) return value.toFixed(0);
  if (Number.isInteger(value)) return String(value);
  return value.toFixed(digits);
}

export type UnitKind = "%" | "bytes" | "bytes/s" | "plain";

export function formatByUnit(value: number, unit: UnitKind): string {
  switch (unit) {
    case "%":
      return formatPercent(value);
    case "bytes":
      return formatBytes(value);
    case "bytes/s":
      return formatBytesPerSec(value);
    default:
      return formatPlain(value);
  }
}

const RELATIVE_THRESHOLDS: Array<{ limit: number; div: number; label: string }> = [
  { limit: 60, div: 1, label: "sec" },
  { limit: 3600, div: 60, label: "min" },
  { limit: 86400, div: 3600, label: "hr" },
  { limit: 604800, div: 86400, label: "day" },
];

export function formatRelativeTime(isoOrNull: string | null, now = Date.now()): string {
  if (!isoOrNull) return "never";
  const then = Date.parse(isoOrNull);
  if (!Number.isFinite(then)) return "unknown";
  const diffSec = Math.round((now - then) / 1000);
  if (diffSec < 0) return "just now";
  if (diffSec < 5) return "just now";
  for (const t of RELATIVE_THRESHOLDS) {
    if (diffSec < t.limit) {
      const n = Math.max(1, Math.floor(diffSec / t.div));
      let unit: string;
      if (t.label === "hr") unit = n === 1 ? "hr" : "hrs";
      else unit = n === 1 ? t.label : `${t.label}s`;
      return `${n} ${unit} ago`;
    }
  }
  const days = Math.floor(diffSec / 86400);
  return `${days} days ago`;
}

export function formatDateTime(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

export function withheldPercent(linesIn: number, linesSkipped: number): number {
  if (linesIn <= 0) return 0;
  return (linesSkipped / linesIn) * 100;
}
