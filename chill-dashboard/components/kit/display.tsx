"use client"

import * as React from "react"
import Link from "next/link"
import { AlertTriangle, CheckCircle2, CircleDashed, CircleDot, Info, XCircle } from "lucide-react"
import { cn } from "@/lib/utils"
import { DASH, formatKst, formatRelative } from "@/lib/format"
import type { StatusInfo, StatusTone } from "@/lib/status-labels"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/custom/blocks/TooltipBlock"
import { CopyButton } from "@/components/custom/blocks/CopyButtonBlock"
import { Card } from "@/components/custom/blocks/CardBlock"

const toneText: Record<StatusTone, string> = { ok: "text-ok", warn: "text-warn", fail: "text-fail", progress: "text-primary", idle: "text-idle" }
const toneSoft: Record<StatusTone, string> = { ok: "bg-ok-soft", warn: "bg-warn-soft", fail: "bg-fail-soft", progress: "bg-primary-soft", idle: "bg-idle-soft" }
const toneIcon: Record<StatusTone, React.ElementType> = { ok: CheckCircle2, warn: AlertTriangle, fail: XCircle, progress: CircleDot, idle: CircleDashed }

/** Short hover/focus hint around any content. Nothing is wrapped when `text` is empty. */
export function Hint({ text, children, focusable = false, className }: { text?: React.ReactNode; children: React.ReactNode; focusable?: boolean; className?: string }) {
  if (!text) return <>{children}</>
  return (
    <Tooltip>
      <TooltipTrigger className={className} tabIndex={focusable ? 0 : undefined}>{children}</TooltipTrigger>
      <TooltipContent className="max-w-xs whitespace-normal">{text}</TooltipContent>
    </Tooltip>
  )
}

/** "—" for an absent value, with the reason in a tooltip. */
export function Missing({ reason }: { reason?: React.ReactNode }) {
  return (
    <Hint text={reason} focusable>
      <span className="text-muted-foreground" aria-label={typeof reason === "string" ? `값 없음: ${reason}` : "값 없음"}>{DASH}</span>
    </Hint>
  )
}

/** One of the five states as a pill: icon shape, colour and label. Unknown codes show "—" with the code in the tooltip. */
export function StatusBadge({ status, tone, label, reason, className }: { status?: StatusInfo; tone?: StatusTone; label?: React.ReactNode; reason?: React.ReactNode; className?: string }) {
  const resolvedTone = status?.tone ?? tone ?? "idle"
  const text = label ?? status?.label ?? DASH
  const hint = reason ?? (status?.code ? `코드 ${status.code}` : undefined)
  const Icon = toneIcon[resolvedTone]
  return (
    <Hint text={hint} focusable>
      <span className={cn("inline-flex h-6 max-w-full items-center gap-1 rounded-md px-2 text-xs font-medium", toneSoft[resolvedTone], toneText[resolvedTone], className)}>
        <Icon className="size-3.5 shrink-0" aria-hidden="true" />
        <span className="truncate text-foreground">{text}</span>
      </span>
    </Hint>
  )
}

/** Compact state marker for dense cells: icon plus an optional visible label. */
export function StatusDot({ status, tone, label, showLabel = false, className }: { status?: StatusInfo; tone?: StatusTone; label?: string; showLabel?: boolean; className?: string }) {
  const resolvedTone = status?.tone ?? tone ?? "idle"
  const text = label ?? status?.label ?? DASH
  const Icon = toneIcon[resolvedTone]
  const body = (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)}>
      <Icon className={cn("size-3.5 shrink-0", toneText[resolvedTone])} aria-hidden="true" />
      {showLabel ? <span className="truncate">{text}</span> : <span className="sr-only">{text}</span>}
    </span>
  )
  return showLabel ? body : <Hint text={status?.code ? `${text} (${status.code})` : text}>{body}</Hint>
}

/** Label, value with unit, one secondary value. */
export function StatTile({ label, value, unit, sub, info, reason, status, href, className }: {
  label: string; value: React.ReactNode | null | undefined; unit?: string; sub?: React.ReactNode
  /** One-sentence definition, shown from an info icon. */ info?: string
  /** Why the value is absent. */ reason?: string
  status?: StatusInfo; href?: string; className?: string
}) {
  const body = (
    <>
      <div className="flex items-center gap-1.5 text-sm text-muted-foreground">
        <span className="truncate">{label}</span>
        {info && <Hint text={info} focusable><Info className="size-3.5" aria-label="설명" /></Hint>}
        {status && <StatusDot status={status} className="ml-auto" />}
      </div>
      <div className="mt-1.5 flex min-w-0 items-baseline gap-1">
        <span className="truncate text-2xl font-semibold tabular-nums tracking-tight text-foreground">{value == null || value === "" ? <Missing reason={reason} /> : value}</span>
        {unit && value != null && <span className="text-sm text-muted-foreground">{unit}</span>}
      </div>
      {sub != null && <div className="mt-1 truncate text-xs text-muted-foreground tabular-nums">{sub}</div>}
    </>
  )
  return href ? (
    <Link href={href} className={cn("block min-w-0 rounded-xl", className)}>
      <Card interactive className="h-full px-4 py-3.5">{body}</Card>
    </Link>
  ) : (
    <Card className={cn("px-4 py-3.5", className)}>{body}</Card>
  )
}

