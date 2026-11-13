"use client"

import * as React from "react"
import { useQueryClient, type UseMutationResult } from "@tanstack/react-query"
import { Button, DataTable, Dialog, Fields, useToast, type Column } from "@/components/kit"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Textarea } from "@/components/ui/textarea"
import { useChillMutation, type ChillMutationInput } from "@/lib/client"
import type { ApiError, RelayResponse } from "@/lib/client-transport"
import { formatNumber } from "@/lib/format"
import { codeText, explain } from "@/lib/labels"
import { routingShare } from "@/lib/live-display"
import type { OperationResult } from "@/lib/read-types"
import { actionStatus } from "@/lib/status-labels"

/** One serving instance of the service: device label and its current live weight. */
export type RoutingTarget = { id: string; device: string; weight: number | undefined }
type Mode = "route" | "unroute" | "release"
const modeLabels: Record<Mode, string> = { route: "가중치 지정", unroute: "새 요청 차단", release: "기본 분배" }
const MAX_WEIGHT = 2147483647

/** Toast for a failed mutation. A lost response is "결과 미확인", never retried. */
export function failureToast(error: ApiError): { title: string; description?: string; type: "warning" | "error" } {
  if (error.unknownOutcome || error.status === 0) return { title: "결과 미확인", type: "warning" }
  const code = (error.data as { code?: unknown } | null)?.code
  // A known code reads as its sentence; an unknown one stays out of the layout (원본 is in the response).
  const description = typeof code === "string" && Object.hasOwn(codeText, code) ? explain(code)[0] : undefined
  if (error.status === 409) return { title: "다른 변경과 충돌", description: description ?? "최신 상태를 다시 읽음", type: "error" }
  if (error.status === 401 || error.status === 403) return { title: "권한 없음", description, type: "error" }
  return { title: "실패", description: description ?? `HTTP ${error.status}`, type: "error" }
}

/** Per-instance weights, block new requests, or return to the scheduler's routing; POST v1/scheduler/operations. */
type RoutingDialogProps = {
  open: boolean; onOpenChange: (open: boolean) => void
  name: string; uid: string
  targets: RoutingTarget[]
  /** The stored routing declaration for this service, when one exists. */
  declared: Record<string, number> | undefined
}
export function RoutingDialog(props: RoutingDialogProps) {
  const mutation = useChillMutation()
  // A fresh form each time the dialog opens, so the draft starts from the current routing.
  const [opened, setOpened] = React.useState(0)
  const [wasOpen, setWasOpen] = React.useState(props.open)
  if (props.open !== wasOpen) {
    setWasOpen(props.open)
    if (props.open) setOpened(count => count + 1)
  }
  return <RoutingForm key={opened} {...props} mutation={mutation} />
}

