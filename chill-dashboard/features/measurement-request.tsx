"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useState, type ReactNode } from "react"
import { useChillMutation, useChillQuery, useSession, type ApiError } from "@/lib/client"
import { QueryGate, SourceJSON, StatusBadge } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { PreflightAnswer, preflightConclusion } from "@/features/read-pages"
import { powerModeText } from "@/features/resources"
import { codeText } from "@/lib/labels"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { ExecutionPath, ModelList, ModelResource, WorkloadView } from "@/lib/domain"
import type { InventoryClass, InventoryNode, InventoryView, MeasurementMutation, MeasurementPlan, MeasurementPlanList, MeasurementPlanSpec, MeasurementPreflight } from "@/lib/read-types"

const back: [string, string] = ["/measurements", "측정"]
const steps = ["workload", "device", "run", "size", "advanced", "name", "review"] as const
type Step = typeof steps[number]
const batchChoices = [1, 2, 4, 8, 16]
const cpuChoices = ["1", "2", "4"]
const dns1123 = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/

type Advanced = { warmup: number; activeDuration: number; activeWindow: number; activeInterval: number; activeTimeout: number; settling: number; idleDuration: number; idleWindow: number; idleInterval: number; idleTimeout: number; retryLimit: number; affinity: string }
type NumberKey = Exclude<keyof Advanced, "affinity">
const advancedFields: [key: NumberKey, label: string, min: number][] = [
  ["warmup", "측정 전 예열 횟수", 0],
  ["activeDuration", "측정 시간 · 초", 1],
  ["activeWindow", "전력 기록 시간 · 초", 1],
  ["activeInterval", "전력 기록 간격 · 밀리초", 1],
  ["activeTimeout", "전력 기록 대기 한도 · 밀리초", 1],
  ["settling", "쉬는 상태 안정 시간 · 초", 1],
  ["idleDuration", "쉬는 상태 측정 시간 · 초", 1],
  ["idleWindow", "쉬는 상태 전력 기록 시간 · 초", 1],
  ["idleInterval", "쉬는 상태 전력 기록 간격 · 밀리초", 1],
  ["idleTimeout", "쉬는 상태 전력 기록 대기 한도 · 밀리초", 1],
  ["retryLimit", "실패하면 다시 시도할 횟수", 0],
]
type Protocol = { values: Advanced; from: string | null }
// model, preprocessor and dataset come from the chosen Workload's spec refs (or a copied plan); the API takes catalog names.
type Draft = { workload: string; model: string; preprocessor: string; dataset: string; deviceClass: string; node: string; path: string; powerMode: string; batchSizes: number[]; trials: number | null; cpu: string | null; protocol: Protocol | null; name: string | null }
type Context = { models: ModelResource[]; workloads: WorkloadView[]; classes: InventoryClass[]; plans: MeasurementPlan[] }

