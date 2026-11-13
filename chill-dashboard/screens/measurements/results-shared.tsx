"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import type { Choice } from "@/components/kit"

export const resultKinds = [
  { value: "throughput", label: "처리 성능" },
  { value: "operation-cost", label: "전환 비용" },
  { value: "dispatch-occupancy", label: "분배 점유시간" },
] as const
export type ResultKind = (typeof resultKinds)[number]["value"]

export const ALL = "all"

/** URL query state; every change replaces the entry and keeps the scroll position. */
export function useUrlState() {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const set = React.useCallback((patch: Record<string, string | null | undefined>) => {
    const next = new URLSearchParams(search.toString())
    for (const [key, value] of Object.entries(patch)) {
      if (value) next.set(key, value)
      else next.delete(key)
    }
    const query = next.toString()
    router.replace(`${pathname}${query ? `?${query}` : ""}`, { scroll: false })
  }, [search, router, pathname])
  return { search, set }
}

/** A select whose "전체" option clears the URL parameter. */
export function filterChoice(label: string, current: string | null, values: string[], onChange: (value: string | null) => void, labelOf: (value: string) => string = value => value): Choice {
  return {
    label, value: current ?? ALL, onChange: value => onChange(value === ALL ? null : value),
    options: [{ value: ALL, label: `${label} 전체` }, ...values.map(value => ({ value, label: labelOf(value) }))],
  }
}

/** Distinct, sorted, non-empty values. */
export const distinct = (values: (string | null | undefined)[]) => [...new Set(values.filter((value): value is string => !!value))].sort()