function RoutingForm({ open, onOpenChange, name, uid, targets, declared, mutation }: RoutingDialogProps & { mutation: UseMutationResult<RelayResponse, ApiError, ChillMutationInput> }) {
  const { toast } = useToast()
  const client = useQueryClient()
  const reasonId = React.useId()
  const [mode, setMode] = React.useState<Mode>(targets.length ? "route" : declared ? "release" : "unroute")
  const [draft, setDraft] = React.useState<Record<string, string>>(() => Object.fromEntries(targets.map(target => [target.id, String(declared?.[target.id] ?? target.weight ?? 0)])))
  const [reason, setReason] = React.useState("")
  const current = Object.fromEntries(targets.flatMap(target => target.weight == null ? [] : [[target.id, target.weight]]))
  const weights = Object.fromEntries(targets.map(target => [target.id, Number(draft[target.id])]).filter(([, weight]) => Number(weight) > 0)) as Record<string, number>
  const weightsValid = targets.every(target => /^\d+$/.test(draft[target.id] ?? "") && Number(draft[target.id]) <= MAX_WEIGHT) && Object.keys(weights).length > 0
  const ready = (mode !== "route" || weightsValid) && reason.trim() !== ""

  const after = (target: RoutingTarget): React.ReactNode =>
    mode === "release" ? null : mode === "unroute" ? "0" : weightsValid ? formatNumber(routingShare(weights, target.id) ?? 0, 1) : null
  const columns: Column<RoutingTarget>[] = [
    { key: "device", header: "장치", cell: target => target.device },
    { key: "before", header: "현재", unit: "%", numeric: true, cell: target => current[target.id] == null ? null : formatNumber(routingShare(current, target.id), 1), missingReason: "분배 없음" },
    ...(mode === "route" ? [{
      key: "weight", header: "가중치", numeric: true, width: "8rem",
      cell: (target: RoutingTarget) => (
        <InputBlock
          type="number" inputSize="sm" aria-label={`${target.device} 가중치`} required min={0} max={MAX_WEIGHT} step={1}
          value={draft[target.id] ?? ""} onChange={event => setDraft(values => ({ ...values, [target.id]: event.target.value }))}
          disabled={mutation.isPending}
        />
      ),
    }] : []),
    { key: "after", header: "변경 후", unit: "%", numeric: true, cell: after, missingReason: mode === "release" ? "스케줄러가 정함" : "가중치 확인 필요" },
  ]

  const submit = (event: React.FormEvent) => {
    event.preventDefault()
    if (!ready || mutation.isPending) return
    const body = { kind: mode, workloadUID: uid, ...(mode === "route" ? { weights } : {}), reason: reason.trim() }
    mutation.mutate({ path: "v1/scheduler/operations", method: "POST", body }, {
      onSuccess: response => {
        const result = response.data as Partial<OperationResult> | null
        if (typeof result?.status === "string" && (result.status !== "complete" || result.error)) toast({ title: "분배 실패", description: actionStatus(result.status).label, type: "error" })
        else toast({ title: result?.status ? "분배 적용됨" : "분배 요청 접수됨", type: "success" })
        onOpenChange(false)
      },
      onError: error => {
        toast(failureToast(error))
        if (error.unknownOutcome) onOpenChange(false)
      },
      onSettled: () => client.invalidateQueries({ predicate: query => typeof query.queryKey[1] === "string" && query.queryKey[1].startsWith("v1/scheduler") }),
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="분배 수정" size="lg" busy={mutation.isPending} onSubmit={submit} footer={<>
      <Button type="button" variant="outline" disabled={mutation.isPending} onClick={() => onOpenChange(false)}>취소</Button>
      <Button type="submit" variant="default" disabled={!ready} loading={mutation.isPending}>적용</Button>
    </>}>
        <RadioGroup value={mode} onValueChange={value => setMode(value as Mode)} className="flex flex-wrap gap-x-5 gap-y-2" aria-label="분배 방식" disabled={mutation.isPending}>
          <Label><RadioGroupItem value="route" disabled={!targets.length} />{modeLabels.route}</Label>
          <Label><RadioGroupItem value="unroute" />{modeLabels.unroute}</Label>
          <Label><RadioGroupItem value="release" disabled={!declared} />{modeLabels.release}</Label>
        </RadioGroup>
        <Fields columns={3} items={[
          { label: "대상", value: name },
          { label: "현재", value: modeLabels[!declared ? "release" : Object.keys(declared).length ? "route" : "unroute"] },
          { label: "변경 후", value: modeLabels[mode] },
        ]} />
        {targets.length > 0 && <DataTable label="분배 변경 전후" rows={targets} rowKey={target => target.id} columns={columns} pageSize={0} />}
        <div className="grid gap-1.5">
          <Label htmlFor={reasonId}>사유</Label>
          <Textarea id={reasonId} value={reason} onChange={event => setReason(event.target.value)} required maxLength={500} rows={2} disabled={mutation.isPending} className="user-invalid:border-fail" />
        </div>
    </Dialog>
  )
}