const emptyDraft: Draft = { workload: "", model: "", preprocessor: "", dataset: "", deviceClass: "", node: "", path: "", powerMode: "", batchSizes: [], trials: null, cpu: null, protocol: null, name: null }
const blankAdvanced: Advanced = { warmup: NaN, activeDuration: NaN, activeWindow: NaN, activeInterval: NaN, activeTimeout: NaN, settling: NaN, idleDuration: NaN, idleWindow: NaN, idleInterval: NaN, idleTimeout: NaN, retryLimit: NaN, affinity: "" }
function copyAdvanced(spec: MeasurementPlanSpec): Advanced {
  const active = spec.activeProtocol, idle = spec.idleProtocol
  return {
    warmup: active.warmupIterations ?? NaN, activeDuration: active.durationSeconds ?? NaN, activeWindow: active.metricObservation?.durationSeconds ?? NaN,
    activeInterval: active.metricObservation?.intervalMilliseconds ?? NaN, activeTimeout: active.metricObservation?.timeoutMilliseconds ?? NaN,
    settling: idle.settlingDurationSeconds ?? NaN, idleDuration: idle.durationSeconds ?? NaN, idleWindow: idle.metricObservation?.durationSeconds ?? NaN,
    idleInterval: idle.metricObservation?.intervalMilliseconds ?? NaN, idleTimeout: idle.metricObservation?.timeoutMilliseconds ?? NaN,
    retryLimit: spec.retryLimit, affinity: spec.cpuAffinity?.join(", ") ?? "",
  }
}
function parseAffinity(text: string): number[] | null {
  const parts = text.split(/[\s,]+/).filter(Boolean)
  if (parts.some(part => !/^\d+$/.test(part) || Number(part) > 2147483647)) return null
  return [...new Set(parts.map(Number))].sort((a, b) => a - b)
}
function advancedProblem(values: Advanced) {
  const bad = advancedFields.find(([key, , min]) => !Number.isInteger(values[key]) || values[key] < min || values[key] > 2147483647)
  if (bad) return `"${bad[1]}"에 ${bad[2]} 이상의 정수를 적어 주세요.`
  if (values.retryLimit > 10) return "다시 시도할 횟수는 10번까지 정할 수 있어요."
  if (values.activeTimeout >= values.activeInterval || values.idleTimeout >= values.idleInterval) return "전력 기록 대기 한도는 기록 간격보다 짧아야 해요."
  if (values.activeWindow < values.activeDuration || values.idleWindow < values.idleDuration) return "전력 기록 시간은 측정 시간보다 짧을 수 없어요."
  if (!parseAffinity(values.affinity)) return "CPU 코어 번호는 0 이상의 정수를 쉼표로 나눠 적어 주세요."
  return ""
}
const nodeReason = (node: InventoryNode) => node.ready !== "True" ? "지금 준비되지 않았어요" : node.schedulingDisabled ? "새 작업을 받지 않도록 막혀 있어요" : ""
// A path runs on a device class only when its artifact was built for the class architecture.
function pathReason(model: ModelResource, path: ExecutionPath, cls: InventoryClass) {
  const artifact = model.spec.artifacts.find(a => a.name === path.artifact)
  if (!artifact) return "모델 파일 정보를 찾지 못했어요"
  return artifact.source.architecture === cls.architecture ? "" : `${artifact.source.architecture} 장비용 파일이라 이 장비에서는 실행할 수 없어요`
}
const sanitize = (text: string) => text.toLowerCase().replace(/[^a-z0-9.-]+/g, "-").replace(/[-.]*\.[-.]*/g, ".").replace(/-+/g, "-").slice(0, 200).replace(/^[^a-z0-9]+|[^a-z0-9]+$/g, "")
function suggest(d: Draft, taken: Set<string>) {
  const now = new Date()
  const day = `${now.getFullYear()}${String(now.getMonth() + 1).padStart(2, "0")}${String(now.getDate()).padStart(2, "0")}`
  const base = sanitize(`${d.model}-${d.node}-${d.path.split("-")[0]}-${day}`)
  let name = base
  for (let i = 2; taken.has(name); i++) name = sanitize(`${base.slice(0, 190)}-${i}`)
  return name
}
const detailHref = (namespace: string, name: string, uid?: string) => `/measurements/batch-energy-optimizations/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}${uid ? `?uid=${encodeURIComponent(uid)}` : ""}`
function failure(error: ApiError) {
  const data = (error.data && typeof error.data === "object" ? error.data : {}) as { code?: string; message?: string }
  return { code: data.code, message: data.message || "서버가 이유를 알려 주지 않았어요." }
}

