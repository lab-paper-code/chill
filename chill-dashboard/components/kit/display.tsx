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

/**
 * Short hover/focus hint around any content. Nothing is wrapped when `text` is empty.
 * `focusable` makes the wrapper a tab stop whose description is the hint from the start, so a screen
 * reader reads it on focus (the visible tooltip opens later and is hidden from assistive tech).
 */
export function Hint({ text, children, focusable = false, className }: { text?: React.ReactNode; children: React.ReactNode; focusable?: boolean; className?: string }) {
  const id = React.useId()
  if (!text) return <>{children}</>
  return (
    <Tooltip>
      <TooltipTrigger className={className} tabIndex={focusable ? 0 : undefined} aria-describedby={focusable ? id : undefined}>{children}</TooltipTrigger>
      <TooltipContent aria-hidden={focusable || undefined} className="max-w-xs whitespace-normal">{text}</TooltipContent>
      {focusable && <span id={id} hidden>{text}</span>}
    </Tooltip>
  )
}

/** An "i" button next to a label whose description is a one-sentence definition (hover or focus shows it). */
export function InfoHint({ text, className }: { text: string; className?: string }) {
  const id = React.useId()
  return (
    <Tooltip>
      <TooltipTrigger className={cn("inline-flex", className)}>
        <button type="button" aria-label="설명" aria-describedby={id} className="-m-[5px] grid size-6 shrink-0 place-items-center rounded-md text-muted-foreground hover:text-foreground">
          <Info className="size-3.5" aria-hidden="true" />
        </button>
      </TooltipTrigger>
      <TooltipContent aria-hidden="true" className="max-w-xs whitespace-normal">{text}</TooltipContent>
      <span id={id} hidden>{text}</span>
    </Tooltip>
  )
}

/** "—" for an absent value, with the reason in a tooltip. */
export function Missing({ reason }: { reason?: React.ReactNode }) {
  return (
    <Hint text={reason} focusable>
      <span className="text-muted-foreground"><span aria-hidden="true">{DASH}</span><span className="sr-only">값 없음</span></span>
    </Hint>
  )
}

/**
 * One line of content. While it is cut off, the full text shows in a tooltip, and plain text becomes
 * a tab stop so keyboard users can read it too.
 */
export function Clip({ children, className }: { children: React.ReactNode; className?: string }) {
  const ref = React.useRef<HTMLSpanElement>(null)
  const [full, setFull] = React.useState<string | null>(null)
  const measure = React.useCallback(() => {
    const element = ref.current
    if (!element) return
    const text = element.scrollWidth > element.clientWidth + 1 ? element.textContent : null
    setFull(current => (current === text ? current : text))
  }, [])
  React.useLayoutEffect(measure)
  React.useEffect(() => {
    if (!ref.current) return
    const observer = new ResizeObserver(measure)
    observer.observe(ref.current)
    return () => observer.disconnect()
  }, [measure])
  const text = typeof children === "string" || typeof children === "number"
  return (
    <Tooltip>
      <TooltipTrigger className={cn("block min-w-0", className)} tabIndex={full && text ? 0 : undefined}>
        <span ref={ref} className="block truncate">{children}</span>
      </TooltipTrigger>
      {full && <TooltipContent aria-hidden="true" className="max-w-sm whitespace-normal break-all">{full}</TooltipContent>}
    </Tooltip>
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
      <span className={cn("inline-flex min-h-6 max-w-full items-center gap-1 rounded-md px-2 text-xs font-medium", toneSoft[resolvedTone], toneText[resolvedTone], className)}>
        <Icon className="size-3.5 shrink-0" aria-hidden="true" />
        <span className="truncate text-foreground">{text}</span>
      </span>
    </Hint>
  )
}

/** Compact state marker for dense cells: icon plus an optional visible label. A bare `tone` (no label) is decoration only. */
export function StatusDot({ status, tone, label, showLabel = false, className }: { status?: StatusInfo; tone?: StatusTone; label?: string; showLabel?: boolean; className?: string }) {
  const resolvedTone = status?.tone ?? tone ?? "idle"
  const text = label ?? status?.label ?? DASH
  const Icon = toneIcon[resolvedTone]
  const named = showLabel || label != null || status != null
  const body = (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)}>
      <Icon className={cn("size-3.5 shrink-0", toneText[resolvedTone])} aria-hidden="true" />
      {showLabel ? <span className="truncate">{text}</span> : named && <span className="sr-only">{text}</span>}
    </span>
  )
  return showLabel || !named ? body : <Hint text={status?.code ? `${text} (${status.code})` : text}>{body}</Hint>
}

