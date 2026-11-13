"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Button, Fields, FormActions, FormSection, LeaveGuard, Page, PageHeader, QueryBoundary, RawDialog, Section, StatusBadge, type FieldItem } from "@/components/kit"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { SelectBlock } from "@/components/custom/blocks/SelectBlock"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { decodeResponse } from "@/lib/client-transport"
import type { WorkloadSummary } from "@/lib/domain"
import type { InventoryView, MeasurementCoverageSummaryResponse, MeasurementPlan, MeasurementProjectionList, OccupancyMutation, OccupancyRequest, OccupancyTask } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { coverageProgress, type CoverageModel } from "@/lib/measurement-coverage"
import { deviceClassLabel, executionPathLabel, powerModeLabel } from "@/lib/display-labels"
import { formatSeconds } from "@/lib/format"
import { occupancyBody, occupancyChecked, occupancyResponseState, occupancyResultHref } from "@/lib/dispatch-occupancy"
import { nameIssue } from "@/lib/measurements-request-form"
import { useCoverageInventory, useMeasurementCoverage, useMeasurementPlans } from "@/hooks/use-measurement-data"
import { UnknownOutcome, allLoaded, lostResponse, suffix, useSubmitToast } from "./request-shared"

type Props = { namespace: string; bar: React.ReactNode; onDirty: (dirty: boolean) => void }
const planId = (plan: MeasurementPlan) => `${plan.metadata.namespace}/${plan.metadata.name}/${plan.metadata.uid}`

export function OccupancyRequestForm({ namespace, bar, onDirty }: Props) {
  const params = useSearchParams()
  const plans = useMeasurementPlans(namespace)
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const inventory = useCoverageInventory()
  const coverage = useMeasurementCoverage(namespace)
  const gate = allLoaded([plans, workloads, inventory, coverage])
  if (!gate.data || !plans.data || !workloads.data || !inventory.data || !coverage.data) return (
    <Page>
      <PageHeader title="새 측정 요청" />
      {bar}
      <QueryBoundary query={gate} skeleton="block">{() => null}</QueryBoundary>
    </Page>
  )
  return <OccupancyForm key={`${namespace}?${params}`} namespace={namespace} bar={bar} onDirty={onDirty} plans={plans.data.items ?? []} workloads={workloads.data} inventory={inventory.data} coverage={coverage.data} />
}