// Everything the steps read: current selections resolved against real data, plus the copied protocol.
// Newest earlier plan for the same model, device class and execution path.
function sameCondition(plans: MeasurementPlan[], model: string, deviceClass: string, path: string) {
  return [...plans].sort((a, b) => b.metadata.creationTimestamp.localeCompare(a.metadata.creationTimestamp))
    .find(p => p.spec.state.candidate.modelSpec === model && p.spec.state.candidate.deviceClass === deviceClass && p.spec.state.candidate.executionPath === path)
}
function view(d: Draft, { models, classes, plans }: Context) {
  const model = models.find(m => m.metadata.name === d.model)
  const cls = classes.find(c => c.name === d.deviceClass)
  const node = cls?.nodes.find(n => n.name === d.node)
  const path = model?.spec.executionPaths.find(p => p.name === d.path)
  const artifact = path && model?.spec.artifacts.find(a => a.name === path.artifact)
  const newest = [...plans].sort((a, b) => b.metadata.creationTimestamp.localeCompare(a.metadata.creationTimestamp))
  const template = sameCondition(plans, d.model, d.deviceClass, d.path) ?? newest[0]
  const protocol: Protocol = d.protocol ?? { values: template ? copyAdvanced(template.spec) : blankAdvanced, from: template?.metadata.name ?? null }
  const trials = d.trials ?? template?.spec.trialsPerBatch ?? 3
  const cpu = d.cpu ?? template?.spec.cpuQuota ?? "1"
  const powerMode = cls?.powerModes.length === 1 ? cls.powerModes[0].name : d.powerMode
  const declared = artifact?.derivation?.declaredBatchSizes?.length ? artifact.derivation.declaredBatchSizes : null
  const max = path?.runtime.settings?.firstfloor?.maxBatchSize
  const taken = new Set(plans.map(p => p.metadata.name))
  const name = d.name ?? (model && node && path ? suggest(d, taken) : "")
  const affinity = parseAffinity(protocol.values.affinity) ?? []
  function problem(step: Step): string {
    switch (step) {
      case "workload": return model && d.preprocessor && d.dataset ? "" : "잴 서비스를 골라 주세요."
      case "device":
        if (!cls || !node) return "잴 장비를 골라 주세요."
        if (nodeReason(node)) return `${node.name}는 ${nodeReason(node)}. 다른 장비를 골라 주세요.`
        return model && !model.spec.executionPaths.some(p => !pathReason(model, p, cls)) ? "이 모델은 이 장비에서 실행할 수 없어요. 다른 장비를 골라 주세요." : ""
      case "run":
        if (!model || !cls || !path || pathReason(model, path, cls)) return "실행 방식을 골라 주세요."
        if (!cls.powerModes.length) return "이 장비에 등록된 전력 모드가 없어서 잴 수 없어요."
        return cls.powerModes.some(m => m.name === powerMode) ? "" : "전력 모드를 골라 주세요."
      case "size":
        if (!declared && (!d.batchSizes.length || (max && d.batchSizes.some(b => b > max)))) return max ? `한 번에 처리할 건수를 ${max}건 이하로 하나 이상 골라 주세요.` : "한 번에 처리할 건수를 하나 이상 골라 주세요."
        if (!Number.isInteger(trials) || trials < 3 || trials > 2147483647) return "반복 횟수는 3번 이상이어야 해요."
        return /^[1-9]\d*$/.test(cpu) ? "" : "CPU 개수를 골라 주세요."
      case "advanced": return advancedProblem(protocol.values)
      case "name":
        if (!name) return "이름을 적어 주세요."
        if (name.length > 200 || !dns1123.test(name)) return "이름은 200자 이하의 영문 소문자, 숫자, '-', '.'로 적고, 처음과 끝은 영문 소문자나 숫자여야 해요."
        return taken.has(name) ? "같은 이름의 측정이 이미 있어요. 다른 이름을 적어 주세요." : ""
      case "review": return ""
    }
  }
  return { model, cls, path, protocol, trials, cpu, powerMode, declared, max, taken, name, affinity, problem }
}
function manifest(d: Draft, context: Context, namespace: string) {
  const v = view(d, context)
  const p = v.protocol.values
  const spec: MeasurementPlanSpec = {
    state: { candidate: { modelSpec: d.model, preprocessor: d.preprocessor, deviceClass: d.deviceClass, executionPath: d.path, powerMode: v.powerMode }, targetNode: d.node, cpuRequest: v.cpu },
    activeProtocol: { warmupIterations: p.warmup, durationSeconds: p.activeDuration, dataset: d.dataset, metricObservation: { intervalMilliseconds: p.activeInterval, timeoutMilliseconds: p.activeTimeout, durationSeconds: p.activeWindow } },
    idleProtocol: { settlingDurationSeconds: p.settling, durationSeconds: p.idleDuration, metricObservation: { intervalMilliseconds: p.idleInterval, timeoutMilliseconds: p.idleTimeout, durationSeconds: p.idleWindow } },
    cpuQuota: v.cpu,
    ...(v.affinity.length ? { cpuAffinity: v.affinity } : {}),
    trialsPerBatch: v.trials,
    // The CRD forbids batchSizes when the artifact declares its own set.
    ...(v.declared ? {} : { batchSizes: [...d.batchSizes].sort((a, b) => a - b) }),
    retryLimit: p.retryLimit,
  }
  return { apiVersion: "edge.dacs.io/v1alpha1", kind: "BatchEnergyOptimization", metadata: { name: v.name, namespace }, spec }
}

