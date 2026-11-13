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

/**
 * Nanoseconds as "850 µs", "12.3 ms", "948 ms", then "55.8초" under a minute and "1분 28초" from one minute up.
 * Milliseconds follow `formatMs`; a whole number of seconds has no decimal: a configured 10 s period reads "10초".
 */
export function formatDurationNanos(value: Num): string {
  const n = finite(value)
  if (n == null || n < 0) return DASH
  if (n < 1e6) return `${formatNumber(n / 1e3, n < 1e4 && n % 1e3 ? 1 : 0)} µs`
  if (n < 1e9) return formatMs(n / 1e6)
  // Rounded to one decimal; 59.96 s would read "60.0초", so it moves to the minute form.
  if (n < 59.95e9) return `${formatNumber(n / 1e9, Math.round(n / 1e8) % 10 ? 1 : 0)}초`
  return duration(Math.round(n / 1e9))
}

/** Seconds through `formatDurationNanos`. */
export function formatSeconds(seconds: Num): string {
  const n = finite(seconds)
  return n == null ? DASH : formatDurationNanos(Math.round(n * 1e9))
}

const goUnits: Record<string, number> = { ns: 1, us: 1e3, "µs": 1e3, "μs": 1e3, ms: 1e6, s: 1e9, m: 60e9, h: 3600e9 }
/** Go duration text ("500ms", "1.5s", "1m30s") in nanoseconds; undefined when it is not one. */
export function goDurationNanos(text: string): number | undefined {
  const parts = [...text.trim().matchAll(/(\d+\.?\d*|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g)]
  if (!parts.length || parts.map(part => part[0]).join("") !== text.trim()) return undefined
  return Math.round(parts.reduce((sum, [, value, unit]) => sum + Number(value) * goUnits[unit], 0))
}

/** Milliseconds number: one decimal below 100 ms unless whole, whole from 100 ms. For a column or tile that shows "ms". */
export function msNumber(value: Num): string {
  const n = finite(value)
  return n == null ? DASH : formatNumber(n, Math.abs(n) < 100 && !Number.isInteger(n) ? 1 : 0)
}
/** Milliseconds, for latency values and targets: "948 ms", "1,000 ms", "12.4 ms". */
export function formatMs(value: Num): string {
  const text = msNumber(value)
  return text === DASH ? DASH : `${text} ms`
}
/** A latency target: "p95 1,000 ms". */
export function formatTarget(percentile: Num, targetNanos: Num): string {
  const p = finite(percentile), n = finite(targetNanos)
  return p == null || n == null ? DASH : `p${p} ${formatMs(n / 1e6)}`
}

/** Watts number: one decimal below 100 W, whole watts from 100 W. For a column or tile that shows "W". */
export function wattsNumber(value: Num): string {
  const n = finite(value)
  return n == null ? DASH : formatNumber(n, Math.abs(n) < 100 ? 1 : 0)
}
export function formatWatts(value: Num): string {
  const text = wattsNumber(value)
  return text === DASH ? DASH : `${text} W`
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

/** A fraction (0.987) as a percent number ("98.7"), one decimal for success rates. For a column or tile that shows "%". */
export function percentNumber(fraction: Num, digits = 1): string {
  const n = finite(fraction)
  return n == null ? DASH : formatNumber(n * 100, digits)
}
/** A fraction (0.987) as "98.7%". */
export function formatPercent(fraction: Num, digits = 1): string {
  const text = percentNumber(fraction, digits)
  return text === DASH ? DASH : `${text}%`
}

/** Request rate number: one decimal below 10 req/s, whole numbers from 10. For a column or tile that shows "req/s". */
export function rateNumber(value: Num): string {
  const n = finite(value)
  return n == null ? DASH : formatNumber(n, Math.abs(n) < 10 ? 1 : 0)
}
export function formatRate(value: Num): string {
  const text = rateNumber(value)
  return text === DASH ? DASH : `${text} req/s`
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
/** Time in a list or table row: "11-13 14:03:22" KST. */
export function formatListTime(at: string | number | Date | null | undefined): string {
  const text = formatKst(at)
  return text === DASH ? DASH : text.slice(5)
}
/** Time in a detail view: "2026-11-13 14:03:22" KST. */
export const formatDetailTime = (at: string | number | Date | null | undefined): string => formatKst(at)

/**
 * Relative time against `now`: "방금" within 5 s either side (server and browser clocks drift a little),
 * then "12초 전", "3분 전", "2시간 전", "4일 전", or "곧" for times further ahead.
 */
export function formatRelative(at: string | number | Date | null | undefined, now: number): string {
  const ms = epoch(at)
  if (ms == null) return DASH
  const seconds = Math.floor((now - ms) / 1000)
  if (Math.abs(now - ms) < 5000) return "방금"
  if (seconds < 0) return "곧"
  if (seconds < 60) return `${seconds}초 전`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}분 전`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}시간 전`
  return `${Math.floor(seconds / 86400)}일 전`
}

/** Round axis values covering [min, max] in about `count` steps, and the fraction digits that keep them apart. */
export function niceTicks(min: number, max: number, count = 4): { ticks: number[]; digits: number } {
  if (!Number.isFinite(min) || !Number.isFinite(max)) return { ticks: [0, 1], digits: 0 }
  if (min === max) { const pad = Math.abs(min) / 10 || 1; min = min >= 0 && min - pad < 0 ? 0 : min - pad; max += pad }
  const raw = (max - min) / count
  const power = 10 ** Math.floor(Math.log10(raw))
  const step = [1, 2, 2.5, 5, 10].map(m => m * power).find(s => s >= raw * (1 - 1e-9)) ?? 10 * power
  const start = Math.floor(min / step + 1e-9) * step, end = Math.ceil(max / step - 1e-9) * step
  const ticks: number[] = []
  for (let value = start; value <= end + step / 2; value += step) ticks.push(Number(value.toPrecision(12)))
  const text = String(Number(step.toPrecision(6)))
  return { ticks, digits: text.includes("e-") ? 6 : (text.split(".")[1]?.length ?? 0) }
}

const MS_STEPS = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400].map(s => s * 1000)
const KST_OFFSET = 9 * 3_600_000
/**
 * Time axis ticks in KST, aligned to round clock times, with a label precision that never repeats a label:
 * seconds under one-minute steps, the date once the window spans a day.
 */
export function timeTicks(from: number, to: number, count = 5): { ticks: number[]; label: (ms: number) => string } {
  const span = Math.max(to - from, 1000)
  const step = MS_STEPS.find(item => span / item <= count) ?? Math.ceil(span / count / 86_400_000) * 86_400_000
  const ticks: number[] = []
  for (let t = Math.ceil((from + KST_OFFSET) / step) * step - KST_OFFSET; t <= to; t += step) ticks.push(t)
  const cut: [number, number] = step < 60_000 ? [11, 19] : step >= 86_400_000 ? [5, 10] : span >= 86_400_000 ? [5, 16] : [11, 16]
  return { ticks, label: ms => kst.format(new Date(ms)).slice(...cut) }
}

/** Ticks for an axis in seconds from a start (load scenarios), labelled "1분 30초". */
export function secondTicks(total: number, count = 5): { ticks: number[]; label: (seconds: number) => string } {
  const span = Math.max(total, 1)
  const step = MS_STEPS.map(ms => ms / 1000).find(item => span / item <= count) ?? Math.ceil(span / count / 3600) * 3600
  const ticks: number[] = []
  for (let s = 0; s <= span; s += step) ticks.push(s)
  return { ticks, label: duration }
}
