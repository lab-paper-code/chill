"use client"

import * as React from "react"
import { useSearchParams } from "next/navigation"
import { ChevronDown } from "lucide-react"
import { Button, Fields, FormActions, FormSection, LeaveGuard, Page, PageHeader, QueryBoundary, RawDialog, StatusBadge } from "@/components/kit"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { SelectBlock } from "@/components/custom/blocks/SelectBlock"
import { Stepper } from "@/components/custom/blocks/StepperBlock"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/custom/blocks/CollapsibleBlock"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"
import { useChillQueries, useChillQuery } from "@/lib/client"
import type { ApiError } from "@/lib/client-transport"
import type { DatasetResource, ModelList, PreprocessorResource, WorkloadSummary } from "@/lib/domain"
import type { InventoryView, MeasurementCoverageSummaryResponse, ModelCompatibility } from "@/lib/read-types"
import { parseMeasurementJSON } from "@/lib/measurement-summary"
import { resolveMeasurementPrefill } from "@/lib/measurement-coverage"
import { deviceClassLabel, executionPathLabel, powerModeLabel } from "@/lib/display-labels"
import { formatSeconds } from "@/lib/format"
import { begin, emptyDraft, manifest, nodeReason, pathReason, requestCopySource, requestHref, steps, updateDraft, view, type Advanced, type Context, type Draft, type Step } from "@/lib/measurement-request"
import { advancedIssue, nameIssue, pathSupport, type PathSupport } from "@/lib/measurements-request-form"
import { useCoverageInventory, useMeasurementCoverage, useMeasurementPlans, useRequestProfileHistory } from "@/hooks/use-measurement-data"
import { useMeasurementDocumentSubmission } from "@/hooks/use-measurement-submission"
import { ChoiceGroup, PreflightSection, UnknownOutcome, allLoaded, suffix, useSubmitToast } from "./request-shared"

const stepTitles = ["대상", "조건", "검토"] as const
const stepOf: Record<Step, number> = { workload: 0, device: 0, run: 0, size: 1, advanced: 1, name: 2, review: 2 }
const stepChecks: Step[][] = [["workload", "device", "run"], ["size", "advanced"], ["name"]]

type Props = { namespace: string; bar: React.ReactNode; onDirty: (dirty: boolean) => void }

export function ProfileRequestForm({ namespace, bar, onDirty }: Props) {
  const params = useSearchParams()
  const variantId = params.get("variantId") ?? ""
  const plans = useMeasurementPlans(namespace)
  const models = useChillQuery<ModelList>("v1/models")
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const preprocessors = useChillQuery<{ items: PreprocessorResource[] }>("v1/preprocessors")
  const datasets = useChillQuery<{ items: DatasetResource[] }>("v1/datasets")
  const inventory = useCoverageInventory()
  const profiles = useRequestProfileHistory()
  const coverage = useMeasurementCoverage(namespace, !!variantId)
  const gate = allLoaded([plans, models, workloads, preprocessors, datasets, inventory, profiles, ...(variantId ? [coverage] : [])])
  if (!gate.data || !plans.data || !models.data || !workloads.data || !preprocessors.data || !datasets.data || !inventory.data || !profiles.data) return (
    <Page>
      <PageHeader title="새 측정 요청" />
      {bar}
      <QueryBoundary query={gate} skeleton="block">{() => null}</QueryBoundary>
    </Page>
  )
  const context: Context = {
    models: models.data.items, workloads: workloads.data, classes: inventory.data.classes, plans: plans.data.items ?? [],
    preprocessors: preprocessors.data.items, datasets: datasets.data.items, profiles: profiles.data.profiles ?? [],
  }
  return <Funnel key={`${namespace}?${params}`} namespace={namespace} bar={bar} onDirty={onDirty} context={context} inventory={inventory.data}
    coverage={coverage.data} refetchPlans={plans.refetch} plansFetching={plans.isFetching} />
}

