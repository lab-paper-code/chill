"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery, useSession } from "@/lib/client"
import type { CampaignResult, MeasurementMutation, MeasurementPlanList, MeasurementPlanSpec, Passport, ProfilesView } from "@/lib/read-types"
import { safeMeasurementReturn, significant } from "@/lib/measurements"
import { planVariant, profileForPlan, relatedPlans } from "@/lib/measurement-coverage"
import { DataTable } from "@/components/data-table"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceExport, SourceJSON, StatusBadge } from "@/components/read-region"
import { Empty, Facts, Figure, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { explain } from "@/lib/labels"

const plansPath = (namespace:string) => `v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
export const planHref = (namespace:string, name:string, uid?:string) => `/measurements/batch-energy-optimizations/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}${uid ? `?uid=${encodeURIComponent(uid)}` : ""}`
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


type ResultDetail = {bestBatch?:number;minimumIncrementalWorkJoulesPerItem?:number;incrementalWorkByBatch?:{batch:number;incrementalWorkJoulesPerItem:number}[];completionReason?:string;trialsPerBatch?:number}
type ResultBody = {detail?:ResultDetail;completedTrials?:{run:{namespace:string;name:string;uid:string};measurement:{workloadMode:string;batch?:number}}[];excludedTrials?:{run:{namespace:string;name:string;uid:string};reasons?:string[]}[]}

// /measurements/batch-energy-optimizations/[ns]/[name]: one measurement, shaped by its state.
export function MeasurementPlanPage({namespace, name}:{namespace:string;name:string}) {
  const params=useSearchParams(), expectedUID = params.get("uid")
  const path = `${plansPath(namespace)}/${encodeURIComponent(name)}/result`
  const probe = useChillQuery<CampaignResult>(path, {decoder:parseSchedulerJSON})
  const query = useChillQuery<CampaignResult>(path, {decoder:parseSchedulerJSON, poll:probe.data?.campaign.phase === "Measuring"})
  const [deleted, setDeleted] = React.useState<MeasurementMutation|null>(null)
  const data = query.data, campaign = data?.campaign, spec = campaign?.spec as MeasurementPlanSpec | undefined
  const plans=useChillQuery<MeasurementPlanList>(plansPath(namespace),{enabled:campaign?.phase==="Failed"})
  const profiles=useChillQuery<ProfilesView>("v1/scheduler/profiles",{enabled:campaign?.phase==="Failed"})
  const original=plans.data?.items?.find(plan=>plan.metadata.uid===campaign?.uid)
  const successor=original ? plans.data?.items?.filter(plan=>plan.metadata.uid!==original.metadata.uid&&plan.metadata.creationTimestamp>original.metadata.creationTimestamp&&planVariant(plan.spec)===planVariant(original.spec)&&profileForPlan(plan,profiles.data?.profiles??[]).length).sort((a,b)=>b.metadata.creationTimestamp.localeCompare(a.metadata.creationTimestamp))[0] : undefined
  const related=original?relatedPlans(original,plans.data?.items??[]).filter(item=>!item.sameRequestedCondition):[]
  const other = !!campaign && !!expectedUID && campaign.uid !== expectedUID
  const back:[string,string] = [safeMeasurementReturn(params.get("returnTo")), "측정으로 돌아가기"]
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
    : detail?.bestBatch !== undefined ? (detail.incrementalWorkByBatch?.length===1 ? `배치 ${detail.bestBatch} 측정 결과 · 다른 배치와 비교하지 않았어요` : `측정한 배치 중 에너지 평균이 가장 낮은 배치 ${detail.bestBatch}`) : "측정이 끝났어요"
  const badge = !campaign ? undefined : other ? <StatusBadge tone="fail">다른 측정</StatusBadge>
    : phase === "Measuring" ? <StatusBadge tone="info">측정 중</StatusBadge> : phase === "Failed" ? <StatusBadge tone="fail">실패</StatusBadge> : <StatusBadge tone="ok">완료</StatusBadge>
  const manage = campaign && <Rows label="관리"><DeleteMeasurement namespace={namespace} name={name} uid={campaign.uid} version={campaign.resourceVersion} trials={campaign.completedTrials ?? 0}
    blocked={phase === "Measuring" ? "진행 중인 측정은 지울 수 없어요. 끝난 뒤에 지워 주세요." : undefined} onDeleted={setDeleted}/></Rows>
  const scale = Math.max(...(detail?.incrementalWorkByBatch ?? []).map(item => item.incrementalWorkJoulesPerItem), 0)
  return <Screen width="data" actionPlacement="header" breadcrumbs={[{href:"/measurements",label:"측정"},{href:back[0],label:"상위 목록"},{label:name}]} back={back} status={badge} title={title}
    lede={spec && !other ? `${spec.state.candidate.modelSpec} · ${condition(spec)}` : name}
    action={campaign && !other && phase === "Failed" ? <div className="flex flex-wrap gap-2">{successor&&<Button asChild><Link href={planHref(namespace,successor.metadata.name,successor.metadata.uid)}>후속 측정 결과 보기</Link></Button>}<Button variant={successor?"outline":"default"} asChild><Link href={`/measurements/new?${new URLSearchParams({from:name,fromNamespace:namespace,fromUID:campaign.uid})}`}>조건을 복사해 새 이름으로 요청</Link></Button></div> : undefined}>
    <QueryGate query={query}>{campaign && spec && !other && <>
      {phase === "Measuring" && <Figure label="지금까지 잰 회차" value={campaign.completedTrials ?? 0} unit={planned ? `/ 최대 ${planned}번` : "번"} note="회차마다 끝나면 자동으로 갱신돼요."/>}
      {phase === "Failed" && <div className="notice"><p>{campaign.conditions?.some(c=>c.reason==="RetryLimitExhausted")?"재시도 한도에 도달했어요 · 상세 원인 미제공":cause?.[0]??"측정을 끝내지 못했어요."} 완료 회차 {campaign.completedTrials ?? 0}개.</p>{campaign.conditions?.map((c,i)=><p key={i}>{c.type}: {c.status} · {c.reason} · {c.message}</p>)}{successor?<p>재측정됨 · 요청 조건이 같은 더 늦은 측정의 catalog 결과가 있어요. 자동 대체를 뜻하지 않아요.</p>:<p>조건을 복사해 새 이름으로 요청할 수 있어요. 기존 요청을 수정하거나 즉시 제출하지 않아요.</p>}</div>}
      {phase==="Failed"&&related.length>0&&<Rows label="관련 후보 · 요청 조건이 달라 후속 유효 결과로 단정하지 않아요">{related.map(({plan})=><Row key={plan.metadata.uid} href={planHref(namespace,plan.metadata.name,plan.metadata.uid)} label={plan.metadata.name} hint={`${plan.status?.phase??"상태 미제공"} · CPU ${plan.spec.cpuQuota??"미제공"} · dataset ${plan.spec.activeProtocol.dataset??"미제공"} · 측정 ${plan.spec.activeProtocol.durationSeconds??"미제공"} s`}/>)}</Rows>}
      {phase === "Complete" && detail?.minimumIncrementalWorkJoulesPerItem !== undefined && <Figure label="요청 1건에 더 드는 에너지 평균" value={significant(detail.minimumIncrementalWorkJoulesPerItem)} unit="J/건" note="에너지 CI 미제공"/>}
      {phase === "Complete" && !!detail?.incrementalWorkByBatch?.length && <section className="rows-group" aria-label="크기별 에너지"><h2 className="rows-label">크기별 요청 1건에 더 드는 에너지</h2>
        <dl className="energy-rows">{detail.incrementalWorkByBatch.map(item => <div key={item.batch} className="energy-row"><dt>한 번에 {item.batch}건</dt>
          <div className="energy-track" aria-hidden="true"><div className="energy-fill" data-hot={item.batch !== detail.bestBatch} style={{width:`${scale ? Math.max(item.incrementalWorkJoulesPerItem / scale * 100, 2) : 0}%`}}/></div>
          <dd className="tabular-nums">{significant(item.incrementalWorkJoulesPerItem)} J/건</dd></div>)}</dl>
        <p className="px-1 text-sm text-muted-foreground">유휴 상태 전력을 뺀, 요청 처리로 늘어난 에너지예요. 지금 운영 중 소비와는 달라요.</p></section>}
      {!!result?.completedTrials?.length&&<ActiveLatency trials={result.completedTrials}/>}
      {!!detail&&<Facts label="반복과 범위" items={[["반복",`${detail.trialsPerBatch??"미제공"}회/배치 · 지연 표본 수 N과 달라요`],["측정한 배치",detail.incrementalWorkByBatch?.map(item=>item.batch).join(", ")??"미제공"],["에너지 오차","에너지 CI 미제공 · 유휴 W CI를 에너지 오차로 사용하지 않아요"]]}/>}
      <Facts label="측정 정책·검증 범위" items={[["active 구간",`${spec.activeProtocol.durationSeconds??"미제공"} s · 예열 ${spec.activeProtocol.warmupIterations??"미제공"}회`],["idle 구간",`${spec.idleProtocol.durationSeconds??"미제공"} s`],["데이터셋",spec.activeProtocol.dataset??"미제공"],["검증 상태",campaign.conditions?.map(c=>`${c.type}=${c.status} (${c.reason??"사유 미제공"})`).join(" · ")||"미제공"],["해석 범위","이 요청의 측정 조건에 한정 · 논문 적격 인증 아님"]]}/>
      <Rows label="자세히">
        <RowDialog label="측정 조건" title="측정 조건" value={`${spec.batchSizes?.length ? spec.batchSizes.join("·") + "건" : "자동"} · ${spec.trialsPerBatch}번씩`}>
          <Facts items={[["모델", spec.state.candidate.modelSpec], ["장비", `${spec.state.targetNode} · ${spec.state.candidate.deviceClass}`], ["실행 방식", spec.state.candidate.executionPath], ["전력 모드", spec.state.candidate.powerMode],
            ["한 번에 보내는 건수", spec.batchSizes?.length ? spec.batchSizes.map(size => `${size}건`).join(", ") : "자동으로 정해요"], ["크기마다 반복", `${spec.trialsPerBatch}번`], ["CPU", spec.cpuQuota ? `${spec.cpuQuota}개${spec.cpuAffinity?.length ? ` (코어 ${spec.cpuAffinity.join(", ")})` : ""}` : "제한 없음"],
            ["한 회차 측정 시간", `${spec.activeProtocol.durationSeconds ?? "-"}초`], ["실패 시 재시도", `${spec.retryLimit}번`]]}/>
        </RowDialog>
        {!!result?.completedTrials?.length && <RowDialog label="회차별 측정" value={`${result.completedTrials.length}번`} title="회차별 측정" description="회차를 누르면 그 회차의 지연과 전력을 볼 수 있어요.">
          <ul className="rows">{result.completedTrials.map(trial => <li key={trial.run.uid}><Link className="row" href={`/measurements/profiling-runs/${encodeURIComponent(trial.run.namespace)}/${encodeURIComponent(trial.run.name)}?${new URLSearchParams({uid:trial.run.uid,returnTo:planHref(namespace,name,campaign.uid)})}`}>
            <span className="row-main"><span className="row-text">{trial.measurement.workloadMode === "idle" ? "유휴 기준 전력" : `한 번에 ${trial.measurement.batch}건`}</span><span className="row-hint">{trial.run.name}</span></span></Link></li>)}</ul>
        </RowDialog>}
        {!!result?.excludedTrials?.length&&<RowDialog label="실패·제외 회차" title="실패·제외 회차"><Rows>{result.excludedTrials.map(trial=><Row key={trial.run.uid} href={`/measurements/profiling-runs/${encodeURIComponent(trial.run.namespace)}/${encodeURIComponent(trial.run.name)}?${new URLSearchParams({uid:trial.run.uid,returnTo:`${planHref(namespace,name,campaign.uid)}`})}`} label={trial.run.name} hint={trial.reasons?.join(", ")??"제외 사유 미제공"}/>)}</Rows></RowDialog>}
        {phase !== "Complete" && <Row href={`/measurements/runs?${new URLSearchParams({namespace,q:name,parent:name,parentUID:campaign.uid,returnTo:planHref(namespace,name,campaign.uid)})}`} label="지금까지의 회차" hint="부모 측정의 정확한 저장 참조와 연결된 회차"/>}
        <RowDialog label="원본 자료" title="측정 원본 자료"><SourceExport value={{namespace, name, expectedUID, source:data}} filename={`chill-measurement-${name}.json`}/><SourceJSON inline value={data}/></RowDialog>
      </Rows>
      {manage}
    </>}</QueryGate>
  </Screen>
}

function ActiveLatency({trials}:{trials:NonNullable<ResultBody["completedTrials"]>}) {
  const {token,signals}=useSession()
  const active=trials.filter(trial=>trial.measurement.workloadMode==="active")
  const query=useQuery({queryKey:["chill","active-latency",active.map(trial=>trial.run.uid)],enabled:!!token&&active.length>0,retry:false,queryFn:async({signal})=>{
    const abort=new AbortController();signals.add(abort)
    const cancel=()=>abort.abort();signal.addEventListener("abort",cancel)
    let next=0
    const rows:{batch:number|undefined;run:string;p95?:number;count?:number;error?:string}[]=new Array(active.length)
    try {
      await Promise.all(Array.from({length:Math.min(3,active.length)},async()=>{
        while(next<active.length) {
          const index=next++,trial=active[index]
          try {
            const response=await fetch(`/api/chill/v1/namespaces/${encodeURIComponent(trial.run.namespace)}/profiling-runs/${encodeURIComponent(trial.run.name)}/evidence-passport`,{headers:{Authorization:`Bearer ${token}`},signal:abort.signal})
            if(!response.ok)throw new Error(`HTTP ${response.status}`)
            const passport=await response.json() as Passport
            if(passport.run.uid!==trial.run.uid||passport.run.namespace!==trial.run.namespace||passport.run.name!==trial.run.name)throw new Error("회차 identity 불일치")
            rows[index]={batch:trial.measurement.batch,run:trial.run.name,p95:passport.measurement.runtime.latencyMilliseconds.p95,count:passport.measurement.runtime.latencyMilliseconds.count}
          } catch(error) {if(abort.signal.aborted)throw error;rows[index]={batch:trial.measurement.batch,run:trial.run.name,error:error instanceof Error?error.message:"근거 조회 실패"}}
        }
      }))
      return rows
    } finally {signals.delete(abort);signal.removeEventListener("abort",cancel)}
  }})
  if(!active.length)return <p>active 회차 지연 미제공</p>
  const rows=query.data??[],batches=[...new Set(active.map(trial=>trial.measurement.batch))]
  return <section className="measurement-latency grid gap-3" aria-label="회차별 지연"><h2>배치별 active 회차 p95</h2><p>에너지 CI 미제공 · {query.isPending?"지연 확인 중":`조회 ${rows.filter(row=>!row.error).length}/${active.length}회차 · 누락 ${rows.filter(row=>row.error).length}회차`}</p>
    {batches.map(batch=>{const values=rows.filter(row=>row.batch===batch&&row.p95!==undefined&&Number.isFinite(row.p95)).map(row=>row.p95!);return <p key={batch}>배치 {batch}: {values.length===1?`해당 회차 p95 ${significant(values[0])} ms`:values.length?`회차 p95 범위 ${significant(Math.min(...values))}–${significant(Math.max(...values))} ms`:"미제공"}</p>})}
    {!!rows.length&&<DataTable data={rows} columns={[{key:"batch",label:"배치",value:row=>row.batch??"미제공"},{key:"run",label:"회차",value:row=>row.run},{key:"p95",label:"해당 회차 p95 (ms)",value:row=>row.p95===undefined?row.error??"미제공":significant(row.p95)},{key:"count",label:"지연 표본 수 N (반복 수 아님)",value:row=>row.count===undefined?"미제공":`${row.count}${row.count<=1?" · 꼬리 지연 판단에 표본 부족":""}`} ]}/>}
  </section>
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
