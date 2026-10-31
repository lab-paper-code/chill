"use client"

import { StatusBadge } from "@/components/read-region"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { normalizeStatusLabel, type StatusValue } from "@/lib/status"

export function StatusCell({axis, label, tone, reason}: StatusValue) {
  const long = reason && reason.length > 40
  return <span className="inline-flex min-w-0 max-w-full flex-wrap items-center gap-x-2 gap-y-1 align-top text-sm" aria-label={axis}>
    <span className="text-xs text-muted-foreground">{axis}</span>
    <StatusBadge tone={tone}>{normalizeStatusLabel(label)}</StatusBadge>
    {reason && (long ? <Tooltip><TooltipTrigger asChild><span tabIndex={0} aria-label={reason} className="max-w-full break-words text-xs text-muted-foreground underline decoration-dotted underline-offset-4 focus-visible:outline-2 focus-visible:outline-ring">{reason.slice(0, 40)}…</span></TooltipTrigger><TooltipContent className="max-w-[min(24rem,calc(100vw-2rem))] break-words">{reason}</TooltipContent></Tooltip> : <span className="break-words text-xs text-muted-foreground">{reason}</span>)}
  </span>
}
