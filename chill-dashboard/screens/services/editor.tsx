"use client"

import { useState, type FormEvent } from "react"
import { useRouter } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { Button, ConfirmDialog, DataTable, Fields, FormActions, FormSection, LeaveGuard, Page, PageHeader, QueryBoundary, Section, Select, useToast, type Change } from "@/components/kit"
import { Alert } from "@/components/custom/blocks/Alerts"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Label } from "@/components/ui/label"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import { admissionPolicy, errorCodeLabel } from "@/lib/status-labels"
import { workloadSpec, type DatasetResource, type ModelResource, type PreprocessorResource, type WorkloadSpec, type WorkloadView } from "@/lib/domain"
import { datasetGap } from "@/lib/measurement-request"

type Draft = { name: string; model: string; preprocessor: string; dataset: string; percentile: string; latency: string; policy: "Reject" | "Delay"; maxQueued: string; lifetime: string }
type Field = keyof Draft
type Errors = Partial<Record<Field, string>>
type Catalog = { models: ModelResource[]; preprocessors: PreprocessorResource[]; datasets: DatasetResource[] }

const NONE = "~"
const emptyDraft: Draft = { name: "", model: "", preprocessor: "", dataset: "", percentile: "99.5", latency: "100ms", policy: "Reject", maxQueued: "1024", lifetime: "" }
// Go duration syntax, same pattern workloadSpec() checks.
const durationPattern = /^[+-]?(?:0|(?:(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))+)$/
// Server code → the field it names.
const codeField: Record<string, Field> = { AlreadyExists: "name", InvalidModelReference: "model", InvalidPreprocessorReference: "preprocessor", InvalidDatasetReference: "dataset", IncompatibleDataset: "dataset", InvalidRequestTimeout: "lifetime" }

const toDraft = (name: string, spec: WorkloadSpec): Draft => ({
  name, model: spec.modelRef.name, preprocessor: spec.preprocessorRef.name, dataset: spec.datasetRef?.name ?? "",
  percentile: String(spec.slo.percentile), latency: spec.slo.targetLatency, policy: spec.admission.policy,
  maxQueued: String(spec.admission.maxQueuedRequests), lifetime: spec.requestLifetime ?? "",
})
// A model path runs only when the preprocessor targets one of the model's artifacts that has an execution path.
const preprocessorFits = (pre: PreprocessorResource, model: ModelResource | undefined) => {
  if (!model) return false
  const targets = pre.spec.artifacts.filter(a => a.target.model === model.metadata.name && model.spec.artifacts.some(m => m.name === a.target.artifact)).map(a => a.target.artifact)
  return model.spec.executionPaths.some(path => targets.includes(path.artifact))
}
const datasetFits = (data: DatasetResource, pre: PreprocessorResource | undefined) => !!pre && !datasetGap(data.spec, pre.spec)

function problems(draft: Draft, creating: boolean): Errors {
  const errors: Errors = {}
  if (creating && !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(draft.name)) errors.name = "소문자, 숫자, -만 가능"
  if (!draft.model) errors.model = "선택 필요"
  if (!draft.preprocessor) errors.preprocessor = "선택 필요"
  const p = Number(draft.percentile)
  if (!draft.percentile.trim() || !Number.isFinite(p) || p <= 0 || p > 100) errors.percentile = "0 초과 100 이하"
  if (!durationPattern.test(draft.latency.trim())) errors.latency = "시간과 단위 (예: 100ms)"
  const q = Number(draft.maxQueued)
  if (!Number.isInteger(q) || q < 1) errors.maxQueued = "1 이상의 정수"
  if (draft.lifetime && !durationPattern.test(draft.lifetime.trim())) errors.lifetime = "시간과 단위 (예: 2s)"
  return errors
}
const specOf = (d: Draft) => workloadSpec(d.model, d.preprocessor, d.dataset, d.percentile.trim(), d.latency.trim(), { policy: d.policy, maxQueuedRequests: Number(d.maxQueued) }, d.lifetime.trim())

