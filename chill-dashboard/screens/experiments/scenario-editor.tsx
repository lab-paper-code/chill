"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { Trash2 } from "lucide-react"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { adoptWorkloads, canDeleteLoad, copyAuthorDraft, object, patchSpec, removeLoadWorkload, specJSON, workloadChanges, type EvidenceInput, type LoadScenario, type Spec } from "@/lib/evaluation-author"
import { evaluationCanSubmit, evaluationPreflight, evaluationSameSpec, type EvaluationPreflight } from "@/lib/evaluation-request"
import { loadCheckLabel, scenarioParts, scenarioProblems, scenarioServices } from "@/lib/experiments-scenario"
import type { DatasetResource, WorkloadSummary } from "@/lib/domain"
import { formatBytes } from "@/lib/format"
import { Button, DataTable, EmptyState, FormActions, FormSection, Hint, LeaveGuard, Page, PageHeader, QueryBoundary, Section, StatusBadge, useToast } from "@/components/kit"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { SelectBlock } from "@/components/custom/blocks/SelectBlock"
import { RateChart } from "./scenario-chart"
import { SCENARIOS_PATH, scenarioHref } from "./scenarios"
import { scenarioPath, scenarioQuery } from "./scenario-detail"

const emptySpec: Spec = { workloads: [], parts: [], arrivalDistribution: {}, seeds: {}, datasetRefs: {} }
const arrivalOptions = [{ value: "poisson", label: "포아송" }]
const TRACE_PAGE = 25

export function ScenarioEditorScreen({ name }: { name?: string }) {
  const search = useSearchParams()
  const source = name ?? search.get("copy") ?? ""
  const wantedUid = search.get("uid")
  const query = useChillQuery<LoadScenario>(scenarioPath(source || "_"), { enabled: !!source, decoder: parseSchedulerJSON })
  const title = name ? `${name} 수정` : "새 부하 시나리오"
  if (!source) return <Page><PageHeader title={title} /><Editor key="new" /></Page>
  return (
    <Page>
      <PageHeader title={title} />
      <QueryBoundary query={query} skeleton="block">
        {item => {
          if (name && wantedUid && wantedUid !== item.uid) return <EmptyState title="다른 정의로 교체됨" action={<Link href={scenarioHref(name)} className="text-sm underline">현재 정의 보기</Link>} />
          if (name && !canDeleteLoad(item)) return <EmptyState title="사용 중이라 수정할 수 없음" action={<Link href={scenarioHref(name)} className="text-sm underline">시나리오로 돌아가기</Link>} />
          return <Editor key={item.uid} source={item} editing={!!name} />
        }}
      </QueryBoundary>
    </Page>
  )
}

