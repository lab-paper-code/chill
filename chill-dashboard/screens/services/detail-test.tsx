"use client"

import * as React from "react"
import { Button, EmptyState, Fields, RawDialog, Section, StatusBadge, FormSection, useNow, useToast, type FieldItem } from "@/components/kit"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import type { UseMutationResult } from "@tanstack/react-query"
import { useChillMutation, useChillQuery, useSession, type ChillMutationInput } from "@/lib/client"
import { errorDetails, type ApiError, type RelayResponse } from "@/lib/client-transport"
import { formatText, rawInferenceBody, rawOutputs, type RawField, type WorkloadView } from "@/lib/domain"
import { formatBytes, formatDurationNanos } from "@/lib/format"
import { errorCodeLabel } from "@/lib/status-labels"
import type { ObservationResult, RequestRow } from "@/lib/read-types"
import { requestError, requestMs, requestResult } from "@/lib/services-records"
import { StageBars } from "./stage-bars"

export type ServiceTabProps = { name: string; uid: string | undefined; workload: WorkloadView | undefined }

const encoder = new TextEncoder()
const decoder = new TextDecoder()
// How long the completion record is awaited after the response; the server writes it right after the batch ends.
const RECORD_WAIT_MS = 20_000

const goUnits: Record<string, number> = { ns: 1e-6, us: 1e-3, "µs": 1e-3, "μs": 1e-3, ms: 1, s: 1000, m: 60_000, h: 3_600_000 }
/** Go duration text ("500ms", "1.5s", "1m30s") in milliseconds; undefined when it is not one. The server parses what is sent. */
function goDurationMs(text: string): number | undefined {
  const parts = [...text.trim().matchAll(/(\d+\.?\d*|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g)]
  if (!parts.length || parts.map(part => part[0]).join("") !== text.trim()) return undefined
  return parts.reduce((sum, [, value, unit]) => sum + Number(value) * goUnits[unit], 0)
}

const isJpeg = (bytes: Uint8Array) => bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff

const readDataUrl = (file: File) => new Promise<string>((resolve, reject) => {
  const reader = new FileReader()
  reader.onload = () => resolve(String(reader.result))
  reader.onerror = () => reject(reader.error)
  reader.readAsDataURL(file)
})

export function ServiceTestTab({ workload }: ServiceTabProps) {
  if (!workload) return <EmptyState title="서비스 정보 없음" />
  return <TestForm key={workload.desired.metadata.uid} workload={workload} />
}

function TestForm({ workload }: { workload: WorkloadView }) {
  const { config } = useSession()
  const { toast } = useToast()
  const now = useNow()
  const mutation = useChillMutation()
  const fields = React.useMemo(() => workload.appliedDefinition?.preprocessor.input ?? [], [workload.appliedDefinition])
  const outputFields = workload.appliedDefinition?.preprocessor.output ?? []
  const [values, setValues] = React.useState<Record<string, Uint8Array>>({})
  const [previews, setPreviews] = React.useState<Record<string, { name: string; url: string }>>({})
  const [problems, setProblems] = React.useState<Record<string, string>>({})
  const [timeout, setTimeoutText] = React.useState(config.inferTimeoutMs % 1000 ? `${config.inferTimeoutMs}ms` : `${config.inferTimeoutMs / 1000}s`)
  const [span, setSpan] = React.useState<{ from: string; to: string } | null>(null)
  const [raw, setRaw] = React.useState(false)

  const waitMs = goDurationMs(timeout)
  const timeoutProblem = waitMs === undefined || waitMs <= 0 ? "예: 500ms, 1.5s, 20s" : waitMs > config.inferTimeoutMs ? `최대 ${formatDurationNanos(config.inferTimeoutMs * 1e6)}` : ""
  const duplicate = (field: RawField) => fields.filter(other => other.format === field.format).length > 1
  const title = (field: RawField) => duplicate(field) ? `${formatText(field.format)} ${field.name}` : formatText(field.format)

  const blocked = !fields.length ? "모델 입출력 처리 없음"
    : !workload.admission ? `접수 중지${workload.desired.status?.admission.reason ? `: ${errorCodeLabel(workload.desired.status.admission.reason)}` : ""}` : ""
  const missing = fields.some(field => !values[field.name]?.length)
  const disabledReason = blocked || (missing ? "입력 필요" : "") || timeoutProblem || Object.values(problems).find(Boolean) || ""

  async function pick(field: RawField, file: File | undefined) {
    if (!file) return
    const bytes = file.size > field.maxBytes ? undefined : new Uint8Array(await file.arrayBuffer())
    const problem = !bytes ? `최대 ${formatBytes(field.maxBytes)}` : isJpeg(bytes) ? "" : "JPEG만 보낼 수 있음"
    setProblems(previous => ({ ...previous, [field.name]: problem }))
    if (!bytes || problem) return
    const url = await readDataUrl(file)
    setPreviews(previous => ({ ...previous, [field.name]: { name: file.name, url } }))
    setValues(previous => ({ ...previous, [field.name]: bytes }))
  }
  function type(field: RawField, text: string) {
    const bytes = encoder.encode(text)
    setProblems(previous => ({ ...previous, [field.name]: bytes.length > field.maxBytes ? `최대 ${formatBytes(field.maxBytes)}` : "" }))
    setValues(previous => ({ ...previous, [field.name]: bytes }))
  }

  function send() {
    let body: string
    try { body = rawInferenceBody(fields, values) } catch (error) { toast({ title: "전송하지 않음", description: error instanceof Error ? error.message : undefined, type: "error" }); return }
    if (encoder.encode(body).byteLength > config.maxRequestBytes) { toast({ title: "전송하지 않음", description: `요청 크기 최대 ${formatBytes(config.maxRequestBytes)}`, type: "error" }); return }
    const from = new Date().toISOString()
    setSpan({ from, to: "" })
    const done = () => setSpan({ from, to: new Date(Date.now() + 1000).toISOString() })
    mutation.mutate({ path: `v1/workloads/${encodeURIComponent(workload.desired.metadata.name)}/infer`, method: "POST", body, timeout }, {
      onSuccess: () => { done(); toast({ title: "응답 받음", type: "success" }) },
      onError: error => { done(); toast(error.unknownOutcome ? { title: "결과 미확인", description: "자동으로 다시 보내지 않음", type: "warning" } : { title: "전송 실패", type: "error" }) },
    })
  }

  const received = mutation.data ?? mutation.error
  const requestID = received?.requestID
  const workloadUID = received?.workloadUID ?? workload.desired.metadata.uid
  const finished = !!span?.to
  const observation = useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(workloadUID)}?${new URLSearchParams({ from: span?.from ?? "", to: span?.to ?? "", limit: String(config.pageSize) })}`, {
    enabled: !!requestID && finished,
    poll: data => !data?.requests?.some(row => row.requestID === requestID && row.kind === "final") && Date.now() - Date.parse(span?.to ?? "") < RECORD_WAIT_MS,
  })
  const record = observation.data?.requests?.find(row => row.requestID === requestID && row.kind === "final")
  const waiting = !!requestID && !record && finished && now - Date.parse(span?.to ?? "") < RECORD_WAIT_MS

  const form = (
    <FormSection title="요청">
      {fields.map(field => {
        const id = `test-${field.name}`
        const used = values[field.name]?.length ?? 0
        const preview = previews[field.name]
        return (
          <div key={field.name} className="grid gap-2">
            <Label htmlFor={id}>{title(field)}</Label>
            {field.format === "JPEG" ? (
              <div className="flex items-center gap-3">
                {/* eslint-disable-next-line @next/next/no-img-element -- local data URL; nothing to optimize. */}
                {preview && <img src={preview.url} alt={preview.name} className="size-16 shrink-0 rounded-md border border-border object-cover" />}
                <input id={id} type="file" accept="image/jpeg,.jpg,.jpeg" aria-invalid={!!problems[field.name]} aria-describedby={`${id}-note`}
                  className="min-w-0 flex-1 text-sm file:mr-3 file:rounded-md file:border file:border-border file:bg-transparent file:px-3 file:py-1.5 file:text-sm file:font-medium hover:file:bg-muted"
                  onChange={event => { void pick(field, event.currentTarget.files?.[0]); event.currentTarget.value = "" }} />
              </div>
            ) : (
              <Textarea id={id} rows={4} aria-invalid={!!problems[field.name]} aria-describedby={`${id}-note`} value={decoder.decode(values[field.name] ?? new Uint8Array())} onChange={event => type(field, event.target.value)} />
            )}
            <p id={`${id}-note`} className={`text-xs tabular-nums ${problems[field.name] ? "text-fail" : "text-muted-foreground"}`}>
              {problems[field.name] || (field.format === "JPEG" ? `JPEG, 최대 ${formatBytes(field.maxBytes)}${preview ? `, ${formatBytes(used)}` : ""}` : `${formatBytes(used)} / ${formatBytes(field.maxBytes)}`)}
            </p>
          </div>
        )
      })}
      <InputBlock label="응답 대기 시간" value={timeout} onChange={event => setTimeoutText(event.target.value)} error={timeoutProblem || undefined} autoComplete="off" spellCheck={false} />
      <div className="flex justify-end">
        <Button variant="default" loading={mutation.isPending} disabled={!!disabledReason} disabledReason={disabledReason || undefined} onClick={send}>전송</Button>
      </div>
    </FormSection>
  )

  return (
    <div className="grid gap-6 lg:grid-cols-2 lg:items-start">
      {form}
      <Section title="결과" actions={received && !mutation.isPending ? <Button size="sm" variant="outline" onClick={() => setRaw(true)}>원본 보기</Button> : undefined} card>
        <Result mutation={mutation} outputFields={outputFields} record={record} waiting={waiting} generative={!!workload.appliedDefinition?.preprocessor.maxNewTokens} sloMs={workload.appliedDefinition ? workload.appliedDefinition.slo.targetLatencyNanos / 1e6 : undefined} />
      </Section>
      <RawDialog open={raw} onOpenChange={setRaw} value={mutation.data?.data ?? mutation.error?.data ?? null} filename={`${workload.desired.metadata.name}-infer`} />
    </div>
  )
}

type Mutation = UseMutationResult<RelayResponse, ApiError, ChillMutationInput>

function Result({ mutation, outputFields, record, waiting, generative, sloMs }: {
  mutation: Mutation; outputFields: Omit<RawField, "maxBytes">[]; record: RequestRow | undefined; waiting: boolean; generative: boolean; sloMs: number | undefined
}) {
  if (mutation.isPending) return <div className="grid gap-3"><StatusBadge tone="progress" label="기다리는 중" /></div>
  if (mutation.isIdle) return <EmptyState compact title="아직 보내지 않음" />
  const error = mutation.error
  if (error) {
    const code = errorDetails(error)?.code
    return (
      <div className="grid gap-4">
        <div><StatusBadge tone={error.unknownOutcome ? "warn" : "fail"} label={error.unknownOutcome ? "결과 미확인" : "실패"} /></div>
        {!error.unknownOutcome && <Fields items={[{ label: "오류", value: requestError(code), reason: code ? `코드 ${code}` : `HTTP ${error.status}` }]} />}
        <Timing record={record} waiting={waiting} generative={generative} sloMs={sloMs} rtt={error.status ? error.browserRTT : null} />
      </div>
    )
  }
  const outputs: FieldItem[] = rawOutputs(outputFields, mutation.data?.data).map(output => ({
    label: output.name,
    value: output.value === undefined ? null
      : output.format === "JPEG"
        // eslint-disable-next-line @next/next/no-img-element -- response data URL; nothing to optimize.
        ? <img src={`data:image/jpeg;base64,${output.value}`} alt={output.name} className="max-h-72 max-w-full rounded-md border border-border" />
        : <span className="whitespace-pre-wrap break-words">{output.value}</span>,
    reason: "응답에 없음",
  }))
  return (
    <div className="grid gap-5">
      {outputs.some(output => output.value != null) && <Fields items={outputs} />}
      <Timing record={record} waiting={waiting} generative={generative} sloMs={sloMs} rtt={mutation.data?.browserRTT ?? null} />
    </div>
  )
}

function Timing({ record, waiting, generative, sloMs, rtt }: { record: RequestRow | undefined; waiting: boolean; generative: boolean; sloMs: number | undefined; rtt: number | null }) {
  return (
    <div className="grid gap-4">
      <Fields items={[
        { label: "결과", value: record ? <StatusBadge status={requestResult(record)} /> : waiting ? <span className="text-muted-foreground">기록 확인 중</span> : null, reason: "완료 기록 없음" },
        { label: "소요 시간", value: record?.elapsedNanos != null ? requestMs(record.elapsedNanos) : rtt != null ? requestMs(rtt * 1e6) : null, reason: "시간 정보 없음", info: record ? undefined : "완료 기록이 없어 브라우저에서 잰 왕복 시간" },
        { label: "응답 목표", value: sloMs !== undefined ? requestMs(sloMs * 1e6) : null, reason: "적용된 정의 없음" },
      ]} columns={3} />
      {record?.nativeElapsedMS !== undefined && <StageBars row={record} backendLabel={generative ? "답 생성" : undefined} />}
    </div>
  )
}
