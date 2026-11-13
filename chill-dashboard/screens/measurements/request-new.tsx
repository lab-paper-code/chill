"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { ConfirmDialog } from "@/components/kit"
import { ToggleGroup, ToggleGroupItem } from "@/components/custom/blocks/ToggleGroupBlock"
import { useSession } from "@/lib/client"
import { ProfileRequestForm } from "./request-profile"
import { OccupancyRequestForm } from "./request-occupancy"
import { DocumentRequestForm } from "./request-document"

type Kind = "profile" | "dispatch-occupancy"
type Method = "form" | "document"

export function MeasurementRequestNewScreen() {
  const params = useSearchParams(), router = useRouter(), pathname = usePathname(), { config } = useSession()
  const namespace = params.get("namespace") ?? config.namespace
  const kind: Kind = params.get("kind") === "dispatch-occupancy" ? "dispatch-occupancy" : "profile"
  const method: Method = kind === "profile" && params.get("method") === "document" ? "document" : "form"
  const [dirty, setDirty] = React.useState(false)
  const [leaving, setLeaving] = React.useState<string | null>(null)

  // Switching kind or method starts a blank form: prefill belongs to the form it was made for.
  function go(next: { kind: Kind; method: Method }) {
    const query = new URLSearchParams()
    if (params.get("namespace")) query.set("namespace", namespace)
    if (next.kind !== "profile") query.set("kind", next.kind)
    if (next.kind === "profile" && next.method === "document") query.set("method", "document")
    const href = `${pathname}${query.size ? `?${query}` : ""}`
    if (dirty) setLeaving(href)
    else router.replace(href, { scroll: false })
  }

  const bar = (
    <div className="flex flex-wrap items-center gap-2">
      <ToggleGroup type="single" variant="segmented" size="sm" aria-label="측정 종류" value={kind} onValueChange={value => { if (value && value !== kind) go({ kind: value as Kind, method: "form" }) }}>
        <ToggleGroupItem value="profile">처리 성능</ToggleGroupItem>
        <ToggleGroupItem value="dispatch-occupancy">분배 점유시간</ToggleGroupItem>
      </ToggleGroup>
      {kind === "profile" && (
        <ToggleGroup type="single" variant="segmented" size="sm" aria-label="작성 방법" value={method} onValueChange={value => { if (value && value !== method) go({ kind, method: value as Method }) }}>
          <ToggleGroupItem value="form">폼</ToggleGroupItem>
          <ToggleGroupItem value="document">문서 가져오기</ToggleGroupItem>
        </ToggleGroup>
      )}
    </div>
  )
  const props = { namespace, bar, onDirty: setDirty }
  return (
    <>
      {kind === "dispatch-occupancy" ? <OccupancyRequestForm {...props} /> : method === "document" ? <DocumentRequestForm {...props} /> : <ProfileRequestForm {...props} />}
      <ConfirmDialog open={leaving !== null} onOpenChange={open => { if (!open) setLeaving(null) }} title="저장하지 않은 변경 버리기" confirmLabel="버리고 이동" tone="destructive"
        onConfirm={() => { const next = leaving; setLeaving(null); setDirty(false); if (next) router.replace(next, { scroll: false }) }} />
    </>
  )
}