function OccupancyForm({ namespace, bar, onDirty, plans, workloads, inventory, coverage }: Props & { plans: MeasurementPlan[]; workloads: WorkloadSummary[]; inventory: InventoryView; coverage: MeasurementCoverageSummaryResponse }) {
  const params = useSearchParams(), router = useRouter()
  const dry = useChillMutation(), send = useChillMutation()
  const [selection, setSelection] = React.useState<{ plan?: string; workload?: string; batch?: string; name?: string }>({})
  const [checked, setChecked] = React.useState<{ key: string; body: OccupancyRequest } | null>(null)
  const [response, setResponse] = React.useState<OccupancyMutation>()
  const [shown, setShown] = React.useState(false)
  const [unknown, setUnknown] = React.useState(false)
  const [raw, setRaw] = React.useState(false)
  const listPath = `v1/namespaces/${encodeURIComponent(namespace)}/dispatch-occupancy-measurements`
  const records = useChillQuery<MeasurementProjectionList>(listPath, { enabled: unknown, decoder: parseSchedulerJSON })
  useSubmitToast(send, error => `${occupancyResponseState((error.data as { code?: string } | null)?.code)} HTTP ${error.status}`)

  // Only the current profile of a measured cell can anchor an occupancy measurement.
  const cellById = new Map(coverage.cells.flatMap(cell => cell.currentMeasurement ? [[cell.currentMeasurement.id, cell] as const] : []))
  const filter = (key: string, value: string) => !params.get(key) || params.get(key) === value
  const candidates = plans.filter(plan => {
    const c = plan.spec.state.candidate
    return cellById.has(planId(plan)) && filter("model", c.modelSpec) && filter("deviceClass", c.deviceClass) && filter("path", c.executionPath) && filter("powerMode", c.powerMode)
  }).sort((a, b) => a.metadata.name.localeCompare(b.metadata.name))
  const profileID = params.get("profileID")
  const measurementID = params.get("measurementID") ?? (profileID ? coverage.cells.find(cell => cell.currentMeasurement?.id === profileID || cell.variants.some(variant => variant.profileIds.includes(profileID)))?.currentMeasurement?.id : undefined)
  const pinned = measurementID ? candidates.find(plan => planId(plan) === measurementID) : undefined
  const initialPlan = params.get("batchEnergyOptimization") ?? pinned?.metadata.name ?? (candidates.length === 1 ? candidates[0].metadata.name : "")
  const planName = selection.plan ?? initialPlan
  const plan = candidates.find(item => item.metadata.name === planName)
  const cell = plan ? cellById.get(planId(plan)) : undefined
  const nodeName = plan?.spec.state.targetNode ?? ""
  const node = inventory.classes.find(cls => cls.name === plan?.spec.state.candidate.deviceClass)?.nodes.find(item => item.name === nodeName)
  const workloadChoices = workloads.filter(item => !plan || item.desired.spec.modelRef.name === plan.spec.state.candidate.modelSpec)
  const workloadName = selection.workload ?? params.get("workload") ?? workloadChoices.find(item => item.desired.metadata.uid === params.get("workloadUID"))?.desired.metadata.name ?? (workloadChoices.length === 1 ? workloadChoices[0].desired.metadata.name : "")
  const workload = workloadChoices.find(item => item.desired.metadata.name === workloadName)
  const batchText = selection.batch ?? params.get("batch") ?? ""
  const batch = Number(batchText)
  const name = selection.name ?? ""
  const body = occupancyBody({ name, node: nodeName, batchEnergyOptimization: planName, workload: workloadName, batch })
  const key = JSON.stringify({ body, planUID: plan?.metadata.uid, planRV: plan?.metadata.resourceVersion, nodeUID: node?.uid, nodeRV: node?.resourceVersion, workloadUID: workload?.desired.metadata.uid, workloadRV: workload?.desired.metadata.resourceVersion })
  const passed = occupancyChecked(key, checked?.key ?? null, response) && (!name || response?.task?.metadata.name === name)
  const frozenName = response?.task?.metadata.name
  const prefillMismatch = (!!(params.get("measurementID") || profileID) && !pinned) || (!!params.get("node") && !!plan && params.get("node") !== nodeName) || (!!params.get("nodeUID") && !!node && node.uid !== params.get("nodeUID"))
  const nodeBlocked = !!node && (node.ready !== "True" || node.schedulingDisabled)
  const batchValid = /^\d+$/.test(batchText) && Number.isSafeInteger(batch) && batch >= 1 && batch <= 2147483647
  const errors = { plan: !plan ? "선택 필요" : !node ? "장치 없음" : "", node: nodeBlocked ? "준비 안 됨" : "", workload: !workload ? "선택 필요" : "", batch: batchValid ? "" : "1 이상 정수", name: nameIssue(name, undefined, false) }
  const invalid = Object.values(errors).some(Boolean)
  const locked = dry.isPending || send.isPending || unknown || send.isSuccess
  const dirty = Object.values(selection).some(value => value !== undefined) && !send.isSuccess
  React.useEffect(() => { onDirty(dirty); return () => onDirty(false) }, [dirty, onDirty])

  function update(patch: typeof selection) {
    if (locked) return
    setSelection(current => ({ ...current, ...patch }))
    setChecked(null); setResponse(undefined); dry.reset(); send.reset()
  }
  function verify() {
    setShown(true)
    if (invalid || locked) return
    setChecked({ key, body }); setResponse(undefined)
    dry.mutate({ path: `${listPath}?dryRun=All`, method: "POST", body }, {
      onSuccess: result => {
        try { setResponse(decodeResponse(result.body, parseSchedulerJSON) as OccupancyMutation) }
        catch { setChecked(null) }
      },
    })
  }
  function submit() {
    if (!passed || !checked || locked || invalid || !frozenName) return
    send.mutate({ path: listPath, method: "POST", body: occupancyBody({ ...checked.body, name: frozenName }) }, {
      onSuccess: result => {
        const accepted = result.data as OccupancyMutation
        if (accepted.request === "Accepted" && accepted.task?.metadata.name) router.replace(occupancyResultHref(namespace, accepted.task.metadata.name, accepted.task.metadata.uid))
        else if (accepted.request !== "NOT_SUBMITTED") setUnknown(true)
      },
      onError: error => { if (lostResponse(error)) setUnknown(true) },
    })
  }

  const label = (item: MeasurementPlan) => {
    const c = item.spec.state.candidate
    const itemModel = inventory.models.find(entry => entry.name === c.modelSpec) as CoverageModel | undefined
    return `${c.modelSpec} / ${deviceClassLabel(c.deviceClass)} ${powerModeLabel(c.powerMode)} / ${executionPathLabel(c.executionPath, itemModel)} / ${item.spec.state.targetNode}`
  }
  const measured = cell ? coverageProgress(cell)?.measured ?? [] : []
  const dryCode = (dry.error?.data as { code?: string } | null | undefined)?.code
  const found = records.data?.items?.find(item => item.metadata.name === frozenName)

  return (
    <Page>
      <PageHeader title="새 측정 요청" badges={prefillMismatch ? <StatusBadge tone="warn" label="초깃값 불일치" reason="링크의 측정 조합을 찾지 못함" /> : undefined}
        more={response ? [{ label: "원본 보기", onSelect: () => setRaw(true) }] : undefined} />
      {bar}
      <LeaveGuard when={dirty} />
      <FormSection title="대상">
        <SelectBlock label="측정된 조합" searchable placeholder={candidates.length ? "선택" : "측정된 조합 없음"} disabled={locked || !candidates.length}
          error={shown && errors.plan ? errors.plan : undefined} value={planName}
          options={candidates.map(item => ({ value: item.metadata.name, label: label(item) })).sort((a, b) => a.label.localeCompare(b.label))}
          onChange={value => update({ plan: value, workload: undefined })} />
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <InputBlock label="장치" readOnly placeholder="—" value={nodeName} error={errors.node || undefined} />
          <SelectBlock label="서비스" placeholder={workloadChoices.length ? "선택" : "서비스 없음"} disabled={locked || !workloadChoices.length}
            error={shown && errors.workload ? errors.workload : undefined} value={workloadName}
            options={workloadChoices.map(item => ({ value: item.desired.metadata.name, label: item.desired.metadata.name }))}
            onChange={value => update({ workload: value })} />
          <InputBlock label="배치" type="number" inputMode="numeric" min={1} step={1} value={batchText} disabled={locked} rightIcon={suffix("건")}
            suggestions={measured.map(String)} error={shown && errors.batch ? errors.batch : undefined}
            onChange={event => update({ batch: event.target.value })} />
          <InputBlock label="이름" placeholder="자동" maxLength={200} value={name} disabled={locked} error={errors.name || undefined}
            onChange={event => update({ name: event.target.value })} />
        </div>
      </FormSection>

      {unknown
        ? <UnknownOutcome name={frozenName ?? name} onLookup={() => void records.refetch()} looking={records.isFetching} checked={records.isSuccess}
            href={found ? occupancyResultHref(namespace, found.metadata.name, found.metadata.uid) : undefined} />
        : (dry.isPending || dry.error || response) && <Section title="사전 검사" card actions={
            dry.isPending ? <StatusBadge tone="progress" label="검사 중" />
            : dry.error ? <StatusBadge tone="fail" label={dry.error.status ? occupancyResponseState(dryCode) : "응답 없음"} reason={dry.error.status ? `HTTP ${dry.error.status}` : undefined} />
            : passed ? <StatusBadge tone="ok" label="통과" /> : <StatusBadge tone="fail" label={occupancyResponseState(undefined, response?.request)} />}>
            {passed && response?.task ? <Fields columns={3} items={protocolFields(response.task)} /> : null}
          </Section>}

      <FormActions>
        <Button variant="outline" loading={dry.isPending} disabled={locked} onClick={verify}>사전 검사</Button>
        <Button variant="default" loading={send.isPending} disabled={!passed || locked || invalid} disabledReason={!passed && !locked ? "사전 검사 통과 필요" : undefined} onClick={submit}>측정 요청</Button>
      </FormActions>
      {response && <RawDialog open={raw} onOpenChange={setRaw} value={response} filename={`${frozenName ?? "occupancy"}-check.json`} title="사전 검사 원본" />}
    </Page>
  )
}