// `?from=` copies a whole existing plan; the funnel then opens at the first step that needs attention, else the name.
function begin(from: string | null, context: Context) {
  const source = from ? context.plans.find(p => p.metadata.name === from) : undefined
  if (!source) return { draft: emptyDraft, index: 0, error: from ? "복사할 측정을 찾지 못해서 처음부터 골라요." : "", copied: false }
  const s = source.spec
  const draft: Draft = { workload: "", model: s.state.candidate.modelSpec, preprocessor: s.state.candidate.preprocessor, dataset: s.activeProtocol.dataset ?? "", deviceClass: s.state.candidate.deviceClass, node: s.state.targetNode, path: s.state.candidate.executionPath, powerMode: s.state.candidate.powerMode, batchSizes: s.batchSizes ?? [], trials: s.trialsPerBatch, cpu: s.cpuQuota ?? s.state.cpuRequest ?? null, protocol: { values: copyAdvanced(s), from: source.metadata.name }, name: null }
  const v = view(draft, context)
  const open = steps.findIndex(step => step !== "name" && step !== "review" && v.problem(step))
  return { draft, index: open < 0 ? steps.indexOf("name") : open, error: open < 0 ? "" : v.problem(steps[open]), copied: true }
}

type Option = { value: string; label: ReactNode; hint?: ReactNode; disabled?: string }
// Pick one existing item; unavailable ones stay visible with the reason.
function Pick({ label, name, value, set, options }: { label: string; name: string; value: string; set: (value: string) => void; options: Option[] }) {
  return <fieldset className="rows-group"><legend className="rows-label">{label}</legend><ul className="rows">{options.map(option => <li key={option.value}><label className={`row ${option.disabled ? "cursor-not-allowed opacity-50" : "cursor-pointer"}`}>
    <input type="radio" name={name} value={option.value} checked={value === option.value} disabled={!!option.disabled} onChange={() => set(option.value)} />
    <span className="row-main"><span className="row-text">{option.label}</span>{(option.disabled || option.hint) && <span className="row-hint">{option.disabled || option.hint}</span>}</span>
  </label></li>)}</ul></fieldset>
}
function AdvancedFields({ value, set }: { value: Advanced; set: (value: Advanced) => void }) {
  return <div className="grid gap-4 sm:grid-cols-2">
    {advancedFields.map(([key, label, min]) => <label key={key} className="grid gap-2 text-sm">{label}<Input type="number" inputMode="numeric" min={min} max={key === "retryLimit" ? 10 : 2147483647} step={1} value={Number.isNaN(value[key]) ? "" : value[key]} onChange={event => set({ ...value, [key]: event.target.valueAsNumber })} /></label>)}
    <label className="grid gap-2 text-sm sm:col-span-2">사용할 CPU 코어 번호 · 비워 두면 정하지 않아요<Input inputMode="numeric" placeholder="예: 3 또는 2, 3" value={value.affinity} onChange={event => set({ ...value, affinity: event.target.value })} /></label>
  </div>
}
function AdvancedForm({ initial, save }: { initial: Advanced; save: (value: Advanced) => void }) {
  const [values, setValues] = useState(initial)
  const [error, setError] = useState("")
  return <form className="grid gap-4" onSubmit={event => { event.preventDefault(); const issue = advancedProblem(values); if (issue) setError(issue); else save(values) }}>
    <AdvancedFields value={values} set={setValues} />
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    <Button type="submit">이 방식으로 재기</Button>
  </form>
}

