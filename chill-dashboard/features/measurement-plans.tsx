"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery } from "@/lib/client"
import type { CampaignResult, MeasurementAssessment, MeasurementMutation, MeasurementPlanList, MeasurementPlanSpec, ResultRow } from "@/lib/read-types"
import { formatMeasurementNumber as number, resultRowsCSV, safeMeasurementReturn } from "@/lib/measurements"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

const plansPath = (namespace:string) => `v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
export const planHref = (namespace:string, name:string, uid?:string) => `/measurements/batch-energy-optimizations/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}${uid ? `?uid=${encodeURIComponent(uid)}` : ""}`
type ResultBody = {completedTrials?:{run:{namespace:string;name:string;uid:string};measurement:{workloadMode:string;batch?:number}}[];excludedTrials?:{run:{namespace:string;name:string;uid:string};reasons?:string[]}[]}

export function AssessmentDetails({assessment}:{assessment?:MeasurementAssessment|null}) {
  if (!assessment) return <p className="notice">서버 판정이 제공되지 않았어요. 실행 상태만으로 사용 가능 여부를 추정하지 않아요.</p>
  return <section className="space-y-3 border-t pt-4" aria-label="서버 결과 판정">
    <h2 className="font-semibold">결과 확인</h2>
    <div className="flex flex-wrap items-center gap-3"><MeasurementStatus assessment={assessment}/><span className="text-sm">{assessment.checklist?.criteriaMet===true?"기준 충족":assessment.checklist?.criteriaMet===false?"결과 체크리스트 미충족":"결과 체크리스트 확인 불가"}</span></div>
    {!!assessment.reasons?.length&&<ul className="space-y-2 text-sm">{assessment.reasons.map((reason,index)=><li key={index}><p>{reason.message.replace(/\s*\(FixtureOnly\)/g,"")}</p><details className="text-xs text-muted-foreground"><summary className="cursor-pointer py-2">사유 코드·출처 원문</summary>{reason.code}{reason.sourceIds?.length?` / ${reason.sourceIds.join(", ")}`:""}</details></li>)}</ul>}
    {assessment.scope?.schedulerUsed===false&&<p className="text-sm">현재 스케줄러 미사용</p>}
    {assessment.scope?.reused&&<p className="text-sm">재사용 결과예요. 대상 확인 실측과 원본 참조는 별도 행이며 독립 전체 실측으로 합산하지 않아요.</p>}
    {!!assessment.scope?.facts?.length&&<ul className="list-disc pl-5 text-sm">{assessment.scope.facts.map((fact,index)=><li key={index}>{fact}</li>)}</ul>}
    {!!assessment.checklist?.checks?.length&&<details><summary className="min-h-11 cursor-pointer py-2 text-sm">결과 확인 항목 원문</summary><div className="overflow-x-auto"><Table><TableHeader><TableRow><TableHead>확인 항목</TableHead><TableHead>서버 결과</TableHead><TableHead>사유</TableHead></TableRow></TableHeader><TableBody>{assessment.checklist.checks.map((check,index)=><TableRow key={index}><TableCell>{check.code}</TableCell><TableCell>{check.result}</TableCell><TableCell className="whitespace-normal">{check.message}</TableCell></TableRow>)}</TableBody></Table></div></details>}
    <p className="text-xs text-muted-foreground">사용 가능은 채택 근거와 목표 batch 측정 충족을 뜻해요. 기준 충족은 서버 결과 체크리스트까지 충족한 경우예요. 전력 관측 시각 부재와 노드 벽전력은 이 판정의 실패 기준이 아니에요.</p>
  </section>
}

function ExactFields({value}:{value:unknown}) {
  if(value===undefined||value===null)return <span>미제공</span>
  if(typeof value!=="object")return <span className="break-all">{String(value)}</span>
  if(Array.isArray(value))return value.length?<ul className="space-y-1">{value.map((item,index)=><li key={index}><ExactFields value={item}/></li>)}</ul>:<span>없음</span>
  return <dl className="grid min-w-56 gap-1 text-xs">{Object.entries(value).map(([key,item])=><div key={key}><dt className="text-muted-foreground">{key}</dt><dd className="whitespace-normal break-all"><ExactFields value={item}/></dd></div>)}</dl>
}

function ResultCondition({condition}:{condition:Record<string,unknown>}) {
  const cpu=condition.cpu&&typeof condition.cpu==="object"?condition.cpu:undefined
  const artifact=condition.artifact&&typeof condition.artifact==="object"?condition.artifact:undefined
  const active=condition.activeProtocol&&typeof condition.activeProtocol==="object"?condition.activeProtocol:undefined
  const text=(value:unknown)=>value==null?"미제공":typeof value==="object"?"name" in value?String(value.name):"식별 정보는 전체 조건 참조":String(value)
  return <div className="min-w-52 space-y-2 whitespace-normal text-xs">
    <p>모델 {text(condition.logicalModel??condition.model)}</p><p>Node {text(condition.targetNode??condition.nodeUID)}</p>
    <p>모드 {text(condition.powerMode)} / 경로 {text(condition.executionPath??(artifact&&"executionPath" in artifact?artifact.executionPath:undefined))}</p>
    <p>CPU {text(cpu&&"request" in cpu?cpu.request:undefined)} / quota {text(cpu&&"limit" in cpu?cpu.limit:undefined)}</p><p>데이터셋 {text(active&&"dataset" in active?active.dataset:condition.dataset)}</p>
    <details><summary className="flex min-h-11 cursor-pointer items-center underline">UID·이미지·측정 방식 포함 전체 조건</summary><ExactFields value={condition}/></details>
  </div>
}

export function ResultRows({rows,filename="chill-result-rows.csv",emptyMessage="저장된 결과가 없어요. 요청 상태와 실패 원인을 확인하세요."}:{rows:ResultRow[];filename?:string;emptyMessage?:string}) {
  function download() {
    const url=URL.createObjectURL(new Blob([resultRowsCSV(rows)],{type:"text/csv;charset=utf-8"}))
    const link=document.createElement("a");link.href=url;link.download=filename.replace(/[^a-zA-Z0-9._-]/g,"_");link.click();URL.revokeObjectURL(url)
  }
  return <section className="min-w-0 space-y-3" aria-label="결과 행">
    <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">운영 판단 요약</h2><Button variant="outline" disabled={!rows.length} onClick={download}>결과 CSV 내보내기</Button></div>
    {!rows.length?<p className="notice">{emptyMessage}</p>:<div className="grid min-w-0 gap-3">{rows.map((row,index)=><article key={row.id??index} className="min-w-0 rounded-lg border p-4">
      <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-semibold">한 번에 {row.batch}건 처리 · {row.rowKind==="confirmation"?"대상 확인 측정":row.rowKind==="source-reference"?"기존 측정 참조":row.rowKind==="direct"?"직접 측정":"측정 결과"}</h3><MeasurementStatus assessment={row.assessment}/></div>
      <p className="mt-2 break-words text-sm tabular-nums">신뢰 근거: {row.confidence&&row.incrementalJoulesPerItem!==null&&row.incrementalJoulesPerItem!==0?`${number(row.confidence.level*100)}% CI ±${number(100*row.confidence.halfWidth/Math.abs(row.incrementalJoulesPerItem))}%`:"상대 CI 미제공"} · 노드 벽전력 출처: {row.powerSource?Object.entries(row.powerSource).filter(([key])=>["kind","nodeName","metric","endpoint","scope"].includes(key)).map(([key,value])=>`${key}: ${String(value)}`).join(" / ")||"출처 상세 참조":"미제공"} (장치 단독 전력 아님)</p>
      <p className="mt-2 text-sm text-amber-700 dark:text-amber-300">모델·데이터셋 용도 일치 여부는 검증되지 않았어요. 아래 측정 조건의 모델과 데이터셋을 확인한 뒤 운영에 사용하세요.</p>
      {!!row.missingReasons?.length&&<p className="mt-2 text-sm text-destructive">{row.missingReasons.join(" · ")}</p>}
      <dl className="mt-3 grid gap-3 sm:grid-cols-3">
        <div><dt className="text-sm text-muted-foreground">응답 지연 p95 / SLO 목표</dt><dd className="font-semibold tabular-nums">{number(row.p95Milliseconds,"ms")} ms</dd><dd className="text-xs text-muted-foreground">이 측정 결과에는 SLO 목표가 제공되지 않아 목표 대비 판단은 할 수 없어요.</dd></div>
        <div><dt className="text-sm text-muted-foreground">요청당 에너지 · 총 / 추가 사용</dt><dd className="font-semibold tabular-nums">{number(row.totalJoulesPerItem,"J/item")} / {number(row.incrementalJoulesPerItem,"J/item")} J/건</dd></div>
        <div><dt className="text-sm text-muted-foreground">평균 전력 · 쉬는 상태 / 실행 중</dt><dd className="font-semibold tabular-nums">{number(row.idleWatts,"W")} / {number(row.activeWatts,"W")} W</dd></div>
      </dl>
      <p className="mt-3 text-sm tabular-nums">독립 반복: 실행 {number(row.activeTrialCount)}회 / 쉬는 상태 {number(row.idleTrialCount)}회 · 지연 표본 {number(row.latencySampleCount)}개</p>
      <ul className="mt-3 space-y-1 text-sm">{row.assessment?.reasons?.map((reason,i)=><li key={i}>{reason.message.replace(/\s*\(FixtureOnly\)/g,"")}</li>)}</ul>
      <details className="mt-3"><summary className="min-h-11 cursor-pointer py-2 text-sm underline">측정 조건·표본·출처 원문 펼치기</summary><div className="grid min-w-0 gap-4 sm:grid-cols-2"><ResultCondition condition={row.condition}/><ExactFields value={row}/></div></details>
    </article>)}</div>}
    <p className="text-xs text-muted-foreground">CSV는 위 결과와 같은 범위이며 원본 정밀도를 유지해요. 전력은 출처에 따라 장치 단독이 아닌 노드 전체 사용량일 수 있어요.</p>
  </section>
}

export function MeasurementPlanPage({namespace,name}:{namespace:string;name:string}) {
  const params=useSearchParams(),expectedUID=params.get("uid")
  const query=useChillQuery<CampaignResult>(`${plansPath(namespace)}/${encodeURIComponent(name)}/result`,{decoder:parseSchedulerJSON})
  const [deleted,setDeleted]=React.useState<MeasurementMutation|null>(null)
  const data=query.data,campaign=data?.campaign,spec=campaign?.spec as MeasurementPlanSpec|undefined
  const other=!!campaign&&!!expectedUID&&campaign.uid!==expectedUID
  const back:[string,string]=[safeMeasurementReturn(params.get("returnTo"),`/measurements/requests?namespace=${encodeURIComponent(namespace)}`),"요청 목록으로"]
  if(deleted)return <Screen back={back} title="측정을 지웠어요" lede={`${name}의 결과와 근거 파일 ${deleted.deletedEvidence?.length??0}개를 함께 지웠어요.`}><SourceJSON value={deleted}/></Screen>
  const result=data?.result as ResultBody|null|undefined
  return <Screen width="data" actionPlacement="header" breadcrumbs={[{href:"/measurements/requests",label:"측정 요청"},{label:name}]} back={back} title={other?"같은 이름의 다른 측정이에요":name} status={!other&&data?<MeasurementStatus assessment={data.assessment}/>:undefined} lede="서버가 확인한 정확 조건과 결과를 보여 줘요."
    action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="requests" namespace={namespace}/>
    <QueryGate query={query}>{campaign&&spec&&!other&&<>
      {campaign.phase==="Failed"&&<section role="alert" className="notice text-destructive"><p>측정 실패: {data?.assessment?.failureSummary?.map(failure=>failure.message).join(" · ")||data?.assessment?.reasons?.map(reason=>reason.message).join(" · ")||"서버 실패 원인을 확인하세요. 아래 연결된 회차에서 실패 근거를 볼 수 있어요."}</p>{data?.assessment?.failureSummary?.map(failure=>failure.runName&&<Link key={failure.runId} className="inline-flex min-h-11 items-center underline" href={`/measurements/profiling-runs/${encodeURIComponent(failure.runNamespace)}/${encodeURIComponent(failure.runName)}?${new URLSearchParams({uid:failure.runId.split("/").at(-1)??"",returnTo:planHref(namespace,name,campaign.uid)})}`}>{failure.runName} 실패 회차 보기{failure.exitCode!=null?` · 종료 코드 ${failure.exitCode}`:""}</Link>)}</section>}
      <ResultRows rows={data?.resultRows??[]} filename={`chill-measurement-${name}.csv`} emptyMessage={campaign.phase==="Failed"?"요청이 실패해 사용할 결과가 없어요. 실패 원인을 수정한 뒤 새 요청을 만드세요.":campaign.phase==="Complete"?"측정은 종료됐지만 저장된 결과가 없어요. 결과 판정과 회차 근거를 확인하세요.":"아직 측정 결과가 없어요. 현재 요청 상태와 연결된 회차를 확인하세요."}/>
      <AssessmentDetails assessment={data?.assessment}/>
      <Facts label="요청 조건" items={[["모델",spec.state.candidate.modelSpec],["장치 종류",spec.state.candidate.deviceClass],["실행 장비",spec.state.targetNode],["전력 모드",spec.state.candidate.powerMode],["실행 방식",spec.state.candidate.executionPath],["목표 처리 건수",spec.batchSizes?.join(", ")??"등록된 처리 범위"],["CPU 사용 한도",spec.cpuQuota??"제한 없음"],["사용할 CPU 코어",spec.cpuAffinity?.join(", ")??"미지정"],["데이터셋",spec.activeProtocol.dataset??"미제공"],["건수별 반복",`${spec.trialsPerBatch}회`],["실행 중 측정",`${spec.activeProtocol.durationSeconds??"미제공"}초`],["예열",`${spec.activeProtocol.warmupIterations??"미제공"}회`],["쉬는 상태 측정",`${spec.idleProtocol.durationSeconds??"미제공"}초`],["실행 상태",campaign.phase==="Measuring"?"측정 중":campaign.phase==="Complete"?"측정 완료":campaign.phase==="Failed"?"측정 실패":"상태 확인 필요"]]}/>
      <Rows label="회차와 원본">
        {!!result?.completedTrials?.length&&<RowDialog label="완료 회차" title="완료 회차"><Rows>{result.completedTrials.map(trial=><Row key={trial.run.uid} label={trial.run.name} hint={`${trial.measurement.workloadMode==="idle"?"쉬는 상태":trial.measurement.workloadMode==="active"?"실행 중":"측정 회차"} / 처리 ${trial.measurement.batch??"미제공"}건`} href={`/measurements/profiling-runs/${encodeURIComponent(trial.run.namespace)}/${encodeURIComponent(trial.run.name)}?${new URLSearchParams({uid:trial.run.uid,returnTo:planHref(namespace,name,campaign.uid)})}`}/>)}</Rows></RowDialog>}
        {!!result?.excludedTrials?.length&&<RowDialog label="실패 또는 제외 회차" title="실패 또는 제외 회차"><Rows>{result.excludedTrials.map(trial=><Row key={trial.run.uid} label={trial.run.name} hint={trial.reasons?.join(", ")??"사유 미제공"} href={`/measurements/profiling-runs/${encodeURIComponent(trial.run.namespace)}/${encodeURIComponent(trial.run.name)}?${new URLSearchParams({uid:trial.run.uid,returnTo:planHref(namespace,name,campaign.uid)})}`}/>)}</Rows></RowDialog>}
        <Row href={`/measurements/runs?${new URLSearchParams({namespace,q:name,parent:name,parentUID:campaign.uid,returnTo:planHref(namespace,name,campaign.uid)})}`} label="연결된 회차 찾기"/>
        <RowDialog label="원본 자료" title="측정 원본 자료"><SourceExport value={{namespace,name,expectedUID,source:data}} filename={`chill-measurement-${name}.json`}/><SourceJSON inline value={data}/></RowDialog>
      </Rows>
      <Button variant="outline" asChild><Link href={`/measurements/new?${new URLSearchParams({from:name,fromNamespace:namespace,fromUID:campaign.uid})}`}>조건을 복사해 새 이름으로 요청</Link></Button>
      <Rows label="위험 영역"><DeleteMeasurement namespace={namespace} name={name} uid={campaign.uid} version={campaign.resourceVersion} trials={campaign.completedTrials??0} blocked={campaign.phase==="Measuring"?"진행 중인 측정은 지울 수 없어요. 끝난 뒤에 지워 주세요.":undefined} onDeleted={setDeleted}/></Rows>
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
    <li className="p-4"><div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/40 p-4"><div><p className="font-semibold text-destructive">측정과 근거 영구 삭제</p><p className="text-sm text-muted-foreground">{blocked??"결과와 모든 회차 근거를 삭제하며 되돌릴 수 없어요."}</p></div><Button variant="destructive" onClick={()=>setOpen(true)} disabled={!!blocked}>측정 삭제</Button></div></li>
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