/** Label, value with unit, one secondary value. */
export function StatTile({ label, value, unit, sub, info, reason, status, href, className }: {
  label: string; value: React.ReactNode | null | undefined; unit?: string; sub?: React.ReactNode
  /** One-sentence definition, shown from an info icon. */ info?: string
  /** Why the value is absent. */ reason?: string
  status?: StatusInfo; href?: string; className?: string
}) {
  // With `href` the label is the link and its ::after covers the card (stretched link), so the info
  // hint stays a separate control above it instead of being nested inside the link.
  const body = (
    <>
      <div className="flex min-w-0 items-center gap-1.5 text-sm text-muted-foreground">
        {href
          ? <Link href={href} className="min-w-0 truncate outline-none after:absolute after:inset-0 after:rounded-xl after:content-['']">{label}</Link>
          : <Clip>{label}</Clip>}
        {info && <InfoHint text={info} />}
        {status && <StatusDot status={status} className="relative z-[1] ml-auto" />}
      </div>
      <div className="mt-1.5 flex min-w-0 items-baseline gap-1">
        <span className="truncate text-2xl font-semibold tabular-nums tracking-tight text-foreground">{value == null || value === "" ? <Missing reason={reason} /> : value}</span>
        {unit && value != null && <span className="text-sm text-muted-foreground">{unit}</span>}
      </div>
      {sub != null && <div className="mt-1 text-xs text-muted-foreground tabular-nums"><Clip>{sub}</Clip></div>}
    </>
  )
  return (
    <Card
      interactive={!!href}
      // Other controls and tooltip triggers in a linked tile sit above the stretched link so they stay operable.
      className={cn("relative min-w-0 px-4 py-3.5", href && "has-[a:focus-visible]:outline-2 has-[a:focus-visible]:outline-offset-2 has-[a:focus-visible]:outline-primary [&_:is(button,[tabindex],[aria-describedby]):not(a)]:relative [&_:is(button,[tabindex],[aria-describedby]):not(a)]:z-[1]", className)}
    >
      {body}
    </Card>
  )
}

const statColumns = { 2: "lg:grid-cols-2", 3: "lg:grid-cols-3", 4: "lg:grid-cols-4", 5: "lg:grid-cols-5", 6: "lg:grid-cols-6" } as const
/** Grid of tiles: `columns` across on desktops, two across on tablets and phones, where an odd last tile spans the row. */
export function StatRow({ children, columns = 4, className }: { children: React.ReactNode; columns?: 2 | 3 | 4 | 5 | 6; className?: string }) {
  return <div className={cn("grid grid-cols-2 gap-3 max-lg:[&>:last-child:nth-child(odd)]:col-span-2", statColumns[columns], className)}>{children}</div>
}

const toneFill: Record<StatusTone, string> = { ok: "bg-ok", warn: "bg-warn", fail: "bg-fail", progress: "bg-primary", idle: "bg-idle" }
/** Thin bar for a value out of a maximum, in a cell or a tile. Display only: the colour never states a verdict. */
export function MeterBar({ value, max, label, tone = "progress", marker, reason, width = "cell", className }: {
  value: number | null | undefined; max: number
  /** Accessible name, e.g. "전력 12.4 / 30 W". */ label: string
  tone?: StatusTone
  /** A tick at this value, e.g. a limit or target (same unit as `value`). */ marker?: number
  /** Why the value is absent. */ reason?: string
  /** "cell": 64 px inline; "full": the width of its container. */ width?: "cell" | "full"
  className?: string
}) {
  if (value == null || !Number.isFinite(value) || !(max > 0)) return <Missing reason={reason} />
  const share = (n: number) => Math.min(Math.max(n / max, 0), 1) * 100
  return (
    <span
      role="meter" aria-label={label} aria-valuenow={value} aria-valuemin={0} aria-valuemax={max}
      className={cn("relative h-1.5 shrink-0 overflow-hidden rounded-full bg-muted align-middle", width === "full" ? "block w-full" : "inline-block w-16", className)}
    >
      <span className={cn("absolute inset-y-0 left-0 rounded-full", toneFill[tone])} style={{ width: `${share(value)}%` }} />
      {marker != null && <span aria-hidden="true" className="absolute inset-y-0 w-0.5 bg-foreground/60" style={{ left: `min(${share(marker)}%, 100% - 2px)` }} />}
    </span>
  )
}