export function MeasurementRequestPage() {
  const { config } = useSession()
  const from = useSearchParams().get("from")
  const plans = useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(config.namespace)}/batch-energy-optimizations`)
  const models = useChillQuery<ModelList>("v1/models")
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads")
  const inventory = useChillQuery<InventoryView>(`v1/inventory?limit=${config.maxPageSize}`)
  if (!plans.data || !models.data || !workloads.data || !inventory.data) return <Screen back={back} title="측정 요청" lede="고를 수 있는 서비스와 장비를 확인하고 있어요.">
    <QueryGate query={plans}><QueryGate query={models}><QueryGate query={workloads}><QueryGate query={inventory}>{null}</QueryGate></QueryGate></QueryGate></QueryGate>
  </Screen>
  const context: Context = { models: models.data.items, workloads: workloads.data, classes: inventory.data.classes, plans: plans.data.items ?? [] }
  return <Funnel key={from ?? ""} namespace={config.namespace} from={from} context={context} more={inventory.data.classPage?.nextOffset !== undefined} refetchPlans={plans.refetch} checking={plans.isFetching} />
}

function Funnel({ namespace, from, context, more, refetchPlans, checking }: { namespace: string; from: string | null; context: Context; more: boolean; refetchPlans: () => Promise<{ error: ApiError | null }>; checking: boolean }) {
  const router = useRouter()
  const dry = useChillMutation(), pre = useChillMutation(), send = useChillMutation()
  const [start] = useState(() => begin(from, context))
  const [d, setD] = useState(start.draft)
  const [index, setIndex] = useState(start.index)
  const [error, setError] = useState(start.error)
  const [unknown, setUnknown] = useState(false)
  const [check, setCheck] = useState<"" | "done" | "failed">("")
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const v = view(d, context)
  const step = steps[index]
  const listPath = `v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
  const update = (patch: Partial<Draft>) => { setD(current => ({ ...current, ...patch })); setError("") }

  function go(next: number) {
    setError(""); dry.reset(); pre.reset(); send.reset(); setIndex(next)
    if (steps[next] !== "review") return
    // Pin the suggested name so a later list refresh can never change what gets sent.
    const pinned = { ...d, name: v.name }
    setD(pinned)
    verify(pinned)
  }
  function conflict() {
    go(steps.indexOf("name"))
    setError("같은 이름의 측정이 이미 있어요. 다른 이름을 정해 주세요.")
    void refetchPlans()
  }
  // Server dry-run checks admission; ChillServer preflight resolves the plan. Both must pass before submit.
  function verify(draft = d) {
    const body = manifest(draft, context, namespace)
    dry.mutate({ path: `${listPath}?dryRun=All`, method: "POST", body }, { onError: e => { if (e.status === 409) conflict() } })
    pre.mutate({ path: `${listPath}/preflight`, method: "POST", body })
  }
  function submit() {
    send.mutate({ path: listPath, method: "POST", body: manifest(d, context, namespace) }, {
      onSuccess: response => {
        const task = (response.data as MeasurementMutation | null)?.task
        router.replace(detailHref(task?.metadata.namespace ?? namespace, task?.metadata.name ?? v.name, task?.metadata.uid))
      },
      // MeasurementAuditIncomplete means "may have been accepted": same as a lost response, never resent automatically.
      onError: e => { if (e.status === 409) conflict(); else if (e.unknownOutcome || e.status === 0 || failure(e).code === "MeasurementAuditIncomplete") setUnknown(true) },
    })
  }
  function next() {
    const issue = v.problem(step)
    if (issue) setError(issue)
    else go(index + 1)
  }

  if (unknown) {
    const found = check === "done" ? context.plans.find(p => p.metadata.name === v.name) : undefined
    return <Screen back={back} status={<StatusBadge tone="warn">결과 미확인</StatusBadge>} title="요청 결과를 확인하지 못했어요"
      lede={check === "failed" ? "목록을 불러오지 못했어요. 잠시 뒤 다시 확인해 주세요." : found ? "요청이 접수돼 있어요. 아래에서 열어 보세요." : check === "done" ? "아직 목록에 없어요. 잠시 뒤 다시 확인해 주세요." : "접수됐을 수도 있어요. 자동으로 다시 보내지 않으니, 먼저 접수됐는지 확인해 주세요."}
      action={<Button className="w-full" disabled={checking} onClick={() => void refetchPlans().then(result => setCheck(result.error ? "failed" : "done"))}>{checking ? "확인하고 있어요" : "요청이 접수됐는지 확인하기"}</Button>}>
      <Rows>
        {found && <Row href={detailHref(found.metadata.namespace, found.metadata.name, found.metadata.uid)} label="접수된 측정 보기" value={found.metadata.name} />}
        {check === "done" && !found && <Row label="작성한 내용으로 돌아가기" hint="조건을 다시 확인한 뒤 직접 요청할 수 있어요" onClick={() => { setUnknown(false); setCheck(""); go(steps.indexOf("review")) }} />}
        {send.error && <RowDialog label="받은 응답 보기" title="받은 응답"><SourceJSON inline value={{ status: send.error.status, body: send.error.data ?? send.error.body }} /></RowDialog>}
      </Rows>
    </Screen>
  }

  const answer = pre.data?.data as MeasurementPreflight | undefined
  const admitted = dry.isSuccess && (dry.data?.data as MeasurementMutation | null)?.request === "NOT_SUBMITTED"
  const passed = admitted && answer?.resolves === true
  const unresolved = answer?.resolves === false
  const pending = dry.isPending || pre.isPending
  const denied = dry.error?.status === 403 || pre.error?.status === 403 || send.error?.status === 403
  const rejected = !!dry.error && dry.error.status >= 400 && dry.error.status < 500
  const unchecked = (!!dry.error && !rejected) || (dry.isSuccess && !admitted) || !!pre.error || (pre.isSuccess && !answer)
  const sizes = v.declared ?? d.batchSizes
  const titles: Record<Step, string> = {
    workload: "어떤 서비스의 조건으로 잴까요?", device: "어느 장비에서 잴까요?", run: "어떻게 실행할까요?", size: "한 번에 몇 건씩, 몇 번씩 잴까요?",
    advanced: v.protocol.from ? "이전 측정과 같은 방식으로 재요" : "어떤 방식으로 잴까요?", name: "이름을 정해 주세요",
    review: denied ? "측정을 요청할 권한이 없어요" : send.error ? "측정을 요청하지 못했어요" : pending ? "요청할 수 있는지 확인하고 있어요" : unresolved ? preflightConclusion(answer!) : rejected ? "이대로는 요청할 수 없어요" : unchecked ? "요청할 수 있는지 확인하지 못했어요" : answer ? preflightConclusion(answer) : "이대로 측정을 요청할까요?",
  }
  const ledes: Record<Step, string> = {
    workload: "서비스가 쓰는 모델, 전처리기, 데이터셋으로 재요.",
    device: more ? "장비 종류가 많아 앞쪽 일부만 보여요. 준비된 노드만 고를 수 있어요." : "준비된 노드만 고를 수 있어요.",
    run: "이 장비에서 돌릴 수 있는 방식만 고를 수 있어요.",
    size: "크기마다 정한 횟수만큼 재서 전력을 가장 적게 쓰는 크기를 찾아요.",
    advanced: v.protocol.from ? "바꾸고 싶을 때만 열어 보세요." : "처음 하는 측정이라 재는 방식을 정해 주세요.",
    name: start.copied ? "기존 측정 조건을 그대로 가져왔어요. 이름만 새로 정하면 돼요." : "추천한 이름을 그대로 써도 돼요. 이 이름으로 결과를 찾아요.",
    review: denied ? "권한이 있는 계정으로 다시 접속해 주세요. 작성한 내용은 그대로 있어요." : send.error ? `${failure(send.error).message} 작성한 내용은 그대로 있어요.` : pending ? "실제로 요청하지는 않아요." : unresolved ? "이전 단계에서 조건을 고쳐 주세요. 아직 요청하지 않았어요." : rejected ? `${failure(dry.error!).message} 이전 단계에서 고쳐 주세요.` : unchecked ? "잠시 뒤 다시 확인해 주세요. 아직 요청하지 않았어요." : "미리 확인을 통과했어요. 아직 요청하지 않았어요.",
  }
  const badge = step !== "review" ? null : pending ? <StatusBadge tone="info">확인 중</StatusBadge> : passed && !send.error ? <StatusBadge tone="ok">확인 통과</StatusBadge> : <StatusBadge tone="fail">요청 불가</StatusBadge>
  const chips = [...new Set([...batchChoices.filter(b => !v.max || b <= v.max), ...d.batchSizes])].sort((a, b) => a - b)
  const primary = step !== "review" ? <Button className="flex-[2]" onClick={next}>다음</Button>
    : unchecked ? <Button className="flex-[2]" disabled={pending} onClick={() => verify()}>다시 확인하기</Button>
    : <Button className="flex-[2]" disabled={!passed || denied || send.isPending} onClick={submit}>{send.isPending ? "요청하고 있어요" : "측정 요청하기"}</Button>

  return <Screen back={back} status={<span className="flex items-center gap-2 text-sm text-muted-foreground">{index + 1} / {steps.length}{badge}</span>} title={titles[step]} lede={ledes[step]}
    action={<div className="flex gap-3">{index > 0 && <Button variant="outline" className="flex-1" disabled={send.isPending} onClick={() => go(index - 1)}>이전</Button>}{primary}</div>}>
    {step === "workload" && (context.workloads.length
      ? <>
        <Pick label="서비스" name="workload" value={d.workload} set={workload => {
          const spec = context.workloads.find(w => w.desired.metadata.name === workload)!.desired.spec
          update({ workload, model: spec.modelRef.name, preprocessor: spec.preprocessorRef.name, dataset: spec.datasetRef?.name ?? "" })
        }} options={context.workloads.map(({ desired: w }) => {
          const model = context.models.find(m => m.metadata.name === w.spec.modelRef.name)
          return { value: w.metadata.name, label: w.metadata.name, hint: `${w.spec.modelRef.name} · ${w.spec.preprocessorRef.name} · ${w.spec.datasetRef?.name ?? "데이터셋 없음"}`,
            disabled: !model ? "모델을 찾지 못했어요" : !model.spec.executionPaths.length ? "실행 방식이 없어서 잴 수 없어요" : w.spec.datasetRef ? "" : "데이터셋이 없어서 잴 수 없어요" }
        })} />
        {d.model && <Facts items={[["모델", d.model], ["전처리기", d.preprocessor || "없음"], ["데이터셋", d.dataset || "없음"]]} />}
      </>
      : <Empty title="잴 수 있는 서비스가 없어요"><Button asChild variant="outline"><Link href="/workloads/new">서비스 추가</Link></Button></Empty>)}
    {step === "device" && (context.classes.some(c => c.nodes.length)
      ? context.classes.filter(c => c.nodes.length).map(c => {
        const fits = !v.model || v.model.spec.executionPaths.some(p => !pathReason(v.model!, p, c))
        return <Pick key={c.name} label={`${c.name} · ${c.accelerator ?? "가속기 없음"} · 메모리 ${c.memory}`} name="node" value={d.deviceClass === c.name ? d.node : ""} set={node => update({ deviceClass: c.name, node })}
          options={c.nodes.map(n => ({ value: n.name, label: n.name, disabled: fits ? nodeReason(n) : "이 모델을 실행할 수 없는 장비예요" }))} />
      })
      : <Empty title="고를 수 있는 장비가 없어요"><Button asChild variant="outline"><Link href="/resources">장비 보기</Link></Button></Empty>)}
    {step === "run" && <>
      <Pick label="실행 방식" name="path" value={d.path} set={path => update({ path, batchSizes: d.batchSizes.length ? d.batchSizes : sameCondition(context.plans, d.model, d.deviceClass, path)?.spec.batchSizes ?? [] })} options={(v.model?.spec.executionPaths ?? []).map(p => ({ value: p.name, label: p.name, hint: `${codeText[p.runtime.family]?.[0] ?? p.runtime.family} · ${codeText[p.runtime.backend]?.[0] ?? p.runtime.backend}`, disabled: v.cls ? pathReason(v.model!, p, v.cls) : "" }))} />
      {v.cls && v.cls.powerModes.length > 0 && <Pick label="전력 모드" name="power" value={v.powerMode} set={powerMode => update({ powerMode })} options={v.cls.powerModes.map(m => ({ value: m.name, label: m.name, hint: powerModeText(m.actuator) }))} />}
    </>}
    {step === "size" && <div className="grid gap-6">
      {v.declared
        ? <Facts items={[["한 번에 처리할 건수", `${v.declared.join(", ")}건 · 모델 파일에 정해져 있어요`]]} />
        : <fieldset className="grid gap-3"><legend className="rows-label">한 번에 처리할 건수 · 여러 개 고를 수 있어요</legend>
          <div className="flex flex-wrap gap-2">{chips.map(b => <label key={b} className="cursor-pointer rounded-full border px-4 py-2 text-sm has-[:checked]:border-primary has-[:checked]:bg-primary has-[:checked]:text-primary-foreground has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring">
            <input type="checkbox" className="sr-only" checked={d.batchSizes.includes(b)} onChange={event => update({ batchSizes: event.target.checked ? [...d.batchSizes, b].sort((x, y) => x - y) : d.batchSizes.filter(x => x !== b) })} />{b}건
          </label>)}</div>
          {v.max && <p className="text-sm text-muted-foreground">이 실행 방식은 한 번에 {v.max}건까지 처리해요.</p>}
        </fieldset>}
      <label className="grid gap-2 text-sm">크기마다 반복할 횟수 · 3번 이상<Input type="number" inputMode="numeric" min={3} step={1} value={Number.isNaN(v.trials) ? "" : v.trials} onChange={event => update({ trials: event.target.valueAsNumber })} /></label>
      <Pick label="CPU 개수" name="cpu" value={v.cpu} set={cpu => update({ cpu })} options={[...new Set([...cpuChoices, v.cpu])].map(c => ({ value: c, label: `${c}개` }))} />
    </div>}
    {step === "advanced" && (v.protocol.from || context.plans.length
      ? <Rows><RowDialog open={advancedOpen} onOpenChange={setAdvancedOpen} label="세부 측정 방식" hint={v.protocol.from ? `복사한 측정: ${v.protocol.from}` : "직접 정한 방식"} value={`측정 ${v.protocol.values.activeDuration}초 · 쉬는 상태 ${v.protocol.values.idleDuration}초`} title="세부 측정 방식" description="측정 시간, 전력 기록, 다시 시도 횟수와 CPU 코어를 정해요.">
        <AdvancedForm initial={v.protocol.values} save={values => { update({ protocol: { values, from: null } }); setAdvancedOpen(false) }} />
      </RowDialog></Rows>
      : <AdvancedFields value={v.protocol.values} set={values => update({ protocol: { values, from: null } })} />)}
    {step === "name" && <>
      <label className="grid gap-2 text-sm">측정 이름<Input value={v.name} maxLength={200} autoComplete="off" spellCheck={false} onChange={event => update({ name: event.target.value })} /></label>
      {v.taken.has(v.name) && !error && <p role="status" className="text-sm text-muted-foreground">같은 이름의 측정이 이미 있어요.</p>}
    </>}
    {step === "review" && <>
      {answer && <PreflightAnswer answer={answer} />}
      <Facts items={[
        ["모델", d.model],
        ["전처리기", d.preprocessor],
        ["데이터셋", d.dataset],
        ["장비", `${d.node} · ${d.deviceClass}`],
        ["실행 방식", `${d.path} · 전력 모드 ${v.powerMode}`],
        ["크기별 반복", `한 번에 ${sizes.join(", ")}건씩 · 크기마다 ${v.trials}번`],
        ["CPU", `${v.cpu}개${v.affinity.length ? ` · 코어 ${v.affinity.join(", ")}` : ""}`],
        ["측정 방식", v.protocol.from ? "이전 측정과 같아요" : "직접 정했어요"],
        ["예상 회차 수", `최대 ${(sizes.length + 1) * v.trials}회`],
        ["이름", v.name],
      ]} />
      <Rows>
        <RowDialog label="보낼 내용 보기" title="보낼 내용"><SourceJSON inline value={manifest(d, context, namespace)} /></RowDialog>
        {dry.error && <RowDialog label="확인 결과 자세히" title="미리 확인한 결과"><SourceJSON inline value={{ status: dry.error.status, body: dry.error.data ?? dry.error.body }} /></RowDialog>}
        {(answer || pre.error) && <RowDialog label="측정 계획 원본" title="ChillServer가 확인한 측정 계획"><SourceJSON inline value={answer ?? { status: pre.error!.status, body: pre.error!.data ?? pre.error!.body }} /></RowDialog>}
        {send.error && <RowDialog label="받은 응답 보기" title="받은 응답"><SourceJSON inline value={{ status: send.error.status, body: send.error.data ?? send.error.body }} /></RowDialog>}
      </Rows>
    </>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
  </Screen>
}
