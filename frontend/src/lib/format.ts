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

/** USD amount. Default 2 decimals; pass 4 only for sub-cent token accounting. */
export function fmtMoney(value: number | null | undefined, digits = 2): string {
  return `$${Number(value ?? 0).toFixed(digits)}`;
}

/**
 * USD amount with precision chosen by magnitude: under $1 keeps 4 decimals
 * (token accounting is sub-cent), $1+ shows 2. Every dollar figure on a page
 * goes through this so `$0.0042` never sits next to a differently-rounded
 * sibling (2026-10-12 审查: 平台页同一行两种精度). `fmtMoney` stays for
 * columns with a deliberate fixed precision (e.g. captured payments, always 2).
 */
export function fmtMoneyAuto(value: number | null | undefined): string {
  const v = Number(value ?? 0);
  return `$${(Math.abs(v) < 1 ? v.toFixed(4) : v.toFixed(2))}`;
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