function Editor({ source, editing = false }: { source?: LoadScenario; editing?: boolean }) {
  const [draft] = React.useState(() => source ? editing ? { name: source.name, spec: structuredClone(source.spec), removedFields: [] as string[] } : copyAuthorDraft(source, "load") : { name: "", spec: emptySpec, removedFields: [] as string[] })
  const [name, setName] = React.useState(draft.name)
  const [spec, setSpec] = React.useState<Spec>(draft.spec)
  const [answer, setAnswer] = React.useState<{ result: EvaluationPreflight; body: string; live: unknown } | null>(null)
  const [unknown, setUnknown] = React.useState(false)
  const [baseline] = React.useState(() => specJSON({ name: draft.name, spec: draft.spec }))
  const router = useRouter(), queryClient = useQueryClient(), mutation = useChillMutation(), { toast } = useToast()
  const detail = useChillQuery<LoadScenario>(scenarioPath(name || "_"), { enabled: false, decoder: parseSchedulerJSON })
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const datasets = useChillQuery<{ items: DatasetResource[] }>("v1/datasets")

  const body = specJSON({ name, spec })
  const dirty = body !== baseline
  const services = scenarioServices(spec), parts = scenarioParts(spec)
  const problems = scenarioProblems(name, spec, editing)
  if (!editing && source && name === source.name) problems.unshift("새 이름 필요")
  const checked = answer && answer.body === body ? answer : null
  const changes = checked ? workloadChanges(spec, checked.live) : []
  const canSave = !problems.length && !mutation.isPending && !unknown && !!checked && evaluationCanSubmit(checked.result, checked.body, body, unknown)

  const edit = (path: (string | number)[], value: unknown) => setSpec(current => patchSpec(current, path, value))
  const addPart = () => setSpec(current => {
    const used = new Set(scenarioParts(current).map(part => part.id))
    let n = used.size + 1
    while (used.has(`part-${n}`)) n++
    const rates = Object.fromEntries(scenarioServices(current).map(service => [service, 0]))
    return patchSpec(current, ["parts", scenarioParts(current).length], { id: `part-${n}`, durationSeconds: 60, rates })
  })
  const removePart = (index: number) => setSpec(current => patchSpec(current, ["parts"], (current.parts as unknown[]).filter((_, i) => i !== index)))
  const addService = (uid: string) => {
    const meta = workloads.data?.find(item => item.desired.metadata.uid === uid)?.desired.metadata
    if (!meta || services.includes(meta.name)) return
    setSpec(current => {
      let next = patchSpec(current, ["workloads", scenarioServices(current).length], { name: meta.name, uid: meta.uid, generation: meta.generation })
      next = patchSpec(next, ["arrivalDistribution", meta.name], "poisson")
      next = patchSpec(next, ["seeds", meta.name], "")
      scenarioParts(next).forEach((_, index) => { next = patchSpec(next, ["parts", index, "rates", meta.name], 0) })
      return next
    })
  }

  async function preflight() {
    try {
      const response = await mutation.mutateAsync({ path: `${SCENARIOS_PATH}/preflight`, method: "POST", body })
      const result = evaluationPreflight(response.data)
      if (!result) throw new Error("점검 응답 형식 오류")
      setAnswer({ result, body, live: object(response.data).liveWorkloads })
    } catch (error) {
      setAnswer(null)
      toast({ title: "사전 점검 실패", description: error instanceof ApiError && error.status === 403 ? "권한 없음" : undefined, type: "error" })
    }
  }

  function done(savedName: string) {
    toast({ title: editing ? "수정됨" : "저장됨", description: savedName, type: "success" })
    void queryClient.invalidateQueries({ predicate: q => String(q.queryKey[1]).startsWith(SCENARIOS_PATH) })
    router.replace(scenarioHref(savedName))
  }

  async function save() {
    if (!canSave) return
    try {
      await mutation.mutateAsync(editing && source
        ? { path: scenarioQuery(source), method: "PUT", body: specJSON({ spec }) }
        : { path: SCENARIOS_PATH, method: "POST", body })
      done(name)
    } catch (error) {
      if (error instanceof ApiError && error.unknownOutcome) { setUnknown(true); toast({ title: "결과 미확인", type: "warning" }); return }
      setAnswer(null)
      toast({ title: "저장 실패", description: error instanceof ApiError && error.status === 409 ? "이미 있거나 사용 중" : error instanceof ApiError && error.status === 403 ? "권한 없음" : undefined, type: "error" })
    }
  }

  async function confirmOutcome() {
    const response = await detail.refetch()
    if (response.data && evaluationSameSpec(response.data.spec, spec)) { done(name); return }
    setUnknown(false)
    setAnswer(null)
    toast({ title: response.error?.status === 404 ? "저장되지 않음" : "저장 여부 확인 실패", type: "warning" })
  }

  return (
    <>
      <LeaveGuard when={dirty && !mutation.isSuccess} />
      <div className="grid min-w-0 items-start gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="grid min-w-0 gap-6">
          <FormSection title="기본">
            <InputBlock label="이름" value={name} disabled={editing || unknown} onChange={event => setName(event.target.value.trim())} placeholder="소문자, 숫자, 하이픈" />
            {draft.removedFields.length > 0 && (
              <Hint text={draft.removedFields.join(", ")} focusable><span className="text-xs text-muted-foreground">복사에서 제외된 항목 {draft.removedFields.length}개</span></Hint>
            )}
          </FormSection>
          <FormSection title="구간" info="길이 초, 요청률 req/s">
            <DataTable
              label="구간" rows={parts.map((part, index) => ({ ...part, index }))} rowKey={row => String(row.index)} pageSize={0}
              empty={<EmptyState compact title="구간 없음" />}
              columns={[
                { key: "id", header: "구간", width: "10rem", cell: row => <InputBlock inputSize="sm" aria-label={`구간 ${row.index + 1} 이름`} value={row.id} onChange={event => edit(["parts", row.index, "id"], event.target.value)} /> },
                { key: "seconds", header: "길이", unit: "초", width: "8rem", numeric: true, cell: row => <NumberInput label={`구간 ${row.index + 1} 길이`} value={row.seconds} onChange={value => edit(["parts", row.index, "durationSeconds"], value)} /> },
                ...services.map((service, i) => ({ key: `r${i}`, header: service, width: "13rem", numeric: true, cell: (row: { index: number; rates: Record<string, number | null> }) => <NumberInput label={`구간 ${row.index + 1} ${service} 요청률`} value={row.rates[service] ?? null} onChange={value => edit(["parts", row.index, "rates", service], value)} /> })),
                { key: "remove", header: "", width: "3rem", cell: row => <Button variant="ghost" size="icon" aria-label={`구간 ${row.index + 1} 삭제`} onClick={() => removePart(row.index)}><Trash2 aria-hidden="true" /></Button> },
              ]}
            />
            <div><Button variant="outline" size="sm" disabled={!services.length} onClick={addPart}>구간 추가</Button></div>
          </FormSection>
          <FormSection title="서비스">
            {services.map(service => (
              <ServiceInputs key={service} spec={spec} service={service} datasets={datasets.data?.items ?? []} onEdit={edit} onRemove={() => setSpec(current => removeLoadWorkload(current, scenarioServices(current).indexOf(service)))} />
            ))}
            <div className="max-w-xs">
              <SelectBlock
                inputSize="sm" label="서비스 추가" placeholder={workloads.isPending ? "불러오는 중" : "서비스 선택"} value="" searchable
                options={(workloads.data ?? []).filter(item => !services.includes(item.desired.metadata.name)).map(item => ({ value: item.desired.metadata.uid, label: item.desired.metadata.name }))}
                onChange={addService}
              />
            </div>
          </FormSection>
        </div>
        <aside className="grid min-w-0 gap-4 lg:sticky lg:top-4">
          <Section title="요청률 미리보기" card>
            {parts.length && services.length ? <RateChart spec={spec} height={180} /> : <EmptyState compact title="구간과 서비스를 추가하세요" />}
          </Section>
          <Section title="사전 점검" card>
            {problems.length > 0 ? (
              <ul className="grid gap-1.5 text-sm">{problems.map(problem => <li key={problem}><StatusBadge tone="warn" label={problem} /></li>)}</ul>
            ) : !checked ? (
              <StatusBadge tone="idle" label={answer ? "변경됨, 다시 점검" : "점검 전"} />
            ) : (
              <ul className="grid gap-2 text-sm">
                {checked.result.checks.map(check => (
                  <li key={check.id} className="flex min-w-0 items-center justify-between gap-2">
                    <Hint text={check.reason}><span className="truncate">{loadCheckLabel(check.id)}</span></Hint>
                    <StatusBadge tone={check.passed ? "ok" : "fail"} label={check.passed ? "통과" : "실패"} />
                  </li>
                ))}
              </ul>
            )}
            {changes.length > 0 && (
              <div className="flex items-center justify-between gap-2 text-sm">
                <span className="truncate">서비스 정의 변경 {changes.length}개</span>
                <Button variant="outline" size="sm" onClick={() => { setSpec(adoptWorkloads(spec, changes, changes.map(change => change.name))); setAnswer(null) }}>현재 값 반영</Button>
              </div>
            )}
          </Section>
        </aside>
      </div>
      <FormActions>
        {unknown && <Button variant="outline" loading={detail.isFetching} onClick={() => void confirmOutcome()}>결과 확인</Button>}
        <Button variant="outline" disabled={!!problems.length || mutation.isPending || unknown} loading={mutation.isPending && !canSave} onClick={() => void preflight()}>사전 점검</Button>
        <Button variant="default" disabled={!canSave} loading={mutation.isPending && canSave} onClick={() => void save()}>저장</Button>
      </FormActions>
    </>
  )
}

