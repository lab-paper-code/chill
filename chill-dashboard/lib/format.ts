// Display formatting only: unit conversion of server values. No statistics are derived here.
import { duration } from "./live-display.ts"

/** The one rendering of an absent value. */
export const DASH = "—"

type Num = number | bigint | null | undefined

function finite(value: Num): number | null {
  if (value == null) return null
  const n = typeof value === "bigint" ? Number(value) : value
  return Number.isFinite(n) ? n : null
}

/** Grouped number with a fixed number of fraction digits. */
export function formatNumber(value: Num, digits = 0): string {
  const n = finite(value)
  return n == null ? DASH : n.toLocaleString("ko-KR", { minimumFractionDigits: digits, maximumFractionDigits: digits })
}

/** Nanoseconds as "850 µs", "12.3 ms", "1.24 s", then "3분 20초" from one minute up. */
export function formatDurationNanos(value: Num): string {
  const n = finite(value)
  if (n == null || n < 0) return DASH
  if (n < 1e6) return `${formatNumber(n / 1e3, n < 1e4 ? 1 : 0)} µs`
  if (n < 1e9) return `${formatNumber(n / 1e6, n < 1e7 ? 2 : 1)} ms`
  if (n < 60e9) return `${formatNumber(n / 1e9, 2)} s`
  return duration(n / 1e9)
}

/** Milliseconds, for live latency values. */
export function formatMs(value: Num, digits = 1): string {
  const n = finite(value)
  return n == null ? DASH : `${formatNumber(n, digits)} ms`
}

export function formatWatts(value: Num, digits = 1): string {
  const n = finite(value)
  return n == null ? DASH : `${formatNumber(n, digits)} W`
}

/** Joules, switching to kJ from 10 000 J. */
export function formatJoules(value: Num): string {
  const n = finite(value)
  if (n == null) return DASH
  return Math.abs(n) >= 10_000 ? `${formatNumber(n / 1000, 1)} kJ` : `${formatNumber(n, Math.abs(n) < 10 ? 2 : 1)} J`
}

export function formatBytes(value: Num): string {
  const n = finite(value)
  if (n == null || n < 0) return DASH
  const units = ["B", "KiB", "MiB", "GiB", "TiB"]
  let size = n, unit = 0
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++ }
  return `${formatNumber(size, unit === 0 ? 0 : 1)} ${units[unit]}`
}

/** A fraction (0.987) as "98.7%". */
export function formatPercent(fraction: Num, digits = 1): string {
  const n = finite(fraction)
  return n == null ? DASH : `${formatNumber(n * 100, digits)}%`
}

export function formatRate(value: Num, digits = 1): string {
  const n = finite(value)
  return n == null ? DASH : `${formatNumber(n, digits)} req/s`
}

const kst = new Intl.DateTimeFormat("sv-SE", { timeZone: "Asia/Seoul", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" })

function epoch(at: string | number | Date | null | undefined): number | null {
  if (at == null) return null
  const ms = at instanceof Date ? at.getTime() : typeof at === "number" ? at : Date.parse(at)
  return Number.isFinite(ms) ? ms : null
}

/** Absolute KST time: "2026-11-13 14:03:22". */
export function formatKst(at: string | number | Date | null | undefined, seconds = true): string {
  const ms = epoch(at)
  if (ms == null) return DASH
  const text = kst.format(new Date(ms))
  return seconds ? text : text.slice(0, 16)
}

/** Relative time against `now`: "12초 전", "3분 전", "2시간 전", "4일 전", or "곧" for future times. */
export function formatRelative(at: string | number | Date | null | undefined, now: number): string {
  const ms = epoch(at)
  if (ms == null) return DASH
  const seconds = Math.floor((now - ms) / 1000)
  if (seconds < 0) return "곧"
  if (seconds < 60) return `${seconds}초 전`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}분 전`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}시간 전`
  return `${Math.floor(seconds / 86400)}일 전`
}