const rows: { field: Field; label: string; show: (d: Draft) => string }[] = [
  { field: "name", label: "이름", show: d => d.name },
  { field: "model", label: "모델", show: d => d.model },
  { field: "preprocessor", label: "전처리기", show: d => d.preprocessor },
  { field: "dataset", label: "데이터셋", show: d => d.dataset || "없음" },
  { field: "percentile", label: "분위수 (%)", show: d => d.percentile },
  { field: "latency", label: "목표 지연", show: d => d.latency },
  { field: "policy", label: "과부하 정책", show: d => admissionPolicy(d.policy) },
  { field: "maxQueued", label: "대기 상한 (건)", show: d => d.maxQueued },
  { field: "lifetime", label: "요청 최종 기한", show: d => d.lifetime || "기본" },
]

export function ServiceEditorScreen({ name }: { name?: string }) {
  const models = useChillQuery<{ items: ModelResource[] }>("v1/models")
  const preprocessors = useChillQuery<{ items: PreprocessorResource[] }>("v1/preprocessors")
  const datasets = useChillQuery<{ items: DatasetResource[] }>("v1/datasets")
  const workload = useChillQuery<WorkloadView>(`v1/workloads/${encodeURIComponent(name ?? "")}`, { enabled: !!name })
  const parts = [models, preprocessors, datasets, ...(name ? [workload] : [])]
  const combined = {
    data: parts.every(q => q.data) ? { models: models.data!.items, preprocessors: preprocessors.data!.items, datasets: datasets.data!.items } : undefined,
    error: parts.find(q => q.error)?.error ?? null,
    isPending: parts.some(q => q.isPending),
    refetch: () => parts.forEach(q => q.refetch()),
  }
  const base = workload.data
  return (
    <Page>
      <PageHeader title={name ? `${name} 수정` : "서비스 등록"} />
      <QueryBoundary query={combined} skeleton="block">
        {catalog => <Editor key={name ?? "new"} name={name} catalog={catalog} base={base} reload={async () => (await workload.refetch()).data} />}
      </QueryBoundary>
    </Page>
  )
}