function ServiceInputs({ spec, service, datasets, onEdit, onRemove }: {
  spec: Spec; service: string; datasets: DatasetResource[]
  onEdit: (path: (string | number)[], value: unknown) => void; onRemove: () => void
}) {
  const arrival = String(object(spec.arrivalDistribution)[service] ?? ""), seed = String(object(spec.seeds)[service] ?? "")
  const dataset = object(object(spec.datasetRefs)[service]), trace = object(object(spec.traceRefs)[service])
  const arrivals = arrivalOptions.some(option => option.value === arrival) || !arrival ? arrivalOptions : [...arrivalOptions, { value: arrival, label: arrival }]
  const datasetOptions = datasets.map(item => ({ value: item.metadata.uid, label: item.metadata.name }))
  if (dataset.uid && !datasetOptions.some(option => option.value === dataset.uid)) datasetOptions.push({ value: String(dataset.uid), label: String(dataset.name) })
  return (
    <div className="grid min-w-0 gap-3 rounded-lg border border-border p-4">
      <div className="flex items-center justify-between gap-2">
        <h3 className="min-w-0 truncate text-sm font-semibold">{service}</h3>
        <Button variant="ghost" size="sm" iconLeft={<Trash2 aria-hidden="true" />} onClick={onRemove}>서비스 제거</Button>
      </div>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <SelectBlock inputSize="sm" label="도착 분포" options={arrivals} value={arrival} onChange={value => onEdit(["arrivalDistribution", service], value)} />
        <InputBlock inputSize="sm" label="난수 씨앗" inputMode="numeric" value={seed} onChange={event => onEdit(["seeds", service], event.target.value.trim())} />
        <SelectBlock
          inputSize="sm" label="데이터셋" placeholder="선택" searchable options={datasetOptions} value={String(dataset.uid ?? "")}
          onChange={uid => { const item = datasets.find(entry => entry.metadata.uid === uid); onEdit(["datasetRefs", service], item ? { name: item.metadata.name, uid: item.metadata.uid } : String(dataset.uid) === uid ? dataset : undefined) }}
        />
        <TracePicker value={trace} onChange={value => onEdit(["traceRefs", service], value)} />
      </div>
    </div>
  )
}