function initialDraft(context: Context, params: URLSearchParams, namespace: string, inventory: InventoryView, coverage?: MeasurementCoverageSummaryResponse) {
  const prefill = resolveMeasurementPrefill(params, namespace, inventory)
  const from = params.get("from")
  const copy = from ? requestCopySource(from, namespace, params.get("fromUID") || params.get("uid"), context.plans) : undefined
  const variantId = params.get("variantId")
  const variant = variantId ? coverage?.cells.flatMap(cell => cell.variants).find(item => item.id === variantId) : undefined
  const source = variant && context.plans.find(plan => variant.requestIds.includes(`${namespace}/${plan.metadata.name}/${plan.metadata.uid}`))
  const target = prefill.active && !prefill.errors.length ? { model: prefill.values.model, deviceClass: prefill.values.deviceClass, node: prefill.node, path: prefill.values.path, powerMode: prefill.values.powerMode } : null
  const notes = [...prefill.errors, ...(copy?.error ? [copy.error] : [])]
  let draft: Draft = emptyDraft, step = 0
  if (source) draft = { ...begin(source.metadata.name, context).draft, ...target, name: null }
  else if (target) {
    // A sole service of the prefilled model is the obvious choice; anything else stays for the user.
    const only = context.workloads.filter(item => item.desired.spec.modelRef.name === target.model)
    const spec = only.length === 1 ? only[0].desired.spec : null
    draft = { ...emptyDraft, ...target, ...(spec ? { workload: only[0].desired.metadata.name, preprocessor: spec.preprocessorRef.name, dataset: spec.datasetRef?.name ?? "" } : {}) }
  } else if (copy?.source) {
    const started = begin(copy.source.metadata.name, context)
    draft = started.draft
    step = Math.min(stepOf[steps[started.index]], 1)
  }
  // The request validator accepts exactly three trials per batch; there is no choice to offer.
  // A prefilled target fixes the model; only its services are offered.
  return { draft: updateDraft({ ...draft, trials: 3 }, {}, context, true), step, notes, model: target?.model ?? "" }
}