const supplyModes: Record<string, string> = { "closed-loop": "닫힌 루프", "short-burst": "짧은 버스트" }
const seconds = (value?: number) => value == null ? null : formatSeconds(value)

/** The server-frozen protocol of a checked request; read-only. */
function protocolFields(task: OccupancyTask): FieldItem[] {
  const spec = task.spec
  const supply = spec.supplyMode ? supplyModes[spec.supplyMode] ?? null : null
  const common: FieldItem[] = [
    { label: "요청 이름", value: task.metadata.name },
    { label: "배치", value: `${spec.batch}건` },
    { label: "공급 방식", value: supply, reason: spec.supplyMode ? `코드 ${spec.supplyMode}` : "미제공" },
    { label: "예열", value: seconds(spec.warmupSeconds), reason: "미제공" },
    { label: "측정 구간", value: seconds(spec.durationSeconds), reason: "미제공" },
    { label: "동시 송신자", value: spec.workers == null ? null : `${spec.workers}명`, reason: "미제공" },
    { label: "요청 수명", value: seconds(spec.requestLifetimeSeconds), reason: "미제공" },
    { label: "드레인", value: seconds(spec.drainSeconds), reason: "미제공" },
  ]
  if (spec.minimumPairs !== undefined && spec.minimumPairs > 0) return [...common, { label: "최소 쌍", value: `${spec.minimumPairs}개` }]
  return [...common,
    { label: "최소 배치 표본", value: spec.minimumBatches == null ? null : `${spec.minimumBatches}개`, reason: "미제공" },
    { label: "최대 유휴 비율", value: spec.maximumIdleFraction || null, reason: "미제공" },
    { label: "계획 경과", value: seconds(spec.plannedElapsedSeconds), reason: "미제공" },
  ]
}
