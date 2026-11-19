"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { ConfirmDialog } from "@/components/kit"
import { ToggleGroup, ToggleGroupItem } from "@/components/custom/blocks/ToggleGroupBlock"
import { useSession } from "@/lib/client"
import { ProfileRequestForm } from "./request-profile"
import { DocumentRequestForm } from "./request-document"

type Method = "form" | "document"

export function MeasurementRequestNewScreen() {
  const params = useSearchParams(), router = useRouter(), pathname = usePathname(), { config } = useSession()
  const namespace = params.get("namespace") ?? config.namespace
  const method: Method = params.get("method") === "document" ? "document" : "form"
  const [dirty, setDirty] = React.useState(false)
  const [leaving, setLeaving] = React.useState<string | null>(null)

  // Switching method starts a blank form: prefill belongs to the form it was made for.
  function go(next: Method) {
    const query = new URLSearchParams()
    if (params.get("namespace")) query.set("namespace", namespace)
    if (next === "document") query.set("method", "document")
    const href = `${pathname}${query.size ? `?${query}` : ""}`
    if (dirty) setLeaving(href)
    else router.replace(href, { scroll: false })
  }

  const bar = (
    <ToggleGroup type="single" variant="segmented" size="sm" aria-label="작성 방법" value={method} onValueChange={value => { if (value && value !== method) go(value as Method) }}>
      <ToggleGroupItem value="form">폼</ToggleGroupItem>
      <ToggleGroupItem value="document">문서 가져오기</ToggleGroupItem>
    </ToggleGroup>
  )
  const props = { namespace, bar, onDirty: setDirty }
  return (
    <>
      {method === "document" ? <DocumentRequestForm {...props} /> : <ProfileRequestForm {...props} />}
      <ConfirmDialog open={leaving !== null} onOpenChange={open => { if (!open) setLeaving(null) }} title="저장하지 않은 변경 버리기" confirmLabel="버리고 이동" tone="destructive"
        onConfirm={() => { const next = leaving; setLeaving(null); setDirty(false); if (next) router.replace(next, { scroll: false }) }} />
    </>
  )
}
