"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery } from "@/lib/client"
import type { CampaignResult, MeasurementAssessment, MeasurementFailure, MeasurementMutation, MeasurementPlanSpec, ResultRow } from "@/lib/read-types"
import { executionPathLabel, formatMeasurementNumber as number, measurementCheckText, measurementReasonSummary, resultRowsCSV, safeMeasurementReturn } from "@/lib/measurements"
import { assessmentReasons } from "@/lib/measurement-assessment"
import { requestExecutionStatus, requestFailureFacts, requestHref, requestRunHref, requestUsedResults } from "@/lib/measurement-request"
import { useMeasurementCoverage, useMeasurementPlans } from "@/hooks/use-measurement-data"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Facts, Rows } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { MeasurementStatus, MeasurementEvidenceChecks } from "@/components/measurement-navigation"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { StatusCell } from "@/components/status-cell"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet"
import { MeasurementChildRuns } from "@/features/measurement-runs"

const plansPath = (namespace:string) => `v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
type ResultBody = {excludedTrials?:{run:{namespace:string;name:string;uid:string};reasons?:string[]}[]}

export function FirstMeasurementError({failure}:{failure?:MeasurementFailure}) {
  if(!failure?.firstErrorLine)return null
  const line=failure.firstErrorTime?failure.firstErrorLine.slice(failure.firstErrorTime.length).trim():failure.firstErrorLine
  return <div aria-label="첫 오류 원문" className="space-y-1">{failure.firstErrorTime&&<time className="block">{failure.firstErrorTime}</time>}<code className="block whitespace-normal break-words" title={failure.firstErrorLine}>{line}</code></div>
}

export function AssessmentDetails({assessment,rows=[]}:{assessment?:MeasurementAssessment|null;rows?:ResultRow[]}) {
  if (!assessment) return <p className="notice">서버 판정 미제공 · 스케줄러 사용 여부 미확인</p>
  const reasons=assessmentReasons(assessment),checks=assessment.checklist?.checks??[]
  return <section className="space-y-3 border-t pt-4" aria-label="신뢰 근거">
    <h2 className="font-semibold">판정 근거</h2>
    {assessment.scope?.reused&&<p className="text-sm">다른 노드 측정 참조 · 독립 반복 합산 제외</p>}
    {reasons.length>0&&<ul className="space-y-2 text-sm">{reasons.map(reason=><li key={reason}>{reason}</li>)}</ul>}
    <MeasurementEvidenceChecks assessment={assessment} rows={rows}/>
    {!reasons.length&&!checks.length&&<p className="text-sm text-muted-foreground">상세 판정 사유·확인 항목 미제공</p>}
    {(checks.length>0||assessment.reasons?.length>0)&&<details><summary className="min-h-11 cursor-pointer py-2 text-sm">서버 확인 항목·사유 원문</summary>
      {!!assessment.reasons?.length&&<ul className="space-y-2 break-words text-sm">{assessment.reasons.map((reason,index)=><li key={index}>{reason.code}: {reason.message.replace(/\s*\(FixtureOnly\)/g,"")}</li>)}</ul>}
      {checks.length>0&&<div className="overflow-x-auto"><Table><TableHeader><TableRow><TableHead>확인 항목</TableHead><TableHead>서버 결과</TableHead><TableHead>사유</TableHead></TableRow></TableHeader><TableBody>{checks.map((check,index)=><TableRow key={index}><TableCell>{check.code}</TableCell><TableCell>{check.result==="unknown"&&check.reason==="PolicyUnset"?"정책 미설정":{pass:"확인",fail:"불일치",warn:"참고",unknown:"미확인"}[check.result]}</TableCell><TableCell className="whitespace-normal">{measurementCheckText({code:check.code,message:check.message})}</TableCell></TableRow>)}</TableBody></Table></div>}
    </details>}
  </section>
}

function ExactFields({value}:{value:unknown}) {
  if(value===undefined||value===null)return <span>미제공</span>
  if(typeof value!=="object")return <span className="break-all">{String(value)}</span>
  if(Array.isArray(value))return value.length?<ul className="space-y-1">{value.map((item,index)=><li key={index}><ExactFields value={item}/></li>)}</ul>:<span>없음</span>
  return <dl className="grid min-w-56 gap-1 text-xs">{Object.entries(value).map(([key,item])=><div key={key}><dt className="text-muted-foreground">{key}</dt><dd className="whitespace-normal break-all"><ExactFields value={item}/></dd></div>)}</dl>
}

function ResultCondition({condition}:{condition:Record<string,unknown>}) {
  const artifact=condition.artifact&&typeof condition.artifact==="object"?condition.artifact:undefined
  const active=condition.activeProtocol&&typeof condition.activeProtocol==="object"?condition.activeProtocol:undefined
  const executionPath=condition.executionPath??(artifact&&"executionPath" in artifact?artifact.executionPath:undefined)
  const text=(value:unknown)=>value==null?"미제공":typeof value==="object"?"name" in value?String(value.name):"식별 정보는 전체 조건 참조":String(value)
  return <div className="min-w-52 space-y-2 whitespace-normal text-xs">
    <p>모델 {text(condition.logicalModel??condition.model)}</p><p>Node {text(condition.targetNode??condition.nodeUID)}</p>
    <p>모드 {text(condition.powerMode)} / 경로 {typeof executionPath==="string"?executionPathLabel(executionPath):"미제공"}</p>
    <p>데이터셋 {text(active&&"dataset" in active?active.dataset:condition.dataset)}</p>
    <details><summary className="flex min-h-11 cursor-pointer items-center underline">UID·이미지·측정 방식 포함 전체 조건 원문</summary><ExactFields value={condition}/></details>
  </div>
}

export function ResultRows({rows,filename="chill-result-rows.csv",emptyMessage="결과 미기록",title="측정 결과"}:{rows:ResultRow[];filename?:string;emptyMessage?:string;title?:string}) {
  function download() {
    const url=URL.createObjectURL(new Blob([resultRowsCSV(rows)],{type:"text/csv;charset=utf-8"}))
    const link=document.createElement("a");link.href=url;link.download=filename.replace(/[^a-zA-Z0-9._-]/g,"_");link.click();URL.revokeObjectURL(url)
  }
  return <section className="min-w-0 space-y-3" aria-label="결과 행">
    <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">{title}</h2><Button variant="outline" disabled={!rows.length} onClick={download}>결과 CSV</Button></div>
    {!rows.length?<p className="notice">{emptyMessage}</p>:<div className="min-w-0 overflow-x-auto"><Table className="[&_th]:text-right [&_td]:text-right [&_td]:tabular-nums"><TableHeader><TableRow><TableHead>배치 (건)</TableHead><TableHead>p95 (ms)</TableHead><TableHead>총 에너지 (J/건)</TableHead><TableHead>추가 에너지 (J/건)</TableHead><TableHead>실행 (회)</TableHead><TableHead>유휴 (회)</TableHead><TableHead>지연 표본 (개)</TableHead><TableHead>유휴 (W)</TableHead><TableHead>실행 (W)</TableHead></TableRow></TableHeader><TableBody>{rows.map((row,index)=><TableRow key={row.id??index}><TableCell>{row.batch}</TableCell><TableCell>{number(row.p95Milliseconds,"ms")}</TableCell><TableCell>{number(row.totalJoulesPerItem,"J/item")}</TableCell><TableCell>{number(row.incrementalJoulesPerItem,"J/item")}</TableCell><TableCell>{number(row.activeTrialCount)}</TableCell><TableCell>{number(row.idleTrialCount)}</TableCell><TableCell>{number(row.latencySampleCount)}</TableCell><TableCell>{number(row.idleWatts,"W")}</TableCell><TableCell>{number(row.activeWatts,"W")}</TableCell></TableRow>)}</TableBody></Table></div>}
    {rows.length>0&&<details><summary className="min-h-11 cursor-pointer py-2 text-sm">조건·표본·출처</summary><div className="space-y-4">{rows.map((row,index)=><section key={row.id??index} className="min-w-0 border-t pt-3"><h3 className="font-medium">{row.rowKind==="confirmation"?"대상 확인 측정":row.rowKind==="source-reference"?"기존 측정 참조":"직접 측정"}</h3><ResultCondition condition={row.condition}/><ExactFields value={row}/></section>)}</div></details>}
  </section>
}

export function MeasurementPlanSheet({namespace,name}:{namespace:string;name:string}) {
  const router=useRouter(),params=useSearchParams()
  const back=safeMeasurementReturn(params.get("returnTo"),`/measurements/requests?namespace=${encodeURIComponent(namespace)}`)
  return <Sheet open onOpenChange={open=>{if(!open)router.push(back,{scroll:false})}}><SheetContent className="w-full overflow-y-auto sm:max-w-4xl" onCloseAutoFocus={event=>{event.preventDefault();requestAnimationFrame(()=>document.querySelector<HTMLElement>(`a[data-request-name="${CSS.escape(name)}"]`)?.focus())}}>
    <SheetHeader><SheetTitle className="break-all">{name}</SheetTitle><SheetDescription className="sr-only">요청·회차·점검 항목</SheetDescription></SheetHeader>
    <div className="min-w-0 space-y-5 px-4 pb-6"><MeasurementPlanDetails key={`${namespace}/${name}`} namespace={namespace} name={name}/></div>
  </SheetContent></Sheet>
}


export function MeasurementPlanDetails({namespace,name}:{namespace:string;name:string}) {
  const params=useSearchParams(),expectedUID=params.get("uid")
  const query=useChillQuery<CampaignResult>(`${plansPath(namespace)}/${encodeURIComponent(name)}/result`,{decoder:parseSchedulerJSON})
  const [deleted,setDeleted]=React.useState<MeasurementMutation|null>(null)
  const denied=[401,403].includes(query.error?.status??0)
  const errorData=denied?undefined:query.error?.data as Partial<CampaignResult>|undefined
  const metadata=useMeasurementPlans(namespace,!!query.error&&!denied&&!errorData?.campaign)
  const stored=metadata.data?.items?.find(plan=>plan.metadata.name===name&&(!expectedUID||plan.metadata.uid===expectedUID))
  const data=denied?undefined:query.data??(errorData?.campaign?errorData as CampaignResult:stored?{campaign:{...stored.metadata,...stored.status,spec:stored.spec},assessment:stored.assessment,resultRows:[],result:null}:undefined)
  const campaign=data?.campaign,spec=campaign?.spec as MeasurementPlanSpec|undefined
  const coverage=useMeasurementCoverage(namespace)
  const other=!!campaign&&!!expectedUID&&campaign.uid!==expectedUID
  if(deleted)return <section role="status"><h2>측정 삭제 완료</h2><SourceJSON value={deleted}/></section>
  const result=data?.result as ResultBody|null|undefined
  const linkedRuns=[...new Map([...(data?.assessment?.failureSummary??[]).filter(failure=>failure.runName).map(failure=>({namespace:failure.runNamespace||namespace,name:failure.runName,uid:failure.runId.split("/").at(-1)??""})),...(result?.excludedTrials??[]).map(trial=>trial.run)].map(run=>[`${run.namespace}/${run.name}/${run.uid}`,run])).values()]
  const used=spec?requestUsedResults(coverage.data?.cells??[],spec):[]
  const otherUsed=used.filter(candidate=>!data?.resultRows?.some(row=>!!row.id&&row.id===candidate.id||!!row.profileId&&row.profileId===candidate.profileId&&row.batch===candidate.batch))
  const sharesUsed=otherUsed.length<used.length
  const resultQuery=spec?new URLSearchParams({namespace,kind:"throughput",model:spec.state.candidate.modelSpec,deviceClass:spec.state.candidate.deviceClass,node:spec.state.targetNode,path:spec.state.candidate.executionPath,powerMode:spec.state.candidate.powerMode}):new URLSearchParams()
  const returnTo=`${requestHref(namespace,name,campaign?.uid)}&${new URLSearchParams({returnTo:params.get("returnTo")??""})}`
  return <>
    <Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>
    {other&&<p role="alert" className="notice">측정 식별자 불일치</p>}
    {query.error&&!denied&&data&&<section role="alert" className="border border-destructive/40 p-3"><StatusCell axis="관측 상태" label={`결과 조회 실패 · HTTP ${query.error.status}`} tone="fail"/><SourceJSON value={query.error.data??query.error.body} title="결과 조회 오류 원문"/><span className="text-sm">요청 상태와 결과 파일 조회는 별도</span></section>}
    {!data&&<QueryGate query={query}>{null}</QueryGate>}
    {campaign&&spec&&!other&&<>
      <div className="flex flex-wrap gap-3"><StatusCell axis="실행" label={requestExecutionStatus(campaign.phase)} tone={campaign.phase==="Failed"?"fail":"idle"}/><MeasurementStatus assessment={data?.assessment}/></div>
      <table aria-label="요청 진행" className="w-full text-left text-sm tabular-nums"><tbody><tr><th className="py-2 font-medium">완료 회차 (회)</th><td>{campaign.completedTrials??"미제공"}</td></tr><tr><th className="py-2 font-medium">예정 회차 (회)</th><td>미제공</td></tr><tr><th className="py-2 font-medium">현재 단계</th><td>{requestExecutionStatus(campaign.phase)}</td></tr><tr><th className="py-2 font-medium">현재 세부 실행 단계</th><td>미제공</td></tr></tbody></table>
      <details><summary className="min-h-11 cursor-pointer py-2 text-sm">요청 조건</summary><Facts items={[["Workload",spec.activeProtocol.workload||"미기록"],["모델",spec.state.candidate.modelSpec],["장치 종류",spec.state.candidate.deviceClass],["장비",spec.state.targetNode],["전력모드",spec.state.candidate.powerMode],["실행 경로",executionPathLabel(spec.state.candidate.executionPath)],["배치 (건)",spec.batchSizes?.join(", ")??"선언 범위"],["반복 (회)",spec.trialsPerBatch],["데이터셋",spec.activeProtocol.dataset??"미제공"],["실행 측정 (s)",spec.activeProtocol.durationSeconds??"미제공"],["예열 (회)",spec.activeProtocol.warmupIterations??"미제공"],["유휴 측정 (s)",spec.idleProtocol.durationSeconds??"미제공"]]}/></details>
      {campaign.phase==="Failed"&&<section role="alert" className="space-y-2 border border-destructive/40 p-4">
        <h2 className="font-semibold text-destructive">실패 근거</h2>
        <FirstMeasurementError failure={data?.assessment?.failureSummary?.find(failure=>failure.firstErrorLine)}/>
        <ul className="space-y-2 text-sm">{(data?.assessment?.failureSummary??[]).map((failure,index)=><li key={index}>{measurementReasonSummary(failure.message||failure.code)}</li>)}</ul>
        <details><summary className="min-h-11 cursor-pointer py-2">서버 실패 사유·연결 회차 원문</summary>
        <table aria-label="실패 원인·연결 회차" className="w-full text-left text-sm"><tbody>
          {requestFailureFacts(campaign.conditions).map(([label,value],index)=><tr key={index} className="border-b"><th scope="row" className="w-28 py-2 align-top font-medium">{label}</th><td className="break-words py-2">{value}</td></tr>)}
          <tr><th scope="row" className="py-2 align-top font-medium">연결 회차</th><td>{linkedRuns.length?linkedRuns.map(run=><Link key={run.uid||run.name} className="flex min-h-11 items-center break-all underline" href={requestRunHref(run.namespace,run.name,run.uid,{name,uid:campaign.uid},returnTo)}>{run.name}</Link>):<a className="inline-flex min-h-11 items-center underline" href="#request-child-runs">회차 검색</a>}</td></tr>
          {data?.assessment?.failureSummary?.map((failure,index)=><React.Fragment key={index}><tr><th scope="row" className="py-2 align-top font-medium">분류</th><td>{failure.code==="Other"?campaign.conditions?.find(condition=>condition.reason)?.reason||failure.code:failure.label??failure.code}</td></tr><tr><th scope="row" className="py-2 align-top font-medium">서버 상세</th><td className="break-words">{failure.message||"미제공"}</td></tr><tr><th scope="row" className="py-2 align-top font-medium">종료 코드</th><td className="text-right tabular-nums">{failure.exitCode??"미제공"}</td></tr></React.Fragment>)}
        </tbody></table>
        </details>
      </section>}
      <ResultRows rows={data?.resultRows??[]} title={sharesUsed?"측정 결과 · 현재 사용 결과 포함":"이 요청의 측정 결과"} filename={`chill-measurement-${name}.csv`} emptyMessage={query.error?"결과 파일 조회 실패":campaign.phase==="Failed"?"측정 실패 · 요청 결과 없음":campaign.phase==="Complete"?"실행 종료 · 저장 결과 없음":"측정 결과 대기"}/>
      <QueryGate query={coverage}>{otherUsed.length>0&&<section aria-label="현재 사용 결과" className="space-y-3"><h2 className="font-semibold">별도 보관 결과</h2><Facts items={[["요청과의 관계","이 요청의 측정 결과와 다른 객체"],["출처","보관된 처리 성능 프로파일"],["현재 사용","스케줄러 참조 · 품질 승인과 별개"]]}/><ResultRows title="다른 보관 프로파일의 현재 사용 결과" rows={otherUsed} filename={`chill-used-${name}.csv`}/></section>}</QueryGate><Link className="inline-flex min-h-11 items-center underline" href={`/measurements/results?${resultQuery}`}>같은 대상 결과</Link>
      <section id="request-child-runs" className="space-y-3"><h2 className="font-semibold">자식 회차</h2><MeasurementChildRuns namespace={namespace} parent={{name,uid:campaign.uid}}/></section>
      <AssessmentDetails assessment={data?.assessment} rows={data?.resultRows}/>
      <Button variant="outline" asChild><Link href={`/measurements/requests/new?${new URLSearchParams({namespace,from:name,fromNamespace:namespace,fromUID:campaign.uid})}`}>동일 조건 요청</Link></Button>
      <details><summary className="min-h-11 cursor-pointer py-2 text-sm">재사용할 측정 요청 문서</summary><p className="pb-3 text-sm text-muted-foreground">YAML/JSON 가져오기에서 열고 새 요청 이름과 대상 조건을 확인하세요.</p><SourceExport value={{apiVersion:"edge.dacs.io/v1alpha1",kind:"BatchEnergyOptimization",metadata:{name,namespace},spec}} filename={`${name}-request.json`}/></details>
      <details><summary className="min-h-11 cursor-pointer py-2">관리</summary><Rows label="위험 영역"><DeleteMeasurement namespace={namespace} name={name} uid={campaign.uid} version={campaign.resourceVersion} trials={campaign.completedTrials??0} blocked={campaign.phase==="Measuring"?"진행 중인 측정은 삭제할 수 없어요.":undefined} onDeleted={setDeleted}/></Rows></details>
      <section aria-label="원본"><SourceExport value={{namespace,name,expectedUID,source:data}} filename={`chill-measurement-${name}.json`}/><SourceJSON value={data} title="요청 원본"/></section>
    </>}
  </>
}

// Permanent: ChillServer deletes the result, curve plan, serving bundle and per-trial evidence it created, then the native object.
function DeleteMeasurement({namespace, name, uid, version, trials, blocked, onDeleted}:{namespace:string;name:string;uid:string;version:string;trials:number;blocked?:string;onDeleted:(result:MeasurementMutation)=>void}) {
  const mutation = useChillMutation(), client = useQueryClient()
  const [open, setOpen] = React.useState(false), [understood, setUnderstood] = React.useState(false), [unknown, setUnknown] = React.useState(false)
  const list = useMeasurementPlans(namespace,unknown)
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