/** Grid that lays tiles out four across, two on tablets, one on phones. */
export function StatRow({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4", className)}>{children}</div>
}

/**
 * Observed latency as a share of the target. Colour is display only (design section 5):
 * below 0.7 primary, 0.7 to 1.0 warn, above 1.0 fail. The verdict text comes from the server.
 */
export function HeadroomBar({ observed, target, unit = "ms", reason, className }: { observed: number | null | undefined; target: number | null | undefined; unit?: string; reason?: string; className?: string }) {
  if (observed == null || !target || target <= 0) return <Missing reason={reason ?? "관측 없음"} />
  const ratio = observed / target
  const color = ratio > 1 ? "bg-fail" : ratio >= 0.7 ? "bg-warn" : "bg-primary"
  // The track spans 0 to 1.25 x target so overruns stay visible; the tick marks the target.
  const width = Math.min(ratio / 1.25, 1) * 100
  return (
    <span
      role="img"
      aria-label={`목표 ${target} ${unit} 대비 ${Math.round(ratio * 100)}%`}
      className={cn("relative inline-block h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-muted align-middle", className)}
    >
      <span className={cn("absolute inset-y-0 left-0 rounded-full", color)} style={{ width: `${width}%` }} />
      <span className="absolute inset-y-[-2px] left-[80%] w-px bg-foreground/50" />
    </span>
  )
}

/** Long identifier shortened to its head and tail, with the full value in a tooltip and a copy button. */
export function IdText({ value, head = 8, tail = 4, copy = true, className }: { value: string | null | undefined; head?: number; tail?: number; copy?: boolean; className?: string }) {
  if (!value) return <Missing />
  const short = value.length > head + tail + 1 ? `${value.slice(0, head)}…${value.slice(-tail)}` : value
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1", className)}>
      <Hint text={<span className="font-mono break-all">{value}</span>} focusable>
        <span className="truncate font-mono text-xs text-muted-foreground" aria-label={value}>{short}</span>
      </Hint>
      {copy && <CopyButton value={value} iconOnly variant="ghost" size="sm" label="복사" copiedLabel="복사됨" errorLabel="실패" aria-label="식별자 복사" className="size-6 text-muted-foreground" />}
    </span>
  )
}

// One shared 1 s clock for every relative time on the page.
let clockNow = Date.now()
const clockListeners = new Set<() => void>()
let clockTimer: number | undefined
function subscribeClock(listener: () => void) {
  clockListeners.add(listener)
  if (clockListeners.size === 1) clockTimer = window.setInterval(() => { clockNow = Date.now(); clockListeners.forEach(notify => notify()) }, 1000)
  return () => {
    clockListeners.delete(listener)
    if (clockListeners.size === 0) window.clearInterval(clockTimer)
  }
}
/** Current time, refreshed every second; stable on the server. */
export function useNow(): number {
  return React.useSyncExternalStore(subscribeClock, () => clockNow, () => 0)
}

/** Relative time ("3분 전") with the absolute KST time in a tooltip, or absolute KST with mode="absolute". */
export function TimeText({ at, mode = "relative", seconds = true, className }: { at: string | number | Date | null | undefined; mode?: "relative" | "absolute"; seconds?: boolean; className?: string }) {
  const now = useNow()
  const absolute = formatKst(at, seconds)
  if (absolute === DASH) return <Missing />
  const iso = new Date(typeof at === "string" ? Date.parse(at) : at instanceof Date ? at.getTime() : Number(at)).toISOString()
  if (mode === "absolute") return <time dateTime={iso} className={cn("tabular-nums", className)}>{absolute}</time>
  return (
    <Hint text={`${absolute} KST`} focusable>
      <time dateTime={iso} className={cn("tabular-nums", className)}>{now ? formatRelative(at, now) : absolute}</time>
    </Hint>
  )
}

export type FieldItem = { label: string; value: React.ReactNode | null | undefined; reason?: string; info?: string }
/** Label and value list. Absent values render "—" with their reason. */
export function Fields({ items, columns = 2, className }: { items: FieldItem[]; columns?: 1 | 2 | 3; className?: string }) {
  return (
    <dl className={cn("grid gap-x-6 gap-y-3", columns === 1 ? "grid-cols-1" : columns === 2 ? "grid-cols-1 sm:grid-cols-2" : "grid-cols-1 sm:grid-cols-2 lg:grid-cols-3", className)}>
      {items.map(item => (
        <div key={item.label} className="grid min-w-0 grid-cols-[minmax(7rem,40%)_minmax(0,1fr)] items-baseline gap-3 border-b border-border pb-2 sm:block sm:border-0 sm:pb-0">
          <dt className="flex items-center gap-1 text-xs text-muted-foreground">
            {item.label}
            {item.info && <Hint text={item.info} focusable><Info className="size-3" aria-label="설명" /></Hint>}
          </dt>
          <dd className="min-w-0 truncate text-sm text-foreground sm:mt-0.5">{item.value == null || item.value === "" ? <Missing reason={item.reason} /> : item.value}</dd>
        </div>
      ))}
    </dl>
  )
}