function Funnel({ namespace, bar, onDirty, context, inventory, coverage, refetchPlans, plansFetching }: Props & {
  context: Context; inventory: InventoryView; coverage?: MeasurementCoverageSummaryResponse
  refetchPlans: () => Promise<{ error: ApiError | null }>; plansFetching: boolean
}) {
  const params = useSearchParams()
  const variantId = params.get("variantId") ?? ""
  const [initial] = React.useState(() => initialDraft(context, new URLSearchParams(params.toString()), namespace, inventory, coverage))
  const [d, setD] = React.useState(initial.draft)
  const [step, setStep] = React.useState(initial.step)
  const [shown, setShown] = React.useState<boolean[]>([false, false, false])
  const [advancedOpen, setAdvancedOpen] = React.useState(false)
  const [raw, setRaw] = React.useState(false)
  const v = view(d, context)
  const node = v.cls?.nodes.find(item => item.name === d.node)

  const compatibilityPaths = v.model && v.cls && node ? v.model.spec.executionPaths.map(path => `v1/model-compatibility?${new URLSearchParams({ modelSpec: d.model, executionPath: path.name, deviceClass: d.deviceClass, node: d.node })}`) : []
  const compatibility = useChillQueries<ModelCompatibility>(compatibilityPaths, { decoder: parseMeasurementJSON })
  const support = (index: number): PathSupport | undefined => v.model && v.cls ? pathSupport(v.model, v.model.spec.executionPaths[index], v.cls, d.node, compatibility[index]) : undefined
  const selectedIndex = v.model?.spec.executionPaths.findIndex(path => path.name === d.path) ?? -1
  const selectedSupport = selectedIndex >= 0 ? support(selectedIndex) : undefined

  const issueOf = (list: Step[]) => list.find(item => v.problem(item))
  const submission = useMeasurementDocumentSubmission({
    body: manifest(d, context, namespace), namespace, plans: context.plans, refetchPlans, variantId,
    verdict: selectedSupport ? { state: selectedSupport.state, reason: selectedSupport.reason } : undefined,
    validate: () => steps.filter(item => item !== "review").map(item => v.problem(item)).find(Boolean) || "",
    onInvalid: () => {
      const bad = issueOf(steps.filter(item => item !== "review"))
      if (!bad) return
      setStep(stepOf[bad])
      setShown(current => current.map((value, index) => value || index === stepOf[bad]))
      if (bad === "advanced") setAdvancedOpen(true)
    },
    onSubmit: () => setD(current => ({ ...current, name: v.name })),
  })
  const locked = submission.unknown || submission.pending || submission.send.isPending
  useSubmitToast(submission.send)

  const dirty = JSON.stringify(d) !== JSON.stringify(initial.draft) && !submission.send.isSuccess
  React.useEffect(() => { onDirty(dirty); return () => onDirty(false) }, [dirty, onDirty])

  function update(patch: Partial<Draft>) {
    if (locked) return
    setD(current => updateDraft(current, patch, context))
    submission.invalidate()
  }
  function goTo(target: number) {
    if (locked) return
    for (let index = 0; index < target; index++) {
      const bad = issueOf(stepChecks[index])
      if (bad) {
        setStep(index)
        setShown(current => current.map((value, at) => value || at === index))
        if (bad === "advanced") setAdvancedOpen(true)
        return
      }
    }
    setStep(target)
  }

  const show = shown[step]
  const workloadError = show && v.problem("workload") ? (!d.workload ? "선택 필요" : "입력 형식 불일치") : ""
  const preprocessor = context.preprocessors.find(item => item.metadata.name === d.preprocessor)
  const values = v.protocol.values
  const setValues = (next: Advanced) => update({ protocol: { ...v.protocol, values: next } })
  const field = (key: keyof Advanced, label: string, unit?: string) => {
    const value = values[key]
    const issue = show ? advancedIssue(values, key) : ""
    return key === "utilizations"
      ? <InputBlock key={key} label={label} inputSize="sm" inputMode="decimal" placeholder="0.2, 0.5, 0.8" value={values.utilizations} error={issue} disabled={locked} onChange={event => setValues({ ...values, utilizations: event.target.value })} />
      : <InputBlock key={key} label={label} inputSize="sm" type="number" inputMode={key === "sparseMeanGap" ? "decimal" : "numeric"} value={typeof value === "number" && !Number.isNaN(value) ? value : ""} error={issue} disabled={locked}
          rightIcon={unit ? suffix(unit) : undefined} onChange={event => setValues({ ...values, [key]: event.target.valueAsNumber })} />
  }
  const advancedKeys: (keyof Advanced)[] = ["activeWindow", "activeInterval", "activeTimeout", "settling", "idleWindow", "idleInterval", "idleTimeout", "utilizations", "sparseMeanGap", "phaseDuration", "postLoadDuration", "requestLifetime", "deliveryMargin", "framedDecodedRequestUpperBytes"]
  const advancedErrors = advancedKeys.filter(key => advancedIssue(values, key)).length

  const supportBadge = selectedSupport
    ? <StatusBadge tone={selectedSupport.state === "candidate" ? "ok" : selectedSupport.state === "unsupported" ? "fail" : "warn"} label={selectedSupport.state === "candidate" ? "지원" : selectedSupport.state === "unsupported" ? "지원 안 함" : "미확인"} reason={selectedSupport.reason || undefined} />
    : null
  const unknownSupport = selectedSupport?.state === "unknown"
  const nameError = nameIssue(v.name, v.taken)
  const submitted = submission.send.isSuccess
  const lookupHref = submission.found ? requestHref(namespace, submission.found.metadata.name, submission.found.metadata.uid) : undefined

  return (
    <Page>
      <PageHeader title="새 측정 요청" badges={initial.notes.length ? <StatusBadge tone="warn" label="초깃값 무시" reason={initial.notes.join(", ")} /> : undefined}
        more={[{ label: "원본 보기", onSelect: () => setRaw(true) }]} />
      {bar}
      <LeaveGuard when={dirty} />
      <Stepper steps={stepTitles.map(title => ({ id: title, title }))} currentStep={step} onStepChange={goTo} color="bg-primary" className="mx-auto max-w-xl" />

      {step === 0 && <>
        <FormSection title="서비스와 모델">
          <SelectBlock label="서비스" searchable placeholder="선택" disabled={locked} error={workloadError || undefined}
            options={context.workloads.filter(item => !initial.model || item.desired.spec.modelRef.name === initial.model).map(item => ({ value: item.desired.metadata.name, label: item.desired.metadata.name }))}
            value={d.workload} onChange={name => {
              const workload = context.workloads.find(item => item.desired.metadata.name === name)
              if (!workload) return
              const spec = workload.desired.spec
              const model = context.models.find(item => item.metadata.name === spec.modelRef.name)
              update({ workload: name, model: spec.modelRef.name, preprocessor: spec.preprocessorRef.name, dataset: spec.datasetRef?.name ?? "", ...(model?.spec.executionPaths.some(path => path.name === d.path) ? {} : { path: "" }) })
            }} />
          {d.model && <Fields columns={3} items={[
            { label: "모델", value: d.model },
            { label: "전처리기", value: d.preprocessor || null, reason: "서비스에 없음" },
            { label: "데이터셋", value: d.dataset || null, reason: preprocessor ? "서비스에 없음" : undefined },
          ]} />}
        </FormSection>
        <FormSection title="장치">
          <ChoiceGroup label="장치 종류" wide disabled={locked} value={d.deviceClass} error={show && !v.cls ? "선택 필요" : undefined}
            options={context.classes.map(cls => {
              const ready = cls.nodes.some(item => !nodeReason(item))
              const runnable = !v.model || v.model.spec.executionPaths.some(path => !pathReason(v.model!, path, cls))
              return { value: cls.name, label: deviceClassLabel(cls.name), title: cls.name, disabled: !ready || !runnable, reason: !ready ? "준비된 장치 없음" : !runnable ? "실행 경로 없음" : undefined }
            })}
            onChange={name => {
              const cls = context.classes.find(item => item.name === name)
              if (!cls) return
              const keepPath = !!v.model && v.model.spec.executionPaths.some(path => path.name === d.path && !pathReason(v.model!, path, cls))
              update({
                deviceClass: name,
                node: cls.nodes.some(item => item.name === d.node && !nodeReason(item)) ? d.node : cls.nodes.find(item => !nodeReason(item))?.name ?? "",
                powerMode: cls.powerModes.some(mode => mode.name === d.powerMode) ? d.powerMode : cls.powerModes.length === 1 ? cls.powerModes[0].name : "",
                ...(keepPath ? {} : { path: "" }),
              })
            }} />
          {v.cls && v.cls.nodes.length > 1 && <ChoiceGroup label="장치" disabled={locked} value={d.node} error={show && v.problem("device") ? "준비된 장치 선택" : undefined}
            options={v.cls.nodes.map(item => ({ value: item.name, label: item.name, disabled: !!nodeReason(item), reason: item.ready !== "True" ? "준비 안 됨" : item.schedulingDisabled ? "작업 차단" : undefined }))}
            onChange={name => update({ node: name })} />}
          {v.model && v.cls && <ChoiceGroup label="실행 경로" wide disabled={locked} value={d.path} error={show && (!v.path || selectedSupport?.state === "unsupported") ? "지원 경로 선택" : undefined}
            options={v.model.spec.executionPaths.map((path, index) => {
              const state = support(index)
              return { value: path.name, label: executionPathLabel(path.name, v.model), title: path.name, disabled: state?.state === "unsupported", reason: state?.state === "candidate" ? undefined : state?.reason }
            })}
            onChange={name => update({ path: name })} />}
          {v.cls && <ChoiceGroup label="전력 모드" disabled={locked} value={v.powerMode} error={show && !v.cls.powerModes.some(mode => mode.name === v.powerMode) ? (v.cls.powerModes.length ? "선택 필요" : "등록된 모드 없음") : undefined}
            options={v.cls.powerModes.map(mode => ({ value: mode.name, label: mode.name }))}
            onChange={name => update({ powerMode: name })} />}
        </FormSection>
      </>}

      {step === 1 && <>
        <FormSection title="측정 방식" info={v.protocol.from ? `기본값: ${v.protocol.from}` : undefined}>
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            {field("warmup", "예열", "회")}
            {field("activeDuration", "실행 측정", "초")}
            {field("idleDuration", "유휴 측정", "초")}
            {field("retryLimit", "재시도 한도", "회")}
          </div>
        </FormSection>
        <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen} className="rounded-xl border border-border bg-card">
          <CollapsibleTrigger className="flex h-12 w-full items-center gap-2 px-5 text-sm font-semibold">
            고급 값
            {show && advancedErrors > 0 && <StatusBadge tone="fail" label={`확인 ${advancedErrors}`} />}
            <ChevronDown className={cn("ml-auto size-4 text-muted-foreground transition-transform", advancedOpen && "rotate-180")} aria-hidden="true" />
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="grid gap-6 border-t border-border p-5" inert={!advancedOpen}>
              <AdvancedGroup title="실행 전력 기록">
                {field("activeWindow", "기록 시간", "초")}{field("activeInterval", "기록 간격", "ms")}{field("activeTimeout", "대기 한도", "ms")}
              </AdvancedGroup>
              <AdvancedGroup title="유휴 전력 기록">
                {field("settling", "안정 시간", "초")}{field("idleWindow", "기록 시간", "초")}{field("idleInterval", "기록 간격", "ms")}{field("idleTimeout", "대기 한도", "ms")}
              </AdvancedGroup>
              <AdvancedGroup title="운영점">
                {field("utilizations", "이용률")}{field("sparseMeanGap", "희소 요청 간격", "ms")}{field("phaseDuration", "운영점별 측정", "초")}{field("postLoadDuration", "부하 후 기록", "초")}
                {field("requestLifetime", "요청 수명", "초")}{field("deliveryMargin", "전달 여유", "ms")}{field("framedDecodedRequestUpperBytes", "요청 프레임 상한", "B")}
              </AdvancedGroup>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </>}

      {step === 2 && <>
        <FormSection title="요청">
          <InputBlock label="요청 이름" inputSize="sm" maxLength={200} value={v.name} disabled={locked || submitted} error={nameError || undefined}
            onChange={event => update({ name: event.target.value })} />
          <Fields columns={3} items={[
            { label: "서비스", value: d.workload },
            { label: "모델", value: d.model },
            { label: "장치 종류", value: deviceClassLabel(d.deviceClass) },
            { label: "장치", value: d.node },
            { label: "실행 경로", value: d.path ? executionPathLabel(d.path, v.model) : null },
            { label: "전력 모드", value: v.powerMode && powerModeLabel(v.powerMode) },
            { label: "전처리기", value: d.preprocessor },
            { label: "데이터셋", value: d.dataset },
            { label: "배치별 반복", value: `${v.trials}회` },
            { label: "예열", value: Number.isFinite(values.warmup) ? `${values.warmup}회` : null },
            { label: "실행 측정", value: Number.isFinite(values.activeDuration) ? formatSeconds(values.activeDuration) : null },
            { label: "유휴 측정", value: Number.isFinite(values.idleDuration) ? formatSeconds(values.idleDuration) : null },
          ]} />
        </FormSection>
        {submission.unknown
          ? <UnknownOutcome name={v.name} onLookup={submission.lookup} looking={plansFetching} checked={submission.checked} href={lookupHref} />
          : <PreflightSection submission={submission} before={[{ label: "지원", value: supportBadge }]}
              blocked={!!nameError || selectedSupport?.state === "unsupported" || (unknownSupport && !submission.showUnknown)}
              control={unknownSupport && <Label className="font-normal"><Checkbox checked={submission.showUnknown} disabled={locked} onCheckedChange={checked => { submission.setShowUnknown(checked === true); submission.invalidate() }} />미확인 조합 검사</Label>} />}
      </>}

      <FormActions>
        {step > 0 && <Button variant="outline" disabled={locked} onClick={() => setStep(step - 1)}>이전</Button>}
        {step < 2
          ? <Button variant="default" disabled={locked} onClick={() => goTo(step + 1)}>다음</Button>
          : <Button variant="default" loading={submission.send.isPending} disabled={locked || submitted || !!nameError} onClick={submission.submit}>측정 요청</Button>}
      </FormActions>
      <RawDialog open={raw} onOpenChange={setRaw} value={manifest(d, context, namespace)} filename={`${v.name || "measurement-request"}.json`} />
    </Page>
  )
}

function AdvancedGroup({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="grid gap-3">
      <h3 className="text-xs font-medium text-muted-foreground">{title}</h3>
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">{children}</div>
    </section>
  )
}
