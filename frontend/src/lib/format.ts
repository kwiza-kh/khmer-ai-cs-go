/**
 * Shared number / date formatting.
 *
 * Every table and KPI in this app used to roll its own formatting, which
 * produced `$12.000000` next to `$412.8700`, `2026/9/3` next to `2026-08-31`,
 * and `48213` next to `48,213,992` inside a single row. Route everything
 * through here so a column reads consistently.
 */

const INT = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });

/** Thousands-separated integer. `fmtInt(48213) === "48,213"`. */
export function fmtInt(value: number | null | undefined): string {
  return INT.format(Number(value ?? 0));
}

/** Compact token count for tight spaces: 1_200_000 → "1.2M". */
export function fmtCompact(value: number | null | undefined): string {
  const n = Number(value ?? 0);
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(1)}B`;
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

/** USD amount. Default 2 decimals; pass 4 only for sub-cent token accounting. */
export function fmtMoney(value: number | null | undefined, digits = 2): string {
  return `$${Number(value ?? 0).toFixed(digits)}`;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** `YYYY-MM-DD` in local time — sortable and zero-padded (was `2026/9/3`). */
export function fmtDate(input: string | number | Date | null | undefined): string {
  if (!input) return "—";
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return "—";
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** `YYYY-MM-DD HH:mm` in local time. */
export function fmtDateTime(input: string | number | Date | null | undefined): string {
  if (!input) return "—";
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return "—";
  return `${fmtDate(d)} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Seconds → `5m`, `2h 30m`, `3.1 min` (for SLA / resolution durations). */
export function fmtDuration(secs: number | null | undefined): string {
  const s = Number(secs ?? 0);
  if (s <= 0) return "—";
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.round((s % 3600) / 60)}m`;
  return `${Math.round(s / 86400)}d`;
}