function TracePicker({ value, onChange }: { value: Spec; onChange: (value: Spec | undefined) => void }) {
  const [offset, setOffset] = React.useState(0)
  const query = useChillQuery<{ items: EvidenceInput[]; page: { total: number } }>(`v1/evaluations/inputs?${new URLSearchParams({ kind: "trace", limit: String(TRACE_PAGE), offset: String(offset) })}`, { decoder: parseSchedulerJSON })
  const identity = (item: { store?: unknown; key?: unknown }) => JSON.stringify([item.store, item.key])
  const items = query.data?.items ?? [], total = query.data?.page.total ?? 0
  const current = value.key ? identity(value) : ""
  const options = items.map(item => ({ value: identity(item), label: item.name || item.key, group: item.set || undefined }))
  if (current && !options.some(option => option.value === current)) options.unshift({ value: current, label: String(value.key), group: undefined })
  return (
    <div className="grid min-w-0 gap-1.5">
      <SelectBlock
        inputSize="sm" label="기록 입력" searchable options={[{ value: "", label: "사용 안 함" }, ...options]} value={current}
        placeholder={query.isPending ? "불러오는 중" : items.length ? "선택" : "입력 없음"}
        onChange={key => { const item = items.find(entry => identity(entry) === key); onChange(item ? { store: item.store, key: item.key, size: item.size } : key === current ? value : undefined) }}
      />
      {(value.size != null || total > TRACE_PAGE) && (
        <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
          <span className="tabular-nums">{value.size != null ? formatBytes(Number(value.size)) : ""}</span>
          {total > TRACE_PAGE && (
            <span className="flex gap-1">
              <Button variant="ghost" size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - TRACE_PAGE))}>이전</Button>
              <Button variant="ghost" size="sm" disabled={offset + TRACE_PAGE >= total} onClick={() => setOffset(offset + TRACE_PAGE)}>다음</Button>
            </span>
          )}
        </div>
      )}
    </div>
  )
}

/** Number field with its own text, so "1." and "" can be typed; the spec only receives finite numbers (or nothing). */
function NumberInput({ label, value, onChange }: { label: string; value: number | null; onChange: (value: number | undefined) => void }) {
  const [text, setText] = React.useState(value == null ? "" : String(value))
  const parsed = text.trim() === "" ? undefined : Number(text)
  const typed = parsed != null && Number.isFinite(parsed) ? parsed : undefined
  if (typed !== (value ?? undefined)) setText(value == null ? "" : String(value))
  return (
    <div className="ml-auto w-24">
      <InputBlock
        type="number" inputSize="sm" aria-label={label} min={0} step="any" value={text}
        onChange={event => {
          const next = event.target.value
          setText(next)
          const number = next.trim() === "" ? NaN : Number(next)
          onChange(Number.isFinite(number) ? number : undefined)
        }}
      />
    </div>
  )
}
