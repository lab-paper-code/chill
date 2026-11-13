"use client"

import * as React from "react"
import { FileUp } from "lucide-react"
import { Button, Fields, FormActions, FormSection, LeaveGuard, Page, PageHeader, RawDialog, StatusBadge } from "@/components/kit"
import { Textarea } from "@/components/ui/textarea"
import type { ModelList } from "@/lib/domain"
import type { MeasurementPlanSpec } from "@/lib/read-types"
import { useChillQuery } from "@/lib/client"
import { executionPathLabel, powerModeLabel } from "@/lib/display-labels"
import { formatSeconds } from "@/lib/format"
import { parseMeasurementDocument, type MeasurementRequestDocument } from "@/lib/measurement-document"
import { requestHref } from "@/lib/measurement-request"
import { useMeasurementPlans } from "@/hooks/use-measurement-data"
import { useMeasurementDocumentSubmission } from "@/hooks/use-measurement-submission"
import { PreflightSection, UnknownOutcome, useSubmitToast } from "./request-shared"

type Props = { namespace: string; bar: React.ReactNode; onDirty: (dirty: boolean) => void }

export function DocumentRequestForm({ namespace, bar, onDirty }: Props) {
  const plans = useMeasurementPlans(namespace)
  const models = useChillQuery<ModelList>("v1/models")
  const [text, setText] = React.useState("")
  const [fileName, setFileName] = React.useState("")
  const [body, setBody] = React.useState<MeasurementRequestDocument | null>(null)
  const [parseError, setParseError] = React.useState("")
  const [raw, setRaw] = React.useState(false)
  const file = React.useRef<HTMLInputElement>(null)
  const textId = React.useId(), errorId = React.useId()
  const submission = useMeasurementDocumentSubmission({ body, namespace, plans: plans.data?.items ?? [], refetchPlans: plans.refetch })
  const locked = submission.unknown || submission.pending || submission.send.isPending || submission.send.isSuccess
  useSubmitToast(submission.send)
  const dirty = !!text.trim() && !submission.send.isSuccess
  React.useEffect(() => { onDirty(dirty); return () => onDirty(false) }, [dirty, onDirty])

  function edit(next: string) {
    if (locked) return
    setText(next); setBody(null); setParseError(""); submission.invalidate()
  }
  function review() {
    try { setBody(parseMeasurementDocument(text, namespace)); setParseError("") }
    catch (error) { setBody(null); setParseError(error instanceof Error ? error.message : "문서 해석 실패") }
  }
  async function load(files: FileList | null) {
    const picked = files?.[0]
    if (!picked) return
    setFileName(picked.name)
    edit(await picked.text())
  }

  const spec = body?.spec as Partial<MeasurementPlanSpec> | undefined
  const candidate = spec?.state?.candidate
  const exists = !!body && (plans.data?.items ?? []).some(plan => plan.metadata.name === body.metadata.name)
  const lookupHref = submission.found ? requestHref(namespace, submission.found.metadata.name, submission.found.metadata.uid) : undefined
  const unit = (value: number | undefined, suffix: string) => value == null ? null : `${value}${suffix}`

  return (
    <Page>
      <PageHeader title="새 측정 요청" more={body ? [{ label: "원본 보기", onSelect: () => setRaw(true) }] : undefined} />
      {bar}
      <LeaveGuard when={dirty} />
      <FormSection title="문서">
        <div className="flex flex-wrap items-center gap-2">
          <input ref={file} type="file" accept=".yaml,.yml,.json,application/json,application/yaml" className="sr-only" tabIndex={-1} aria-hidden="true"
            onChange={event => { void load(event.target.files); event.target.value = "" }} />
          <Button size="sm" variant="outline" iconLeft={<FileUp />} disabled={locked} onClick={() => file.current?.click()}>파일 선택</Button>
          {fileName && <span className="truncate text-sm text-muted-foreground">{fileName}</span>}
        </div>
        <div className="grid gap-1.5">
          <label htmlFor={textId} className="text-sm font-medium">YAML 또는 JSON</label>
          <Textarea id={textId} value={text} disabled={locked} spellCheck={false} aria-invalid={!!parseError || undefined} aria-describedby={parseError ? errorId : undefined}
            className="h-64 resize-y font-mono text-xs field-sizing-fixed" onChange={event => edit(event.target.value)} />
          {parseError && <p id={errorId} className="text-xs font-medium text-fail">{parseError}</p>}
        </div>
        <div className="flex">
          <Button size="sm" variant="outline" disabled={!text.trim() || locked} onClick={review}>문서 검토</Button>
        </div>
      </FormSection>

      {body && <FormSection title="요청">
        <Fields columns={3} items={[
          { label: "요청 이름", value: exists ? <span className="inline-flex items-center gap-2">{body.metadata.name}<StatusBadge tone="warn" label="이미 있음" /></span> : body.metadata.name },
          { label: "서비스", value: spec?.activeProtocol?.workload || null, reason: "문서에 없음" },
          { label: "모델", value: candidate?.modelSpec, reason: "문서에 없음" },
          { label: "장치 종류", value: candidate?.deviceClass, reason: "문서에 없음" },
          { label: "장치", value: spec?.state?.targetNode, reason: "문서에 없음" },
          { label: "실행 경로", value: candidate?.executionPath ? executionPathLabel(candidate.executionPath, models.data?.items.find(item => item.metadata.name === candidate.modelSpec)) : null, reason: "문서에 없음" },
          { label: "전력 모드", value: candidate?.powerMode && powerModeLabel(candidate.powerMode), reason: "문서에 없음" },
          { label: "배치별 반복", value: unit(spec?.trialsPerBatch, "회"), reason: "문서에 없음" },
          { label: "실행 측정", value: spec?.activeProtocol?.durationSeconds == null ? null : formatSeconds(spec.activeProtocol.durationSeconds), reason: "문서에 없음" },
        ]} />
      </FormSection>}

      {body && (submission.unknown
        ? <UnknownOutcome name={body.metadata.name} onLookup={submission.lookup} looking={plans.isFetching} checked={submission.checked} href={lookupHref} />
        : <PreflightSection submission={submission} />)}

      <FormActions>
        <Button variant="default" loading={submission.send.isPending} disabled={!body || locked} onClick={submission.submit}>측정 요청</Button>
      </FormActions>
      {body && <RawDialog open={raw} onOpenChange={setRaw} value={body} filename={`${body.metadata.name}.json`} />}
    </Page>
  )
}
