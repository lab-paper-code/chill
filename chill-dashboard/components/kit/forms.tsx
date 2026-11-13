"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { CalendarRange, Search } from "lucide-react"
import { cn } from "@/lib/utils"
import { formatKst } from "@/lib/format"
import Button from "@/components/custom/blocks/Button"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { SelectBlock, type SelectOption } from "@/components/custom/blocks/SelectBlock"
import { ToggleGroup, ToggleGroupItem } from "@/components/custom/blocks/ToggleGroupBlock"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/custom/blocks/PopoverBlock"
import { Label } from "@/components/ui/label"
import { ConfirmDialog } from "./overlays"
import { Hint } from "./display"

/** A titled group of form fields (fieldset + legend), on a card. */
export function FormSection({ title, info, children, className }: { title: string; info?: string; children: React.ReactNode; className?: string }) {
  return (
    <fieldset className={cn("min-w-0 rounded-xl border border-border bg-card p-5", className)}>
      <legend className="float-left mb-4 flex w-full items-center gap-1.5 text-sm font-semibold">
        {title}
        {info && <Hint text={info} focusable><span className="text-xs font-normal text-muted-foreground">?</span></Hint>}
      </legend>
      <div className="clear-left grid gap-4">{children}</div>
    </fieldset>
  )
}

/** Sticky bottom row: change count, secondary actions and the submit button. */
export function FormActions({ changes, children, className }: { changes?: number; children: React.ReactNode; className?: string }) {
  return (
    <div className={cn("sticky bottom-0 z-10 -mx-4 flex items-center gap-2 border-t border-border bg-background/95 px-4 py-3 sm:-mx-6 sm:px-6", className)}>
      {changes != null && <span role="status" className="text-sm tabular-nums text-muted-foreground">{changes ? `변경 ${changes}건` : "변경 없음"}</span>}
      <div className="ml-auto flex items-center gap-2">{children}</div>
    </div>
  )
}

/**
 * Asks before leaving a form with unsaved changes: browser reload/close (beforeunload)
 * and in-app link clicks. ponytail: browser Back is not intercepted; add a history guard if it matters.
 */
export function LeaveGuard({ when }: { when: boolean }) {
  const router = useRouter()
  const [target, setTarget] = React.useState<string | null>(null)
  React.useEffect(() => {
    if (!when) return
    const beforeUnload = (event: BeforeUnloadEvent) => { event.preventDefault() }
    const click = (event: MouseEvent) => {
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
      const link = event.target instanceof Element ? event.target.closest("a[href]") : null
      if (!(link instanceof HTMLAnchorElement) || link.target === "_blank" || link.hasAttribute("download")) return
      const url = new URL(link.href)
      if (url.origin !== window.location.origin || (url.pathname === window.location.pathname && url.search === window.location.search)) return
      event.preventDefault()
      event.stopPropagation()
      setTarget(`${url.pathname}${url.search}${url.hash}`)
    }
    window.addEventListener("beforeunload", beforeUnload)
    document.addEventListener("click", click, true)
    return () => {
      window.removeEventListener("beforeunload", beforeUnload)
      document.removeEventListener("click", click, true)
    }
  }, [when])
  return (
    <ConfirmDialog
      open={target !== null}
      onOpenChange={open => { if (!open) setTarget(null) }}
      title="저장하지 않은 변경 버리기"
      confirmLabel="버리고 이동"
      tone="destructive"
      onConfirm={() => { const next = target; setTarget(null); if (next) router.push(next) }}
    />
  )
}

export type Segment = { label: string; value: string; options: { value: string; label: string; count?: number }[]; onChange: (value: string) => void }
export type Choice = { label: string; value: string; options: SelectOption[]; onChange: (value: string) => void; searchable?: boolean }