export type FeedItem = {
  key: string
  title: React.ReactNode
  status?: StatusInfo; tone?: StatusTone
  /** One secondary value after the title, e.g. the target name. */ secondary?: React.ReactNode
  /** Relative time on the right ("3분 전"), absolute KST on hover. */ at?: string | number | Date | null
  /** Replaces the time on the right. */ meta?: React.ReactNode
  href?: string
}
/** One-line list rows: status mark, title, secondary text and time. Rows link when `href` is set. */
export function Feed({ items, empty = "항목 없음", label }: { items: FeedItem[]; empty?: string; label?: string }) {
  const now = useNow()
  if (!items.length) return <p className="py-2 text-sm text-muted-foreground">{empty}</p>
  return (
    <ul aria-label={label} className="-mx-2 flex flex-col">
      {items.map(item => {
        const body = (
          <>
            {(item.status || item.tone) && <StatusDot status={item.status} tone={item.tone} />}
            <span className="min-w-0 max-w-[60%] shrink-0 truncate font-medium text-foreground">{item.title}</span>
            {item.secondary != null && <span className="min-w-0 flex-1 truncate text-muted-foreground">{item.secondary}</span>}
            <span className="ml-auto shrink-0 pl-2 text-xs tabular-nums text-muted-foreground">
              {item.meta ?? (item.at ? <Hint text={`${formatKst(item.at)} KST`}><time dateTime={new Date(item.at).toISOString()}>{now ? formatRelative(item.at, now) : formatKst(item.at)}</time></Hint> : null)}
            </span>
          </>
        )
        const row = "flex h-9 min-w-0 items-center gap-2 rounded-md px-2 text-sm"
        return <li key={item.key}>{item.href ? <Link href={item.href} className={cn(row, "hover:bg-muted")}>{body}</Link> : <div className={row}>{body}</div>}</li>
      })}
    </ul>
  )
}

export type StepItem = { label: string; status: "done" | "current" | "failed" | "pending"; /** Tooltip, e.g. a failure reason. */ reason?: string }
const stepTone = { done: "ok", current: "progress", failed: "fail", pending: "idle" } as const
const stepWord = { done: "완료", current: "진행 중", failed: "실패", pending: "대기" } as const
/** Ordered stages and their state in one wrapping row. */
export function Steps({ items, label, className }: { items: StepItem[]; label: string; className?: string }) {
  return (
    <ol aria-label={label} className={cn("flex flex-wrap items-center gap-x-2 gap-y-2", className)}>
      {items.map((item, index) => {
        const Icon = toneIcon[stepTone[item.status]]
        return (
          <li key={`${index}-${item.label}`} aria-current={item.status === "current" ? "step" : undefined} className="flex items-center gap-2">
            {index > 0 && <span aria-hidden="true" className="h-px w-3 bg-border sm:w-6" />}
            <Hint text={item.reason} focusable className="inline-flex">
              <span className={cn("inline-flex items-center gap-1.5 text-sm", item.status === "pending" ? "text-muted-foreground" : "text-foreground", item.status === "current" && "font-medium")}>
                <Icon className={cn("size-4 shrink-0", toneText[stepTone[item.status]])} aria-hidden="true" />
                {item.label}<span className="sr-only">: {stepWord[item.status]}</span>
              </span>
            </Hint>
          </li>
        )
      })}
    </ol>
  )
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
            {item.info && <InfoHint text={item.info} />}
          </dt>
          <dd className="min-w-0 text-sm text-foreground sm:mt-0.5">{item.value == null || item.value === "" ? <Missing reason={item.reason} /> : <Clip>{item.value}</Clip>}</dd>
        </div>
      ))}
    </dl>
  )
}
