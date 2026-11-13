"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { Button, ConfirmDialog, DataTable, Dialog, EmptyState, Fields, FilterBar, Hint, LeaveGuard, Page, PageHeader, PageTabs, QueryBoundary, Section, StatusBadge, StatusDot, TimeText, buttonClassName, useToast, type BoundaryQuery } from "@/components/kit"
import { Textarea } from "@/components/ui/textarea"
import { useChillMutation, useChillQuery, useSession } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import { explain } from "@/lib/labels"
import { formatDurationNanos } from "@/lib/format"
import { evaluationPhase, experimentVerdict } from "@/lib/status-labels"
import { parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import type { InventoryView, SchedulerSettings } from "@/lib/read-types"
import type { WorkloadSummary } from "@/lib/domain"
import { decodeExperimentPage, type ExperimentPage } from "@/lib/experiments"
import { copyAuthorDraft, evaluationReplacement, object, parseSpec, patchSpec, records, specJSON, type LoadScenario, type Spec } from "@/lib/evaluation-author"
import { evaluationCanSubmit, evaluationPreflight, evaluationSameSpec, type EvaluationPreflight } from "@/lib/evaluation-request"
import { checkLabel, checkReason, runNameError, scenarioSeconds, specChanges } from "@/lib/experiments-run-editor"
import { serviceNames } from "@/lib/experiments-run"
import { InvalidContext } from "./run-editor-settings"
import { BasicSection, ComparisonsSection, CriteriaSection, StagesSection, type EditorContext, type Path } from "./run-editor-sections"

type Run = { name: string; uid: string; phase: string; spec: Spec; publicationID?: string; startedAt?: string; finishedAt?: string }
type Template = { spec: Spec; sources?: Record<string, string>; sourcePublication?: { name: string; uid: string } }
type Publication = { definition?: { run?: { name?: string }; spec?: Spec } }
type Source = { spec: Spec; name: string; origin?: string; existing?: { name: string; uid: string } }

const decode = (body: string) => parseSchedulerJSON(body)
const runHref = (name: string, uid?: string) => `/experiments/runs/${encodeURIComponent(name)}${uid ? `?uid=${encodeURIComponent(uid)}` : ""}`
const sourceLabels: Record<string, string> = { question: "질문", target: "대상 구성요소", harness: "실행기", loadGenerator: "부하 발생 장치", powerMeasurement: "전력 계측", settingsPatch: "공통 설정 변경", judgment: "판정 기준", evidenceStore: "증거 저장소", stages: "단계", comparisons: "비교" }

export function ExperimentEditorScreen({ name }: { name?: string }) {
  const params = useSearchParams()
  if (name) return <EditRun name={name} uid={params.get("uid") ?? ""} />
  const copy = params.get("copy"), publication = params.get("publication")
  if (copy) return <CopyRun name={copy} />
  if (publication) return <CopyPublication id={publication} />
  if (params.get("from") === "template") return <FromTemplate />
  return <StartChoice />
}

/** Loads the editor's source; the header (with 원문 편집) belongs to the editor once loaded. */
function Loading<T>({ title, query, children }: { title: string; query: BoundaryQuery<T>; children: (data: T) => React.ReactNode }) {
  if (query.data !== undefined) return <>{children(query.data)}</>
  return (
    <Page>
      <PageHeader title={title} />
      <QueryBoundary query={query} skeleton="block">{() => null}</QueryBoundary>
    </Page>
  )
}

function EditRun({ name, uid }: { name: string; uid: string }) {
  const run = useChillQuery<Run>(`v1/evaluations/${encodeURIComponent(name)}`, { decoder: decode })
  const title = `${name} 수정`
  return (
    <Loading title={title} query={run}>
      {data => data.phase !== "Pending" || (uid && data.uid !== uid) ? (
        <Page>
          <PageHeader title={title} status={evaluationPhase(data.phase)} />
          <EmptyState title={data.phase !== "Pending" ? "시작 전 실험만 수정 가능" : "다른 실험으로 교체됨"}
            action={<Link href={runHref(data.name, data.uid)} className={buttonClassName({ variant: "outline", size: "sm" })}>실행 상세</Link>} />
        </Page>
      ) : <Editor key={data.uid} title={title} source={{ spec: data.spec, name: data.name, existing: { name: data.name, uid: data.uid } }} />}
    </Loading>
  )
}

function CopyRun({ name }: { name: string }) {
  const run = useChillQuery<Run>(`v1/evaluations/${encodeURIComponent(name)}`, { decoder: decode })
  return (
    <Loading title="새 실험" query={run}>
      {data => <Editor key={`copy:${data.uid}`} title="새 실험" source={{ spec: copyAuthorDraft({ spec: data.spec }, "evaluation").spec, name: "", origin: data.name }} />}
    </Loading>
  )
}

function CopyPublication({ id }: { id: string }) {
  const result = useChillQuery<Publication>(`v1/experiments/${encodeURIComponent(id)}`, { decoder: decode })
  return (
    <Loading title="새 실험" query={result}>
      {data => data.definition?.spec ? (
        <Editor key={`publication:${id}`} title="새 실험" source={{ spec: copyAuthorDraft({ spec: data.definition.spec }, "evaluation").spec, name: "", origin: data.definition.run?.name }} />
      ) : (
        <Page>
          <PageHeader title="새 실험" />
          <EmptyState title="정의가 기록되지 않은 결과" action={<Link href="/experiments/runs/new?start=publication" className={buttonClassName({ variant: "outline", size: "sm" })}>다른 결과 선택</Link>} />
        </Page>
      )}
    </Loading>
  )
}

function FromTemplate() {
  const template = useChillQuery<Template>("v1/evaluations/template", { decoder: decode })
  return (
    <Loading title="새 실험" query={template}>
      {data => <Editor key="template" title="새 실험" source={{ spec: data.spec, name: "" }} missing={Object.entries(data.sources ?? {}).filter(([, source]) => source === "none").map(([field]) => sourceLabels[field] ?? field)} />}
    </Loading>
  )
}

function StartChoice() {
  const params = useSearchParams()
  const start = params.get("start") ?? "template"
  return (
    <Page>
      <PageHeader title="새 실험" />
      <PageTabs param="start" defaultValue="template" label="시작점" tabs={[{ value: "template", label: "빈 양식" }, { value: "run", label: "기존 실행 복사" }, { value: "publication", label: "게시 결과 복사" }]} />
      {start === "run" ? <RunPicker /> : start === "publication" ? <PublicationPicker /> : <TemplateStart />}
    </Page>
  )
}

function TemplateStart() {
  const template = useChillQuery<Template>("v1/evaluations/template", { decoder: decode })
  return (
    <QueryBoundary query={template} skeleton="block">
      {data => {
        const missing = Object.entries(data.sources ?? {}).filter(([, source]) => source === "none").map(([field]) => sourceLabels[field] ?? field)
        return (
          <Section card title="빈 양식" actions={<Link href="/experiments/runs/new?from=template" className={buttonClassName({ variant: "default", size: "sm" })}>이 양식으로 작성</Link>}>
            <Fields columns={3} items={[
              { label: "기준 결과", value: data.sourcePublication?.name || null, reason: "최근 게시 없음" },
              { label: "단계", value: `${records(data.spec.stages).length}개` },
              { label: "입력 필요", value: missing.length ? missing.join(", ") : "없음" },
            ]} />
          </Section>
        )
      }}
    </QueryBoundary>
  )
}

/** One query value kept in the URL (replace, no scroll). */
function useParam(key: string): [string, (value: string) => void] {
  const params = useSearchParams(), router = useRouter()
  const set = (value: string) => {
    const next = new URLSearchParams(params.toString())
    if (value) next.set(key, value)
    else next.delete(key)
    router.replace(`?${next}`, { scroll: false })
  }
  return [params.get(key) ?? "", set]
}

function RunPicker() {
  const runs = useChillQuery<{ items: Run[] }>("v1/evaluations", { decoder: decode })
  const [query, setQuery] = useParam("q")
  // The URL updates asynchronously; typing stays on local state so no keystroke is lost.
  const [search, setSearch] = React.useState(query)
  return (
    <QueryBoundary query={runs} isEmpty={data => data.items.length === 0} empty={<EmptyState title="실행 없음" />}>
      {data => (
        <DataTable label="복사할 실행" pageSize={10} rowKey={run => run.uid} rowHref={run => `/experiments/runs/new?copy=${encodeURIComponent(run.name)}`}
          rows={data.items.filter(run => run.name.includes(search.trim()))} defaultSort={{ key: "started", desc: true }}
          toolbar={<FilterBar search={{ value: search, onChange: value => { setSearch(value); setQuery(value) }, placeholder: "이름 검색" }} />}
          columns={[
            { key: "name", header: "이름", width: "40%", cell: run => run.name, sortValue: run => run.name },
            { key: "phase", header: "상태", cell: run => <StatusDot status={evaluationPhase(run.phase)} showLabel /> },
            { key: "stages", header: "단계", numeric: true, cell: run => records(run.spec.stages).length, sortValue: run => records(run.spec.stages).length },
            { key: "started", header: "시작", cell: run => run.startedAt ? <TimeText at={run.startedAt} mode="absolute" /> : null, sortValue: run => run.startedAt ?? "", missingReason: "시작 전" },
            { key: "finished", header: "종료", cell: run => run.finishedAt ? <TimeText at={run.finishedAt} mode="absolute" /> : null, sortValue: run => run.finishedAt ?? "", missingReason: "종료 전" },
          ]} />
      )}
    </QueryBoundary>
  )
}

function PublicationPicker() {
  const { config } = useSession()
  const [offsetText, setOffset] = useParam("offset")
  const offset = Math.max(0, Number.parseInt(offsetText, 10) || 0)
  const limit = Math.min(5, config.maxPageSize)
  const page = useChillQuery<ExperimentPage>(`v1/experiments?${new URLSearchParams({ offset: String(offset), limit: String(limit) })}`, { decoder: decodeExperimentPage })
  return (
    <QueryBoundary query={page} isEmpty={data => data.items.length === 0 && offset === 0} empty={<EmptyState title="게시 결과 없음" />}>
      {data => (
        <DataTable label="복사할 게시 결과" pageSize={0} rowKey={item => item.id} rowHref={item => `/experiments/runs/new?publication=${encodeURIComponent(item.id)}`} rows={data.items}
          title={data.unreadable?.length ? <StatusBadge tone="warn" label={`읽을 수 없는 결과 ${data.unreadable.length}건`} /> : undefined}
          paging={{ mode: "offset", offset, limit, total: data.page?.total, onOffsetChange: value => setOffset(value ? String(value) : "") }}
          columns={[
            { key: "name", header: "실험", width: "40%", cell: item => item.facts?.name ?? item.definition?.run.name ?? null, missingReason: "이름 미기록" },
            { key: "stages", header: "단계", numeric: true, cell: item => item.stages.length },
            { key: "from", header: "시작", cell: item => <TimeText at={item.from} mode="absolute" /> },
            { key: "to", header: "종료", cell: item => <TimeText at={item.to} mode="absolute" /> },
            { key: "verdict", header: "판정", cell: item => item.outcome?.measurement?.verdict ? <StatusDot status={experimentVerdict(item.outcome.measurement.verdict)} showLabel /> : null, missingReason: "판정 없음" },
          ]} />
      )}
    </QueryBoundary>
  )
}

const tabs = ["basic", "stages", "compare", "criteria"] as const
type Tab = typeof tabs[number]

function Editor({ title, source, missing = [] }: { title: string; source: Source; missing?: string[] }) {
  const router = useRouter()
  const params = useSearchParams()
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const tab: Tab = (tabs as readonly string[]).includes(params.get("tab") ?? "") ? params.get("tab") as Tab : "basic"
  const existing = source.existing
  const [name, setName] = React.useState(source.name)
  const [spec, setSpec] = React.useState<Spec>(() => structuredClone(source.spec))
  const [baseline] = React.useState(() => specJSON({ name: source.name, spec: source.spec }))
  const [revision, setRevision] = React.useState(0)
  const [invalid, setInvalid] = React.useState<ReadonlySet<string>>(new Set())
  const [rawOpen, setRawOpen] = React.useState(false)
  const [confirm, setConfirm] = React.useState(false)
  const [checkedBody, setCheckedBody] = React.useState("")
  const [unknown, setUnknown] = React.useState(false)
  const [blocked, setBlocked] = React.useState(false)
  const [done, setDone] = React.useState(false)
  const preflight = useChillMutation(), send = useChillMutation()

  const scenarios = useChillQuery<{ items: LoadScenario[] }>("v1/evaluations/load-scenarios", { decoder: decode })
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const inventory = useChillQuery<InventoryView>("v1/inventory")
  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", { decoder: decode })
  const comparisons = records(spec.comparisons)
  const runs = useChillQuery<{ items: Run[] }>("v1/evaluations", { decoder: decode, enabled: tab === "compare" || comparisons.length > 0 })
  const lookup = useChillQuery<Run>(`v1/evaluations/${encodeURIComponent(name || "_")}`, { decoder: decode, enabled: false })

  const context: EditorContext = React.useMemo(() => {
    const live = (workloads.data ?? []).map(item => ({ uid: item.desired.metadata.uid, name: item.desired.metadata.name }))
    const liveUIDs = new Set(live.map(item => item.uid))
    const retired = [...serviceNames(scenarios.data?.items ?? [])].filter(([uid]) => !liveUIDs.has(uid)).map(([uid, name]) => ({ uid, name, retired: true }))
    return {
      services: [...live, ...retired],
      nodes: (inventory.data?.classes ?? []).flatMap(item => item.nodes.map(node => node.name)),
      current: settings.data,
      scenarios: scenarios.data?.items ?? [],
      published: (runs.data?.items ?? []).filter(run => run.publicationID).map(run => ({ id: run.publicationID!, name: run.name, stages: records(run.spec.stages).map(stage => String(stage.id ?? "")) })),
    }
  }, [workloads.data, inventory.data, settings.data, scenarios.data, runs.data])

  const report = React.useCallback((id: string, bad: boolean) => setInvalid(current => {
    if (current.has(id) === bad) return current
    const next = new Set(current)
    if (bad) next.add(id)
    else next.delete(id)
    return next
  }), [])
  const edit = React.useCallback((path: Path, value: unknown) => setSpec(current => patchSpec(current, path, value)), [])
  // Reordering or removing rows moves values between inputs that keep their own text: remount them.
  const restructure = (key: string) => (rows: Spec[]) => { setSpec(current => patchSpec(current, [key], rows.length || key === "stages" ? rows : undefined)); setRevision(value => value + 1) }

  const body = stringifySchedulerJSON({ name, spec })
  const dirty = specJSON({ name, spec }) !== baseline
  const nameError = existing ? "" : runNameError(name, source.origin)
  const answer = preflight.data && checkedBody ? evaluationPreflight(preflight.data.data) : null
  const stale = !!checkedBody && checkedBody !== body
  const canSubmit = !done && !blocked && !nameError && invalid.size === 0 && evaluationCanSubmit(answer, checkedBody, body, unknown)
  const stages = records(spec.stages)
  const totalSeconds = stages.reduce((sum, stage) => {
    const scenario = context.scenarios.find(item => item.uid === object(stage.loadScenarioRef).uid)
    return scenario ? sum + scenarioSeconds(scenario.spec) : sum
  }, 0)
  const unresolved = stages.some(stage => !context.scenarios.some(item => item.uid === object(stage.loadScenarioRef).uid))

  function check() {
    setCheckedBody(body)
    preflight.mutate({ path: "v1/evaluations/preflight", method: "POST", body })
  }

  function finish(created: { name: string; uid: string }) {
    setDone(true)
    setConfirm(false)
    void queryClient.invalidateQueries({ queryKey: ["chill", "v1/evaluations"] })
    toast({ title: existing ? "실험 수정됨" : "실험 접수됨", type: "success" })
    router.push(runHref(created.name, created.uid))
  }

  // A lost response is never resent: read the run back and compare it with what was sent.
  async function reconcile(sent: Spec) {
    const read = await lookup.refetch()
    const status = read.error?.status ?? (read.data ? 200 : 0)
    if (existing) {
      const result = evaluationReplacement(existing.uid, sent, status, read.data)
      if (result.state === "updated") return finish({ name: existing.name, uid: result.uid })
      setUnknown(result.state === "unknown")
      if (result.state === "deleted") { setBlocked(true); toast({ title: "실험이 삭제됨", type: "error" }) }
      if (result.state === "unchanged") toast({ title: "변경되지 않음", type: "warning" })
      return
    }
    if (read.data && evaluationSameSpec(read.data.spec, sent)) return finish({ name: read.data.name, uid: read.data.uid })
    setUnknown(status !== 404)
    toast({ title: status === 404 ? "접수되지 않음" : "결과 미확인", type: status === 404 ? "info" : "warning" })
  }

  async function submit() {
    const sent = structuredClone(spec)
    try {
      const response = await send.mutateAsync(existing
        ? { path: `v1/evaluations/${encodeURIComponent(existing.name)}?${new URLSearchParams({ uid: existing.uid })}`, method: "PUT", body: stringifySchedulerJSON({ spec: sent }) }
        : { path: "v1/evaluations", method: "POST", body })
      const created = object(response.data)
      if (typeof created.name === "string" && typeof created.uid === "string") finish({ name: created.name, uid: created.uid })
      else { setConfirm(false); setUnknown(true); toast({ title: "결과 미확인", type: "warning" }) }
    } catch (error) {
      setConfirm(false)
      if (!(error instanceof ApiError)) { toast({ title: "요청 실패", type: "error" }); return }
      if (error.unknownOutcome || error.status === 0) { setUnknown(true); toast({ title: "결과 미확인", type: "warning" }); return }
      const code = (error.data as { code?: unknown } | null)?.code
      if (existing && error.status === 409) { setBlocked(true); toast({ title: "이미 시작되어 수정 불가", type: "error" }); return }
      toast({ title: existing ? "저장 실패" : "시작 실패", description: typeof code === "string" ? explain(code)[0] : `HTTP ${error.status}`, type: "error" })
    }
  }

  const submitLabel = existing ? "저장" : "실험 시작"
  const why = done ? "" : blocked ? "수정할 수 없는 상태" : unknown ? "결과 미확인" : nameError ? `이름: ${nameError}` : invalid.size ? `입력 오류 ${invalid.size}건` : !answer || stale || !answer.checks.every(item => item.passed) ? "사전 점검 통과 후 가능" : ""
  const summary = [
    { label: "단계", value: `${stages.length}개` },
    { label: "부하 길이", value: totalSeconds ? formatDurationNanos(totalSeconds * 1e9) : null, reason: "시나리오 미선택", info: unresolved ? "선택되지 않은 시나리오 제외" : undefined },
  ]

  return (
    <InvalidContext.Provider value={report}>
      <Page>
        <LeaveGuard when={dirty && !done} />
        <PageHeader title={title} more={[{ label: "원문 편집", onSelect: () => setRawOpen(true), disabled: done }]} />
        <PageTabs param="tab" defaultValue="basic" label="작성 항목" tabs={[
          { value: "basic", label: "기본" },
          { value: "stages", label: "단계", count: stages.length },
          { value: "compare", label: "비교", count: comparisons.length },
          { value: "criteria", label: "판정 기준" },
        ]} />
        {missing.length > 0 && <div><StatusBadge tone="warn" label={`입력 필요: ${missing.join(", ")}`} reason="원문 편집에서 입력" /></div>}
        <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
          <div key={revision} className="min-w-0">
            <div hidden={tab !== "basic"}><BasicSection spec={spec} edit={edit} context={context} name={name} setName={setName} nameError={nameError} locked={!!existing} /></div>
            <div hidden={tab !== "stages"}><StagesSection spec={spec} edit={edit} context={context} restructure={restructure("stages")} /></div>
            <div hidden={tab !== "compare"}><ComparisonsSection spec={spec} edit={edit} context={context} restructure={restructure("comparisons")} /></div>
            <div hidden={tab !== "criteria"}><CriteriaSection spec={spec} edit={edit} /></div>
          </div>
          <aside className="grid gap-4 lg:sticky lg:top-20">
            <Section card title="사전 점검" actions={
              <Hint text={invalid.size ? `입력 오류 ${invalid.size}건` : undefined} focusable={invalid.size > 0}>
                <Button type="button" size="sm" variant="outline" loading={preflight.isPending} disabled={done || unknown || invalid.size > 0} onClick={check}>{answer ? "다시 점검" : "점검"}</Button>
              </Hint>
            }>
              <Preflight answer={answer} stale={stale} pending={preflight.isPending} error={preflight.error} received={!!preflight.data && !!checkedBody} />
            </Section>
            <Section card>
              <div className="grid gap-4">
                <Fields columns={2} items={summary} />
                {unknown && <StatusBadge tone="warn" label="결과 미확인" className="justify-self-start" />}
                <div className="grid gap-2">
                  <Hint text={why || undefined} focusable={!!why} className="grid">
                    <Button type="button" variant="default" className="w-full" disabled={!canSubmit} loading={send.isPending} onClick={() => setConfirm(true)}>{submitLabel}</Button>
                  </Hint>
                  {unknown && <Button type="button" variant="outline" className="w-full" loading={lookup.isFetching} onClick={() => void reconcile(spec)}>다시 조회</Button>}
                </div>
              </div>
            </Section>
          </aside>
        </div>
        <ConfirmDialog open={confirm} onOpenChange={setConfirm} title={existing ? "실험 수정" : "실험 시작"} target={name} confirmLabel={existing ? "저장" : "시작"}
          changes={existing ? specChanges(source.spec, spec) : undefined} pending={send.isPending} onConfirm={() => void submit()}>
          {!existing && <Fields columns={2} items={summary} />}
        </ConfirmDialog>
        <RawEditor open={rawOpen} onOpenChange={setRawOpen} spec={spec} onApply={next => { setSpec(next); setRevision(value => value + 1) }} />
      </Page>
    </InvalidContext.Provider>
  )
}

function Preflight({ answer, stale, pending, error, received }: { answer: EvaluationPreflight | null; stale: boolean; pending: boolean; error: ApiError | null; received: boolean }) {
  if (error && !pending) {
    const code = (error.data as { code?: unknown } | null)?.code
    return <StatusBadge tone="fail" label="점검 실패" reason={typeof code === "string" ? `코드 ${code}` : error.status ? `HTTP ${error.status}` : "응답 없음"} />
  }
  if (!answer) return received && !pending
    ? <StatusBadge tone="fail" label="점검 결과 없음" reason="서버가 점검 항목을 보내지 않음" />
    : <p className="text-sm text-muted-foreground">{pending ? "점검 중" : "점검 전"}</p>
  const failed = answer.checks.filter(item => !item.passed)
  const passed = answer.checks.length - failed.length
  return (
    <div className={stale ? "grid gap-3 opacity-60" : "grid gap-3"}>
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge tone={stale ? "idle" : failed.length ? "fail" : "ok"} label={stale ? "변경 후 다시 점검" : failed.length ? `실패 ${failed.length}건` : "모두 통과"} />
        <Hint text={answer.checks.filter(item => item.passed).map(item => checkLabel(item.id).label).join(", ")} focusable className="text-sm text-muted-foreground tabular-nums">통과 {passed}건</Hint>
      </div>
      <ul className="grid gap-1.5 text-sm">
        {failed.map((item, index) => {
          const { label, code } = checkLabel(item.id)
          const reason = [!item.passed && checkReason(item.reason), code && `코드 ${code}`].filter(Boolean).join(" ")
          return (
            <li key={`${item.id}/${index}`} className="flex min-w-0 items-center gap-2">
              <StatusDot tone={item.passed ? "ok" : "fail"} label={item.passed ? "통과" : "실패"} />
              <Hint text={reason || undefined} focusable={!!reason} className="min-w-0 truncate text-left">
                {label}{!item.passed && item.reason && <span className="ml-2 text-muted-foreground">{checkReason(item.reason)}</span>}
              </Hint>
            </li>
          )
        })}
      </ul>
      {!!answer.liveTarget?.length && (
        <div className="grid gap-1.5 border-t border-border pt-3">
          <span className="text-xs font-medium text-muted-foreground">대상 구성요소</span>
          <ul className="grid gap-1.5 text-sm">
            {answer.liveTarget.map(target => (
              <li key={target.component} className="flex min-w-0 items-center gap-2">
                <StatusDot tone={target.ready ? "ok" : "warn"} label={target.ready ? "준비됨" : "준비 안 됨"} />
                <span className="truncate">{target.component}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

function RawEditor({ open, onOpenChange, spec, onApply }: { open: boolean; onOpenChange: (open: boolean) => void; spec: Spec; onApply: (spec: Spec) => void }) {
  // A fresh body each time the dialog opens, so the text starts from the current definition.
  const [opened, setOpened] = React.useState(0)
  const [wasOpen, setWasOpen] = React.useState(open)
  if (open !== wasOpen) {
    setWasOpen(open)
    if (open) setOpened(count => count + 1)
  }
  return <RawBody key={opened} open={open} spec={spec} onCancel={() => onOpenChange(false)} onApply={next => { onApply(next); onOpenChange(false) }} />
}
function RawBody({ open, spec, onCancel, onApply }: { open: boolean; spec: Spec; onCancel: () => void; onApply: (spec: Spec) => void }) {
  const [text, setText] = React.useState(() => specJSON(spec))
  const [error, setError] = React.useState("")
  const id = React.useId()
  return (
    <Dialog open={open} onOpenChange={next => { if (!next) onCancel() }} title="원문 편집" size="xl"
      onSubmit={() => { try { onApply(parseSpec(text)) } catch { setError("JSON 객체 형식 오류") } }}
      footer={<>
        <Button type="button" variant="outline" onClick={onCancel}>취소</Button>
        <Button type="submit" variant="default">적용</Button>
      </>}>
      <Textarea id={id} aria-label="실험 정의" aria-invalid={!!error} aria-describedby={`${id}-error`} spellCheck={false} rows={20} wrap="off"
        className="max-h-[60dvh] font-mono text-xs leading-5" value={text} onChange={event => { setText(event.target.value); setError("") }} />
      <p id={`${id}-error`} role="status" className="min-h-5 text-sm text-fail">{error}</p>
    </Dialog>
  )
}