/** Search box, segmented choices and selects in one wrapping row. */
export function FilterBar({ search, segments, selects, end, className }: {
  search?: { value: string; onChange: (value: string) => void; placeholder?: string; label?: string }
  segments?: Segment[]
  selects?: Choice[]
  end?: React.ReactNode
  className?: string
}) {
  return (
    <div role="search" className={cn("flex min-w-0 flex-wrap items-center gap-2", className)}>
      {search && (
        <div className="w-full sm:w-60">
          <InputBlock
            type="text" inputSize="sm" aria-label={search.label ?? "검색"} placeholder={search.placeholder ?? "검색"}
            leftIcon={<Search />} value={search.value} onChange={event => search.onChange(event.target.value)}
          />
        </div>
      )}
      {segments?.map(segment => (
        <ToggleGroup key={segment.label} type="single" variant="segmented" size="sm" aria-label={segment.label} value={segment.value} onValueChange={value => { if (value) segment.onChange(value) }}>
          {segment.options.map(option => (
            <ToggleGroupItem key={option.value} value={option.value}>
              {option.label}{option.count != null && <span className="ml-1 tabular-nums text-muted-foreground">{option.count}</span>}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      ))}
      {selects?.map(choice => (
        <div key={choice.label} className="w-full sm:w-44">
          <SelectBlock inputSize="sm" options={choice.options} value={choice.value} onChange={choice.onChange} placeholder={choice.label} searchable={choice.searchable} />
        </div>
      ))}
      {end && <div className="ml-auto flex items-center gap-2">{end}</div>}
    </div>
  )
}

export const TIME_PRESETS = { "15m": { label: "15분", ms: 15 * 60_000 }, "1h": { label: "1시간", ms: 3_600_000 }, "24h": { label: "24시간", ms: 86_400_000 } } as const
export type TimePreset = keyof typeof TIME_PRESETS
export type TimeRangeValue = { preset: TimePreset } | { from: number; to: number }

/** The absolute window of a range value at `now`. */
export function resolveTimeRange(value: TimeRangeValue, now: number): { from: number; to: number } {
  return "preset" in value ? { from: now - TIME_PRESETS[value.preset].ms, to: now } : value
}

// datetime-local carries no zone; the dashboard reads and writes it as KST.
const toKstInput = (ms: number) => formatKst(ms, false).replace(" ", "T")
const fromKstInput = (text: string) => Date.parse(`${text}:00+09:00`)

/** Recent-window presets allowed by the screen plus "직접 선택" (KST, bounded by `maxSpanMs`). */
export function TimeRange({ value, onChange, presets, custom }: {
  value: TimeRangeValue
  onChange: (value: TimeRangeValue) => void
  presets: TimePreset[]
  /** Enables "직접 선택"; the span is limited to `maxSpanMs`. */
  custom?: { maxSpanMs: number }
}) {
  const current = "preset" in value ? value.preset : "custom"
  return (
    <div className="flex items-center gap-2">
      <ToggleGroup type="single" variant="segmented" size="sm" aria-label="구간" value={current} onValueChange={next => { if (next && next !== "custom") onChange({ preset: next as TimePreset }) }}>
        {presets.map(preset => <ToggleGroupItem key={preset} value={preset}>{TIME_PRESETS[preset].label}</ToggleGroupItem>)}
        {custom && <ToggleGroupItem value="custom" disabled={current !== "custom"} className={current !== "custom" ? "hidden" : undefined}>직접</ToggleGroupItem>}
      </ToggleGroup>
      {custom && <CustomRange value={value} onChange={onChange} maxSpanMs={custom.maxSpanMs} />}
    </div>
  )
}

function CustomRange({ value, onChange, maxSpanMs }: { value: TimeRangeValue; onChange: (value: TimeRangeValue) => void; maxSpanMs: number }) {
  const [initial] = React.useState(() => resolveTimeRange(value, Date.now()))
  const [from, setFrom] = React.useState(() => toKstInput(initial.from))
  const [to, setTo] = React.useState(() => toKstInput(initial.to))
  const fromId = React.useId(), toId = React.useId()
  const start = fromKstInput(from), end = fromKstInput(to)
  const error = !Number.isFinite(start) || !Number.isFinite(end) ? "시각 형식 오류" : end <= start ? "끝이 시작보다 늦어야 함" : end - start > maxSpanMs ? `최대 ${Math.round(maxSpanMs / 3_600_000)}시간` : ""
  return (
    <Popover>
      <PopoverTrigger className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-card px-2.5 text-sm hover:bg-muted">
        <CalendarRange className="size-4" aria-hidden="true" />직접 선택
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-4 text-sm lg:p-4 lg:text-sm">
        <form className="grid gap-3" onSubmit={event => { event.preventDefault(); if (!error) onChange({ from: start, to: end }) }}>
          <div className="grid gap-1.5"><Label htmlFor={fromId}>시작 (KST)</Label><input id={fromId} type="datetime-local" required value={from} onChange={event => setFrom(event.target.value)} className="h-9 rounded-md border border-input bg-card px-2 tabular-nums" /></div>
          <div className="grid gap-1.5"><Label htmlFor={toId}>끝 (KST)</Label><input id={toId} type="datetime-local" required value={to} onChange={event => setTo(event.target.value)} className="h-9 rounded-md border border-input bg-card px-2 tabular-nums" /></div>
          <p role="status" className="min-h-5 text-xs text-fail">{error}</p>
          <Button type="submit" size="sm" variant="default" disabled={!!error}>적용</Button>
        </form>
      </PopoverContent>
    </Popover>
  )
}

/**
 * Time range kept in the URL: `?range=15m` or `?from=<ms>&to=<ms>`, so reload and Back keep it.
 * Values outside `allowed` fall back to `fallback`.
 */
export function useTimeRangeParam(allowed: TimePreset[], fallback: TimePreset, maxSpanMs?: number): [TimeRangeValue, (value: TimeRangeValue) => void] {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const range = search.get("range"), from = Number(search.get("from")), to = Number(search.get("to"))
  const value: TimeRangeValue = range && (allowed as string[]).includes(range) ? { preset: range as TimePreset }
    : maxSpanMs && Number.isSafeInteger(from) && Number.isSafeInteger(to) && to > from && to - from <= maxSpanMs ? { from, to }
    : { preset: fallback }
  const set = React.useCallback((next: TimeRangeValue) => {
    const params = new URLSearchParams(search.toString())
    params.delete("range"); params.delete("from"); params.delete("to")
    if ("preset" in next) { if (next.preset !== fallback) params.set("range", next.preset) }
    else { params.set("from", String(next.from)); params.set("to", String(next.to)) }
    const query = params.toString()
    router.replace(`${pathname}${query ? `?${query}` : ""}`, { scroll: false })
  }, [search, router, pathname, fallback])
  return [value, set]
}