function Editor({ name, catalog, base, reload }: { name?: string; catalog: Catalog; base: WorkloadView | undefined; reload: () => Promise<WorkloadView | undefined> }) {
  const router = useRouter()
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const mutation = useChillMutation()
  const creating = !name
  const [original, setOriginal] = useState<WorkloadView | undefined>(base)
  const initial = original ? toDraft(original.desired.metadata.name, original.desired.spec) : emptyDraft
  const [saved, setSaved] = useState(initial)
  const [draft, setDraft] = useState(initial)
  const [errors, setErrors] = useState<Errors>({})
  const [review, setReview] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const [latest, setLatest] = useState<WorkloadView | null>(null)

  const model = catalog.models.find(m => m.metadata.name === draft.model)
  const pre = catalog.preprocessors.find(p => p.metadata.name === draft.preprocessor)
  const changed = rows.filter(r => r.show(draft) !== r.show(saved))
  const dirty = changed.length

  function set(patch: Partial<Draft>) {
    setDraft(current => {
      const next = { ...current, ...patch }
      // A model change drops a preprocessor that no longer fits it, and that drops a dataset the preprocessor no longer covers.
      const nextModel = catalog.models.find(m => m.metadata.name === next.model)
      let nextPre = catalog.preprocessors.find(p => p.metadata.name === next.preprocessor)
      if ("model" in patch && nextPre && !(next.preprocessor === saved.preprocessor && next.model === saved.model) && !preprocessorFits(nextPre, nextModel)) { next.preprocessor = ""; nextPre = undefined }
      const data = catalog.datasets.find(d => d.metadata.name === next.dataset)
      if (("model" in patch || "preprocessor" in patch) && data && !(next.preprocessor === saved.preprocessor && next.dataset === saved.dataset) && !datasetFits(data, nextPre)) next.dataset = ""
      return next
    })
    setErrors(current => { const next = { ...current }; for (const key of Object.keys(patch) as Field[]) delete next[key]; return next })
  }

  function start(event: FormEvent) {
    event.preventDefault()
    const found = problems(draft, creating)
    setErrors(found)
    if (Object.keys(found).length) { document.querySelector<HTMLElement>("[aria-invalid=true]")?.focus(); return }
    setFailure(null)
    setReview(true)
  }

  async function submit() {
    try {
      const spec = specOf(draft)
      const response = await mutation.mutateAsync(creating
        ? { path: "v1/workloads", method: "POST", body: { name: draft.name, spec } }
        : { path: `v1/workloads/${encodeURIComponent(name)}`, method: "PUT", body: { spec }, version: original?.desired.metadata.resourceVersion })
      await Promise.all(["v1/workloads?view=summary", `v1/workloads/${encodeURIComponent(draft.name)}`].map(key => queryClient.invalidateQueries({ queryKey: ["chill", key] })))
      toast({ title: creating ? "서비스 등록됨" : "서비스 수정됨", type: "success" })
      const uid = response.data && typeof response.data === "object" ? (response.data as WorkloadView).desired?.metadata.uid : undefined
      setSaved(draft)
      setReview(false)
      router.push(`/services/${encodeURIComponent(draft.name)}${uid ? `?uid=${encodeURIComponent(uid)}` : ""}`)
    } catch (error) {
      setReview(false)
      if (!(error instanceof ApiError)) { setFailure(error instanceof Error ? error.message : "저장 실패"); return }
      const code = (error.data as { code?: string } | null)?.code ?? ""
      if (error.unknownOutcome) { setFailure("결과 미확인"); toast({ title: "결과 미확인", description: "목록에서 등록 여부 확인 필요", type: "warning" }); return }
      if (!creating && error.status === 409 && (code === "Conflict" || code === "ResourceVersionConflict")) {
        const fresh = await reload()
        if (fresh) setLatest(fresh)
        toast({ title: "다른 변경이 먼저 저장됨", type: "error" })
        return
      }
      const field = codeField[code]
      if (field) setErrors({ [field]: errorCodeLabel(code) })
      else setFailure(errorCodeLabel(code))
      toast({ title: creating ? "등록 실패" : "수정 실패", type: "error" })
    }
  }

  const changes: Change[] = changed.map(r => ({ label: r.label, before: r.show(saved), after: r.show(draft) }))
  const modelOptions = catalog.models.map(m => ({ label: m.metadata.name, value: m.metadata.name }))
  const preOptions = catalog.preprocessors.filter(p => p.metadata.name === saved.preprocessor && draft.model === saved.model || preprocessorFits(p, model)).map(p => ({ label: p.metadata.name, value: p.metadata.name }))
  const dataOptions = [{ label: "없음", value: NONE }, ...catalog.datasets.filter(d => d.metadata.name === saved.dataset && draft.preprocessor === saved.preprocessor || datasetFits(d, pre)).map(d => ({ label: d.metadata.name, value: d.metadata.name }))]
  const text = (field: Field, label: string, extra: { type?: "number"; helperText?: string; disabled?: boolean } = {}) => (
    <InputBlock label={label} value={draft[field]} error={errors[field]} aria-invalid={!!errors[field]} disabled={extra.disabled} type={extra.type} helperText={extra.helperText} onChange={event => set({ [field]: event.target.value })} />
  )
  const lastValues = latest ? toDraft(latest.desired.metadata.name, latest.desired.spec) : undefined

  return (
    <>
      <form onSubmit={start} noValidate className="grid gap-6">
        <LeaveGuard when={dirty > 0 && !mutation.isSuccess} />
        {failure && <Alert type="danger" message={failure} />}
        {latest && lastValues && (
          <Section title="다른 변경이 먼저 저장됨" card actions={<>
            <Button type="button" variant="outline" size="sm" onClick={() => {
              // Keep only my edits on top of the latest values, so the other change is not silently reverted.
              setDraft(Object.fromEntries(Object.keys(lastValues).map(key => [key, draft[key as Field] !== saved[key as Field] ? draft[key as Field] : lastValues[key as Field]])) as Draft)
              setOriginal(latest); setSaved(lastValues); setLatest(null)
            }}>최신 값을 기준으로 계속</Button>
            <Button type="button" variant="outline" size="sm" onClick={() => { setDraft(lastValues); setSaved(lastValues); setOriginal(latest); setLatest(null) }}>최신 값으로 바꾸기</Button>
          </>}>
            <DataTable label="충돌한 항목" pageSize={0} rowKey={r => r.field}
              rows={rows.filter(r => r.show(draft) !== r.show(lastValues) || r.show(saved) !== r.show(lastValues))}
              columns={[
                { key: "label", header: "항목", cell: r => r.label },
                { key: "draft", header: "내 입력", cell: r => r.show(draft) },
                { key: "latest", header: "최신", cell: r => r.show(lastValues) },
              ]} />
          </Section>
        )}
        <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
          <div className="grid gap-4">
            <FormSection title="이름">{text("name", "서비스 이름", { disabled: !creating })}</FormSection>
            <FormSection title="모델">
              <Select label="모델" searchable placeholder="선택" options={modelOptions} value={draft.model} error={errors.model} onChange={value => set({ model: value })} />
              <Select label="전처리기" placeholder={draft.model ? "선택" : "모델을 먼저 선택"} disabled={!draft.model} options={preOptions} value={draft.preprocessor} error={errors.preprocessor} onChange={value => set({ preprocessor: value })} />
              <Select label="데이터셋" placeholder={draft.preprocessor ? "선택" : "전처리기를 먼저 선택"} disabled={!draft.preprocessor} options={dataOptions}
                value={draft.dataset || (draft.preprocessor ? NONE : "")} error={errors.dataset} onChange={value => set({ dataset: value === NONE ? "" : value })} />
            </FormSection>
            <FormSection title="응답 목표" info="분위수 비율의 요청이 목표 지연 안에 끝나야 함">
              <div className="grid gap-4 sm:grid-cols-2">
                {text("percentile", "분위수 (%)", { type: "number" })}
                {text("latency", "목표 지연", { helperText: "예: 100ms" })}
              </div>
            </FormSection>
            <FormSection title="과부하 정책">
              <div className="grid gap-2">
                <Label id="policy-label">정책</Label>
                <RadioGroup aria-labelledby="policy-label" value={draft.policy} onValueChange={value => set({ policy: value as Draft["policy"] })} className="flex gap-6">
                  {(["Reject", "Delay"] as const).map(value => (
                    <Label key={value} className="gap-2 font-normal"><RadioGroupItem value={value} />{admissionPolicy(value)}</Label>
                  ))}
                </RadioGroup>
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                {text("maxQueued", "대기 상한 (건)", { type: "number" })}
                {text("lifetime", "요청 최종 기한", { helperText: "비우면 기본값 (예: 2s)" })}
              </div>
            </FormSection>
          </div>
          <aside className="lg:sticky lg:top-20">
            <Section title="입력 요약" card>
              <Fields columns={1} items={rows.filter(r => creating || r.field !== "name").map(r => ({ label: r.label, value: r.show(draft) }))} />
            </Section>
          </aside>
        </div>
        <FormActions changes={creating ? undefined : dirty}>
          <Button type="button" variant="outline" onClick={() => router.back()}>취소</Button>
          <Button type="submit" variant="default" disabled={!creating && dirty === 0}>검토</Button>
        </FormActions>
      </form>
      <ConfirmDialog open={review} onOpenChange={setReview} title={creating ? "서비스 등록" : "서비스 수정"} target={draft.name} changes={creating ? undefined : changes}
        confirmLabel={creating ? "등록" : "저장"} pending={mutation.isPending} onConfirm={() => void submit()}>
        {creating && <Fields items={rows.filter(r => r.field !== "name").map(r => ({ label: r.label, value: r.show(draft) }))} />}
      </ConfirmDialog>
    </>
  )
}
