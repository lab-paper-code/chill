"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery, useSession } from "@/lib/client"
import type { CampaignResult, MeasurementMutation, MeasurementPlan, MeasurementPlanList, MeasurementPlanSpec } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceExport, SourceJSON, StatusBadge } from "@/components/read-region"
import { Empty, Facts, Figure, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { explain } from "@/lib/labels"

const plansPath = (namespace:string) => `v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
export const planHref = (namespace:string, name:string, uid?:string) => `/measurements/batch-energy-optimizations/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}${uid ? `?uid=${encodeURIComponent(uid)}` : ""}`
const day = (at:string) => new Date(at).toLocaleString("ko-KR", {month:"long", day:"numeric", hour:"2-digit", minute:"2-digit"})
const num = (value:number, digits = 2) => value.toLocaleString("ko-KR", {minimumFractionDigits:digits, maximumFractionDigits:digits})
// Upper bound only: an adaptive search may stop before every batch size is measured.
const plannedTrials = (spec:MeasurementPlanSpec) => spec.batchSizes?.length ? (spec.batchSizes.length + 1) * spec.trialsPerBatch : undefined
// A failed condition's reason as [sentence, next action]; the raw reason stays in 원본.
function planCause(plan:{conditions?:{type:string;status:string;reason?:string}[]}) {
  const failed = plan.conditions?.find(condition => condition.status === "False")
  return failed?.reason ? explain(failed.reason) : undefined
}
function condition(spec:MeasurementPlanSpec) {
  return `${spec.state.targetNode} · ${spec.state.candidate.executionPath}${spec.cpuQuota ? ` · CPU ${spec.cpuQuota}개` : ""}`
}

// /measurements: one row per measurement, grouped by state, newest first.
export function MeasurementPlansPage() {
  const {config} = useSession(), namespace = useSearchParams().get("namespace") ?? config.namespace
  const probe = useChillQuery<MeasurementPlanList>(plansPath(namespace))
  const measuring = (probe.data?.items ?? []).some(plan => plan.status?.phase === "Measuring")
  const query = useChillQuery<MeasurementPlanList>(plansPath(namespace), {poll:measuring})
  const plans = [...(query.data?.items ?? [])].sort((a, b) => b.metadata.creationTimestamp.localeCompare(a.metadata.creationTimestamp))
  const byPhase = (phase:string) => plans.filter(plan => (plan.status?.phase ?? "Measuring") === phase)
  const running = byPhase("Measuring"), failed = byPhase("Failed"), done = plans.filter(plan => plan.status?.phase === "Complete")
  const row = (plan:MeasurementPlan) => {
    const status = plan.status ?? {}
    const hint = status.phase === "Failed" ? planCause(status)?.[0] ?? "측정을 끝내지 못했어요." : status.phase === "Complete" ? `${day(plan.metadata.creationTimestamp)} · ${plan.metadata.name}` : `${status.completedTrials ?? 0}번 쟀어요 · ${day(plan.metadata.creationTimestamp)} 시작`
    return <Row key={plan.metadata.uid} href={planHref(plan.metadata.namespace, plan.metadata.name, plan.metadata.uid)} label={`${plan.spec.state.candidate.modelSpec} · ${condition(plan.spec)}`} hint={hint}
      value={status.phase === "Measuring" ? <StatusBadge tone="info">측정 중</StatusBadge> : status.phase === "Failed" ? <StatusBadge tone="fail">실패</StatusBadge> : null}/>
  }
  return <Screen status={failed.length ? <StatusBadge tone="fail">실패 {failed.length}개</StatusBadge> : undefined}
    title={!query.data ? "측정" : running.length ? `측정 ${running.length}개가 진행 중이에요` : plans.length ? `측정 ${plans.length}개가 있어요` : "아직 요청한 측정이 없어요"}
    lede="모델을 장비에서 실행해 크기별 에너지를 재요. 끝난 측정은 운영 판단이 바로 구성 비교에 써요."
    action={<Button asChild><Link href="/measurements/new">새 측정 요청</Link></Button>}>
    <QueryGate query={query}>
      {running.length > 0 && <Rows label="진행 중">{running.map(row)}</Rows>}
      {failed.length > 0 && <Rows label="끝내지 못한 측정">{failed.map(row)}</Rows>}
      {done.length > 0 && <Rows label="끝난 측정">{done.map(row)}</Rows>}
      {query.data && !plans.length && <Empty title="새 측정을 요청해 보세요"/>}
    </QueryGate>
    <Rows label="더 보기">
      <Row href="/scheduler/profiles" label="측정 프로파일" hint="운영 판단이 쓰는 측정 결과"/>
      <Row href="/measurements/runs" label="개별 측정 회차" hint="회차 하나하나의 결과와 근거"/>
      <Row href={`/measurements/operation-cost-measurements?namespace=${encodeURIComponent(namespace)}`} label="동작 비용 측정" hint="순서 있는 동작 조건과 단계별 저장 결과"/>
      <Row href={`/measurements/dispatch-occupancy-measurements?namespace=${encodeURIComponent(namespace)}`} label="분배 점유시간 측정" hint="요청당 점유시간 d와 지속 완료 처리량"/>
      <Row href="/measurements/check" label="측정 계획 파일 검사" hint="파일로 된 계획의 조건만 확인해요"/>
      {query.data && <RowDialog label="원본 자료" title="측정 목록 원본"><SourceExport value={{namespace, source:query.data}} filename="chill-measurements.json"/><SourceJSON inline value={query.data}/></RowDialog>}
    </Rows>
  </Screen>
}

type ResultDetail = {bestBatch?:number;minimumIncrementalWorkJoulesPerItem?:number;incrementalWorkByBatch?:{batch:number;incrementalWorkJoulesPerItem:number}[];completionReason?:string;trialsPerBatch?:number}
type ResultBody = {detail?:ResultDetail;completedTrials?:{run:{namespace:string;name:string;uid:string};measurement:{workloadMode:string;batch?:number}}[]}

// /measurements/batch-energy-optimizations/[ns]/[name]: one measurement, shaped by its state.
export function MeasurementPlanPage({namespace, name}:{namespace:string;name:string}) {
  const expectedUID = useSearchParams().get("uid")
  const path = `${plansPath(namespace)}/${encodeURIComponent(name)}/result`
  const probe = useChillQuery<CampaignResult>(path, {decoder:parseSchedulerJSON})
  const query = useChillQuery<CampaignResult>(path, {decoder:parseSchedulerJSON, poll:probe.data?.campaign.phase === "Measuring"})
  const [deleted, setDeleted] = React.useState<MeasurementMutation|null>(null)
  const data = query.data, campaign = data?.campaign, spec = campaign?.spec as MeasurementPlanSpec | undefined
  const other = !!campaign && !!expectedUID && campaign.uid !== expectedUID
  const back:[string,string] = ["/measurements", "측정"]
  if (deleted) return <Screen back={back} status={<StatusBadge tone="ok">삭제됨</StatusBadge>} title="측정을 지웠어요"
    lede={`${name} · 결과와 근거 파일 ${deleted.deletedEvidence?.length ?? 0}개를 함께 지웠어요.`} action={<Button asChild><Link href="/measurements">측정 목록으로</Link></Button>}>
    <Rows><RowDialog label="지운 파일" value={`${deleted.deletedEvidence?.length ?? 0}개`} title="지운 근거 파일"><SourceJSON inline value={deleted}/></RowDialog></Rows>
  </Screen>
  const phase = campaign?.phase, cause = campaign ? planCause(campaign) : undefined
  const result = (data?.result ?? null) as ResultBody | null, detail = result?.detail
  const planned = spec ? plannedTrials(spec) : undefined
  const title = !campaign ? name : other ? "같은 이름으로 다시 등록된 다른 측정이에요"
    : phase === "Measuring" ? "측정하고 있어요"
    : phase === "Failed" ? "측정을 끝내지 못했어요"
    : detail?.bestBatch !== undefined ? `한 번에 ${detail.bestBatch}건씩 보낼 때 에너지가 가장 적어요` : "측정이 끝났어요"
  const badge = !campaign ? undefined : other ? <StatusBadge tone="fail">다른 측정</StatusBadge>
    : phase === "Measuring" ? <StatusBadge tone="info">측정 중</StatusBadge> : phase === "Failed" ? <StatusBadge tone="fail">실패</StatusBadge> : <StatusBadge tone="ok">완료</StatusBadge>
  const manage = campaign && <Rows label="관리"><DeleteMeasurement namespace={namespace} name={name} uid={campaign.uid} version={campaign.resourceVersion} trials={campaign.completedTrials ?? 0}
    blocked={phase === "Measuring" ? "진행 중인 측정은 지울 수 없어요. 끝난 뒤에 지워 주세요." : undefined} onDeleted={setDeleted}/></Rows>
  const scale = Math.max(...(detail?.incrementalWorkByBatch ?? []).map(item => item.incrementalWorkJoulesPerItem), 0)
  return <Screen back={back} status={badge} title={title}
    lede={spec && !other ? `${spec.state.candidate.modelSpec} · ${condition(spec)}` : name}
    action={campaign && !other && phase === "Failed" ? <Button asChild><Link href={`/measurements/new?from=${encodeURIComponent(name)}`}>같은 조건으로 다시 요청</Link></Button> : undefined}>
    <QueryGate query={query}>{campaign && spec && !other && <>
      {phase === "Measuring" && <Figure label="지금까지 잰 회차" value={campaign.completedTrials ?? 0} unit={planned ? `/ 최대 ${planned}번` : "번"} note="회차마다 끝나면 자동으로 갱신돼요."/>}
      {phase === "Failed" && <p className="text-muted-foreground">{cause?.[0] ?? "측정을 끝내지 못했어요."} 지금까지 {campaign.completedTrials ?? 0}번 쟀어요.{cause?.[1] && ` ${cause[1]}`}</p>}
      {phase === "Complete" && detail?.minimumIncrementalWorkJoulesPerItem !== undefined && <Figure label="요청 1건에 더 드는 에너지" value={num(detail.minimumIncrementalWorkJoulesPerItem)} unit="J" note={campaign.outcome ? explain(campaign.outcome)[0] : undefined}/>}
      {phase === "Complete" && !!detail?.incrementalWorkByBatch?.length && <section className="rows-group" aria-label="크기별 에너지"><h2 className="rows-label">크기별 요청 1건에 더 드는 에너지</h2>
        <dl className="energy-rows">{detail.incrementalWorkByBatch.map(item => <div key={item.batch} className="energy-row"><dt>한 번에 {item.batch}건</dt>
          <div className="energy-track" aria-hidden="true"><div className="energy-fill" data-hot={item.batch !== detail.bestBatch} style={{width:`${scale ? Math.max(item.incrementalWorkJoulesPerItem / scale * 100, 2) : 0}%`}}/></div>
          <dd>{num(item.incrementalWorkJoulesPerItem)} J</dd></div>)}</dl>
        <p className="px-1 text-sm text-muted-foreground">유휴 상태 전력을 뺀, 요청 처리로 늘어난 에너지예요. 지금 운영 중 소비와는 달라요.</p></section>}
      <Rows label="자세히">
        <RowDialog label="측정 조건" title="측정 조건" value={`${spec.batchSizes?.length ? spec.batchSizes.join("·") + "건" : "자동"} · ${spec.trialsPerBatch}번씩`}>
          <Facts items={[["모델", spec.state.candidate.modelSpec], ["장비", `${spec.state.targetNode} · ${spec.state.candidate.deviceClass}`], ["실행 방식", spec.state.candidate.executionPath], ["전력 모드", spec.state.candidate.powerMode],
            ["한 번에 보내는 건수", spec.batchSizes?.length ? spec.batchSizes.map(size => `${size}건`).join(", ") : "자동으로 정해요"], ["크기마다 반복", `${spec.trialsPerBatch}번`], ["CPU", spec.cpuQuota ? `${spec.cpuQuota}개${spec.cpuAffinity?.length ? ` (코어 ${spec.cpuAffinity.join(", ")})` : ""}` : "제한 없음"],
            ["한 회차 측정 시간", `${spec.activeProtocol.durationSeconds ?? "-"}초`], ["실패 시 재시도", `${spec.retryLimit}번`]]}/>
        </RowDialog>
        {!!result?.completedTrials?.length && <RowDialog label="회차별 측정" value={`${result.completedTrials.length}번`} title="회차별 측정" description="회차를 누르면 그 회차의 지연과 전력을 볼 수 있어요.">
          <ul className="rows">{result.completedTrials.map(trial => <li key={trial.run.uid}><Link className="row" href={`/measurements/profiling-runs/${encodeURIComponent(trial.run.namespace)}/${encodeURIComponent(trial.run.name)}?uid=${encodeURIComponent(trial.run.uid)}`}>
            <span className="row-main"><span className="row-text">{trial.measurement.workloadMode === "idle" ? "유휴 기준 전력" : `한 번에 ${trial.measurement.batch}건`}</span><span className="row-hint">{trial.run.name}</span></span></Link></li>)}</ul>
        </RowDialog>}
        {phase !== "Complete" && <Row href={`/measurements/runs?q=${encodeURIComponent(name)}`} label="지금까지의 회차" hint="이 측정 이름으로 찾은 회차"/>}
        <RowDialog label="원본 자료" title="측정 원본 자료"><SourceExport value={{namespace, name, expectedUID, source:data}} filename={`chill-measurement-${name}.json`}/><SourceJSON inline value={data}/></RowDialog>
      </Rows>
      {manage}
    </>}</QueryGate>
  </Screen>
}

// Permanent: ChillServer deletes the result, curve plan, serving bundle and per-trial evidence it created, then the native object.
function DeleteMeasurement({namespace, name, uid, version, trials, blocked, onDeleted}:{namespace:string;name:string;uid:string;version:string;trials:number;blocked?:string;onDeleted:(result:MeasurementMutation)=>void}) {
  const mutation = useChillMutation(), client = useQueryClient()
  const [open, setOpen] = React.useState(false), [understood, setUnderstood] = React.useState(false), [unknown, setUnknown] = React.useState(false)
  const list = useChillQuery<MeasurementPlanList>(plansPath(namespace), {enabled:unknown})
  const remaining = unknown && list.data ? (list.data.items ?? []).find(plan => plan.metadata.name === name) : undefined
  async function remove() {
    setUnknown(false)
    try {
      const response = await mutation.mutateAsync({path:`${plansPath(namespace)}/${encodeURIComponent(name)}?uid=${encodeURIComponent(uid)}`, method:"DELETE", version})
      await client.invalidateQueries({predicate:q => typeof q.queryKey[1] === "string" && q.queryKey[1].startsWith(plansPath(namespace))})
      onDeleted(parseSchedulerJSON(response.body) as MeasurementMutation)
    } catch (error) {
      const code = error instanceof ApiError && error.data && typeof error.data === "object" && "code" in error.data ? String(error.data.code) : ""
      if (error instanceof ApiError && (error.unknownOutcome || error.status === 0 || code.endsWith("Incomplete"))) setUnknown(true)
    } finally { setOpen(false); setUnderstood(false) }
  }
  const error = mutation.error && !unknown ? mutation.error : null
  const code = error?.data && typeof error.data === "object" && "code" in error.data ? String(error.data.code) : undefined
  return <>
    <Row onClick={() => setOpen(true)} label="측정 삭제" hint={blocked ?? "결과와 근거 파일까지 영구히 지워요"}/>
    {unknown && <li className="p-4"><div role="alert" className="notice"><p>삭제가 끝났는지 확인하지 못했어요. 일부 근거 파일만 지워졌을 수도 있어요. 다시 지우기 전에 지금 상태를 확인해 주세요.</p>
      {list.data ? <p>{remaining ? (remaining.metadata.uid === uid ? "측정이 아직 남아 있어요. 화면을 새로 열어 최신 상태로 다시 시도해 주세요." : "같은 이름의 다른 측정이 있어요.") : "측정이 목록에서 사라졌어요. 삭제됐어요."}</p>
        : <Button variant="outline" onClick={() => void list.refetch()}>지금 상태 확인하기</Button>}</div></li>}
    {error && <li className="p-4"><div role="alert" className="notice"><p>{error.status === 403 ? "측정을 지울 권한이 없어요."
      : code === "MeasurementInUse" ? "운영 판단이나 실행 중인 서비스가 아직 이 측정을 쓰고 있어서 지울 수 없어요."
      : error.status === 409 ? "그 사이 측정이 바뀌었어요. 화면을 새로 열어 최신 상태로 다시 시도해 주세요."
      : "삭제 요청을 처리하지 못했어요."}</p><SourceJSON value={error.data ?? error.body} title="문제 자세히 보기"/></div></li>}
    <Dialog open={open} onOpenChange={setOpen}><DialogContent>
      <DialogHeader><DialogTitle>{blocked ? "지금은 지울 수 없어요" : "이 측정을 영구히 지울까요?"}</DialogTitle><DialogDescription>{blocked ?? "지운 측정은 되돌릴 수 없어요."}</DialogDescription></DialogHeader>
      {!blocked && <>
        <Facts items={[["측정", name], ["지워지는 것", `측정 기록, 회차 ${trials}개, 결과와 근거 파일`]]}/>
        <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={understood} onChange={event => setUnderstood(event.target.checked)}/>근거 파일까지 영구히 지워지고 되돌릴 수 없다는 걸 확인했어요.</label>
        <Button variant="destructive" disabled={!understood || mutation.isPending} onClick={() => void remove()}>{mutation.isPending ? "지우는 중" : "영구 삭제하기"}</Button>
      </>}
    </DialogContent></Dialog>
  </>
}
