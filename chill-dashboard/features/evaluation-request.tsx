"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery } from "@/lib/client"
import { parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { evaluationActions, evaluationCanSubmit, evaluationOperation, evaluationPreflight, evaluationReleaseBody, evaluationSameSpec, type EvaluationSpec, type EvaluationLiveTarget } from "@/lib/evaluation-request"
import { decodeExperiment, experimentPath, finite, type Experiment, type OutcomeAxis, type Published } from "@/lib/experiments"
import { experimentRows } from "@/lib/experiment-comparison"
import { evaluationReplacement, object, records, type LoadScenario } from "@/lib/evaluation-author"
import { formatMeasurementTime } from "@/lib/measurements"
import { EvaluationActiveDetails } from "@/features/evaluation-active"
import { ExperimentStageResults } from "@/features/experiments-conclusion"
import type { ActiveEvaluations, EvaluationStageProgress } from "@/lib/evaluation-active"
import { settingFields, settingControls, settingTargets } from "@/lib/scheduler-settings"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { Screen } from "@/components/screen"

type RunCondition = {type:string;status:string;reason?:string;message?:string;lastTransitionTime?:string}
type RunProcedure = {step:string;startedAt:string|null;finishedAt:string|null;reason?:string;measurementVerdict?:string}
type RunStage = EvaluationStageProgress & {name:string;uid:string;currentProcedure?:string;conditions:RunCondition[]|null;measurementVerdict?:string;procedures:RunProcedure[]|null;startedAt:string|null;finishedAt:string|null;goAt:string|null;provisionalD1?:Published|null}
type Run = {name:string;uid:string;spec:EvaluationSpec;liveTarget?:EvaluationLiveTarget[]|null;phase:string;conditions?:RunCondition[];currentStage?:unknown;currentProcedure?:string;procedures?:RunProcedure[]|null;stages?:RunStage[]|null;startedAt?:string;finishedAt?:string;publicationID?:string}
type Created = {name:string;uid:string}
const encode=(value:unknown)=>stringifySchedulerJSON(value)
const phases:Record<string,string>={Pending:"시작 전",Running:"실행 중",Finishing:"정리 중",Done:"종료",Blocked:"정리 실패"}
const conditionLabels:Record<string,string>={PoolAcquired:"풀 확보",ExecutionComplete:"실행",MeasurementPassed:"측정 판정",CleanupComplete:"정리",Published:"게시"}
const conditionReasons:Record<string,string>={Returned:"반환 완료",IdentityMismatch:"실행 대상 식별자 불일치",NOT_EVALUATED:"미평가",NoControlEffects:"복구할 제어 변경 없음",PublicationFailed:"결과 게시 실패",SLOFailed:"SLO 미충족"}
const arrivalLabels:Record<string,string>={poisson:"푸아송 분포",constant:"일정 간격",uniform:"균등 분포",trace:"기록 재생"}
const procedureLabels:Record<string,string>={prepare:"준비",factor:"조건 적용",stage:"부하 실행",judge:"측정 판정","cleanup-jobs":"작업 정리",finish:"정리",publish:"게시"}
const loadJobStatuses={Active:"진행 중",Succeeded:"완료",Failed:"실패"}

function ProcedureTimeline({title,procedures,current,finished,publishing=false,now}:{title:string;procedures:RunProcedure[]|null|undefined;current?:string;finished:boolean;publishing?:boolean;now:number}) {
  return <div className="overflow-x-auto"><table className="w-full text-left text-sm tabular-nums"><caption className="py-2 text-left font-medium">{title}</caption><thead><tr>{["절차","시작 (KST)","종료 (KST)","소요","사유","측정 판정"].map(label=><th key={label} className="whitespace-nowrap p-2">{label}</th>)}</tr></thead><tbody>{procedures?.map((procedure,index)=>{
    const isCurrent=procedure.finishedAt===null&&procedure.step===current&&(!finished||procedure.step==="publish"&&publishing)
    const start=procedure.startedAt?Date.parse(procedure.startedAt):NaN,end=procedure.finishedAt?Date.parse(procedure.finishedAt):isCurrent?now:NaN
    const seconds=Number.isFinite(start)&&Number.isFinite(end)&&end>=start&&end>0?Math.floor((end-start)/1000):null
    return <tr key={index} aria-current={isCurrent?"step":undefined} className={isCurrent?"border-t bg-primary/10 font-medium":"border-t"}><th className="p-2 font-normal">{procedureLabels[procedure.step]??procedure.step}{isCurrent&&<span className="block text-xs">현재 절차</span>}</th><td className="whitespace-nowrap p-2">{procedure.startedAt?formatMeasurementTime(procedure.startedAt):"시작 미기록"}</td><td className="whitespace-nowrap p-2">{procedure.finishedAt?formatMeasurementTime(procedure.finishedAt):isCurrent?"진행 중":"종료 미기록"}</td><td className="whitespace-nowrap p-2">{seconds===null?"미기록":`${seconds.toLocaleString("ko-KR")}초`}</td><td className="p-2">{procedure.reason?conditionReasons[procedure.reason]??procedure.reason:"미기록"}</td><td className="p-2">{procedure.measurementVerdict??"미기록"}</td></tr>
  })}{!procedures?.length&&<tr><td colSpan={6} className="p-2">절차 기록 없음</td></tr>}</tbody></table></div>
}

function LatestConditions({conditions}:{conditions:RunCondition[]|null|undefined}) {
  const types=[...new Set([...Object.keys(conditionLabels),...(conditions??[]).map(condition=>condition.type)])]
  return <div className="space-y-2"><h3 className="text-xs text-muted-foreground">조건별 최신 상태 · 마지막 변경 (KST)</h3><ol className="grid grid-cols-2 gap-px overflow-hidden rounded border bg-border md:grid-cols-5">{types.map(type=>{
    const label=conditionLabels[type]??type,condition=conditions?.find(item=>item.type===type)
    let state=!condition?"미기록":condition.status==="Unknown"?"미확인":condition.status==="True"?"완료":condition.status==="False"?"미완료":condition.status
    if(condition&&type==="PoolAcquired")state=condition.status==="True"?"점유 중":condition.reason==="Returned"?"반환 완료":condition.status==="False"?"미확보":"미확인"
    if(condition&&type==="MeasurementPassed")state=condition.status==="True"?"SLO 충족":condition.reason==="NOT_EVALUATED"?"미평가":condition.reason==="SLOFailed"?"SLO 미충족":condition.status==="False"?"판정 미통과":"미확인"
    if(condition&&type==="Published")state=condition.status==="True"?"게시 완료":condition.status==="False"?"미게시":"미확인"
    const reason=condition?.reason?conditionReasons[condition.reason]??condition.reason:undefined
    return <li key={type} className="space-y-1 bg-background p-3"><h4 className="text-sm text-muted-foreground">{label}</h4><p className={`font-medium ${type==="MeasurementPassed"&&condition?.status==="False"?"text-destructive":""}`}>{state}</p>{reason&&reason!==state&&<p className="text-xs">{reason}</p>}{condition?.message&&<p className="text-xs">{condition.message}</p>}<p className="text-xs tabular-nums">{condition?.lastTransitionTime?<time dateTime={condition.lastTransitionTime}>{formatMeasurementTime(condition.lastTransitionTime)}</time>:"시각 미기록"}</p></li>
  })}</ol></div>
}

function SettingsChanges({patch,title,unavailable}:{patch:unknown;title:string;unavailable?:string}) {
  const rows=Object.entries(object(patch)).flatMap(([key,value])=>key==="controls"
    ?Object.entries(object(value)).map(([control,enabled])=>({key:`controls.${control}`,label:settingControls.find(item=>item.key===control)?.label??control,value:enabled}))
    :[{key,label:[...settingFields,...settingTargets].find(item=>item.key===key)?.label??key,value}])
  return <table className="w-full text-left text-sm"><caption className="py-2 text-left font-medium">{title} · 변경 설정</caption><thead><tr><th className="p-2">항목</th><th className="p-2">변경값</th></tr></thead><tbody>{rows.map(row=><tr key={row.key} className="border-t"><th className="p-2 font-normal">{row.label}</th><td className="break-all p-2">{typeof row.value==="boolean"?row.value?"활성":"비활성":row.key.endsWith("Nanos")&&(typeof row.value==="number"||typeof row.value==="bigint")?`${Number(row.value)/1e9}초`:Array.isArray(row.value)?row.value.length?row.value.map(String).join(", "):"없음":row.value!==null&&typeof row.value==="object"?Object.entries(row.value).map(([key,value])=>`${key}: ${String(value)}`).join(", ")||"없음":String(row.value??"미기록")}</td></tr>)}{!rows.length&&<tr><td colSpan={2} className="p-2">{unavailable??"변경 없음"}</td></tr>}</tbody></table>
}
function runHref(run:Created,search?:URLSearchParams) {
  const next=new URLSearchParams(search)
  next.delete("name");next.delete("uid");next.set("uid",run.uid)
  return `/experiments/runs/${encodeURIComponent(run.name)}?${next}`
}
function Failure({error}:{error:ApiError|null}) {
  if(!error)return null
  const value=error.data as {code?:string;message?:string}|null
  return <strong role="alert" className="text-sm text-destructive" title={value?.message??error.body}>{error.status===403?"권한 없음(실험 운영자)":error.unknownOutcome?"결과 미확인":value?.code??error.message}</strong>
}

export function EvaluationPermission({denied=false}:{denied?:boolean}) {
  return <dl aria-label="실험 작업 권한" className="flex flex-wrap gap-x-4 gap-y-1 text-xs"><div className="flex gap-2"><dt>필요 권한</dt><dd>평가 운영자</dd></div><div className="flex gap-2"><dt>현재 권한</dt><dd className={denied?"text-destructive":"text-muted-foreground"}>{denied?"권한 없음":"미확인"}</dd></div></dl>
}


function EvaluationTargets({targets,title}:{targets?:EvaluationLiveTarget[]|null;title:string}) {
  return <section aria-label={title} className="space-y-2"><h3 className="text-sm font-medium">{title}</h3>{targets==null?<span className="text-sm">미기록</span>:<div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th>구성요소</th><th>자원</th><th>관측 이미지 ID</th><th>준비</th></tr></thead><tbody>{targets.map((target,index)=><tr key={`${target.component}/${index}`} className="border-t"><th className="py-2 font-normal">{target.component}</th><td title={`${target.resource.apiVersion} · ${target.resource.namespace}`}>{target.resource.kind}/{target.resource.name}</td><td className="break-all">{target.liveImageIDs.length?target.liveImageIDs.join(", "):"미제공"}</td><td>{target.ready?"준비됨":"준비 대기"}</td></tr>)}{!targets.length&&<tr><td colSpan={4}>대상 없음</td></tr>}</tbody></table></div>}</section>
}
export function EvaluationSubmit({name,spec,disabled=false,existing,onSubmitted}:{name:string;spec:EvaluationSpec;disabled?:boolean;existing?:Created;onSubmitted?:(value:Created)=>void}) {
  const pre=useChillMutation(),send=useChillMutation(),cache=useQueryClient()
  const [checkedBody,setCheckedBody]=React.useState(""),[unknown,setUnknown]=React.useState(false),[created,setCreated]=React.useState<Created|null>(null)
  const [replacement,setReplacement]=React.useState<"unchanged"|"deleted"|"unknown"|"blocked"|null>(null)
  const [attempt,setAttempt]=React.useState<{name:string;spec:EvaluationSpec}|null>(null)
  const body=encode({name,spec}),answer=checkedBody===body?evaluationPreflight(pre.data?.data):null
  const lookup=useChillQuery<Run>(`v1/evaluations/${encodeURIComponent((unknown?attempt?.name:name)||"_")}`,{enabled:false,decoder:parseSchedulerJSON})
  const confirmed=unknown&&attempt&&lookup.data?.name===attempt.name&&!!lookup.data.uid&&evaluationSameSpec(lookup.data.spec,attempt.spec)&&(!existing||lookup.data.uid!==existing.uid)
  const busy=pre.isPending||send.isPending
  function reconcile(read:{data?:Run;error:ApiError|null},requested:{name:string;spec:EvaluationSpec}) {
    if(!existing)return
    const result=evaluationReplacement(existing.uid,requested.spec,read.error?.status??200,read.data)
    if(result.state==="updated") {
      const value={name:requested.name,uid:result.uid}
      setReplacement(null);setUnknown(false);setCreated(value);onSubmitted?.(value)
    } else {
      setReplacement(result.state);setUnknown(result.state==="unknown")
      if(result.state==="deleted"||result.state==="unchanged")setCheckedBody("")
    }
  }
  async function refresh() {
    const [read]=await Promise.all([lookup.refetch(),cache.invalidateQueries({queryKey:["chill","v1/evaluations"]}),cache.invalidateQueries({queryKey:["chill","v1/evaluations/active"]})])
    if(existing&&unknown&&attempt)reconcile(read,attempt)
  }
  function verify() {
    if(disabled||busy||unknown||created)return
    setCheckedBody(body)
    pre.mutate({path:"v1/evaluations/preflight",method:"POST",body})
  }
  async function submit() {
    if(disabled||busy||!evaluationCanSubmit(answer,checkedBody,body,unknown)||created)return
    setAttempt({name,spec:structuredClone(spec)})
    try {
      const input=existing&&replacement!=="deleted"?{path:`v1/evaluations/${encodeURIComponent(existing.name)}?${new URLSearchParams({uid:existing.uid})}`,method:"PUT" as const,body:encode({spec})}:{path:"v1/evaluations",method:"POST" as const,body}
      const response=existing?await send.mutateAsync(input):await evaluationOperation(()=>send.mutateAsync(input),refresh)
      const value=response.data as Created
      if(!value?.name||!value.uid){setUnknown(true);await refresh();return}
      setCreated(value);onSubmitted?.(value);await refresh()
    } catch(error) {
      if(existing) {
        if(error instanceof ApiError&&error.status===409){setReplacement("blocked");setCheckedBody("");return}
        reconcile(await lookup.refetch(),{name,spec})
      } else if(error instanceof ApiError&&(error.unknownOutcome||error.status===0))setUnknown(true)
    }
  }
  return <div className="space-y-3">
    {answer&&<table className="w-full text-left text-sm"><thead><tr><th>검사</th><th>상태</th><th>사유</th></tr></thead><tbody>{answer.checks.map((check,index)=><tr key={`${check.id}/${index}`} className="border-t"><th className="py-2 font-normal">{check.id}</th><td>{check.passed?"통과":"실패"}</td><td>{check.reason??"미기록"}</td></tr>)}</tbody></table>}
    {!!answer?.liveTarget?.length&&<EvaluationTargets title="현재 외부 대상" targets={answer.liveTarget}/>}
    {pre.data&&checkedBody===body&&!answer&&<strong role="alert">점검 응답 형식 오류</strong>}
    <Failure error={pre.error}/><Failure error={send.error}/>
    {replacement&&<strong role="status">{replacement==="deleted"?"삭제됨·재생성 필요":replacement==="unchanged"?"미변경":replacement==="blocked"?"중지 후 다시 실행":"결과 미확인"}</strong>}
    {(unknown||created)&&<div role="status" className="flex flex-wrap items-center gap-3 text-sm"><strong>{created?existing?"수정 접수":"실행 접수":confirmed?"실행 확인":"결과 미확인"}</strong>{(created||confirmed)&&<Link className="underline" href={runHref(created??{name:lookup.data!.name,uid:lookup.data!.uid})}>실험 상태</Link>}{unknown&&<Button variant="outline" onClick={()=>void refresh()} disabled={lookup.isFetching}>다시 조회</Button>}</div>}
    <EvaluationPermission denied={pre.error?.status===403||send.error?.status===403}/>
    <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={disabled||busy||unknown||!!created} onClick={verify}>{pre.isPending?"점검 중":"사전 점검"}</Button><Button disabled={disabled||busy||!!created||!evaluationCanSubmit(answer,checkedBody,body,unknown)} onClick={()=>void submit()}>{send.isPending?"제출 중":replacement==="deleted"?"재생성 제출":existing?"수정 제출":"실험 시작"}</Button></div>
  </div>
}

export function EvaluationLaunch({sourceName,publicationID,label="새 실험"}:{sourceName?:string;publicationID?:string;label?:string}) {
  const query=new URLSearchParams(publicationID?{publication:publicationID}:sourceName?{copy:sourceName}:{})
  return <Button variant="outline" asChild><Link href={`/experiments/new${query.size?`?${query}`:""}`}>{label}</Link></Button>
}

function CleanupPanel({cleanup,identity}:{cleanup:OutcomeAxis|undefined;identity:Created|undefined}) {
  const [open,setOpen]=React.useState(false),[reason,setReason]=React.useState(""),[unknown,setUnknown]=React.useState(false)
  const send=useChillMutation(),cache=useQueryClient()
  const current=useChillQuery<Run>(`v1/evaluations/${encodeURIComponent(identity?.name??"_")}`,{enabled:open&&!!identity,poll:true,decoder:parseSchedulerJSON})
  if(cleanup?.status!=="Failed")return null
  async function release() {
    if(!identity||!reason.trim()||send.isPending||unknown||send.isSuccess||current.data?.uid!==identity.uid)return
    try {
      await evaluationOperation(()=>send.mutateAsync({path:`v1/evaluations/${encodeURIComponent(identity.name)}/release`,method:"POST",body:evaluationReleaseBody(identity.uid,reason)}),()=>current.refetch())
      await Promise.allSettled([current.refetch(),cache.invalidateQueries({queryKey:["chill","v1/evaluations/active"]})])
    } catch(error){if(error instanceof ApiError&&(error.unknownOutcome||error.status===0))setUnknown(true)}
  }
  return <div className="space-y-2"><div className="overflow-x-auto"><table className="w-full text-left text-sm"><caption className="text-left">정리 잔여 자원</caption><thead><tr><th>종류</th><th>이름</th><th>사유</th></tr></thead><tbody>{cleanup.residuals?.map((row,index)=><tr key={`${row.kind}/${row.name}/${index}`} className="border-t"><td className="py-2">{row.kind}</td><th className="break-all font-normal">{row.name}</th><td>{row.reason}</td></tr>)}{!cleanup.residuals?.length&&<tr><td colSpan={3}>미기록</td></tr>}</tbody></table></div><Button variant="outline" disabled={!identity} onClick={()=>setOpen(true)}>자원 반환 확인 요청</Button><Dialog open={open} onOpenChange={setOpen}><DialogContent aria-describedby={undefined}><DialogHeader><DialogTitle>자원 반환 확인 요청</DialogTitle></DialogHeader><label className="grid gap-2 text-sm">확인 사유<Input value={reason} onChange={event=>setReason(event.target.value)} required disabled={send.isPending||unknown||send.isSuccess}/></label><dl className="flex gap-3 text-sm"><dt>실제 반환</dt><dd>{current.data?.uid===identity?.uid?(current.data?.conditions?.find(condition=>condition.type==="PoolAcquired")?.status==="False"?"반환 관측":current.data?.conditions?.find(condition=>condition.type==="PoolAcquired")?.status==="True"?"점유 중":"미확인"):"미확인"}</dd></dl><Failure error={send.error}/><Failure error={current.error}/>{current.data&&current.data.uid!==identity?.uid&&<strong role="alert">UID 변경</strong>}{(unknown||send.isSuccess)&&<strong role="status">{unknown?"결과 미확인":"확인 접수"}</strong>}<Button disabled={!identity||!reason.trim()||send.isPending||unknown||send.isSuccess||current.data?.uid!==identity?.uid} onClick={()=>void release()}>확인 제출</Button>{unknown&&<Button variant="outline" onClick={()=>void current.refetch()}>다시 조회</Button>}</DialogContent></Dialog></div>
}

export function EvaluationCleanup({run}:{run:Experiment}) {
  return <CleanupPanel cleanup={run.outcome?.cleanup} identity={run.definition?.run}/>
}

function BlockedCleanup({run}:{run:Run}) {
  const publication=useChillQuery<Experiment>(experimentPath(run.publicationID??""),{enabled:!!run.publicationID,decoder:decodeExperiment})
  return <div className="space-y-2"><h3 className="text-sm font-semibold">풀 묶임</h3>{publication.error&&<Failure error={publication.error}/>}<CleanupPanel cleanup={publication.data?.outcome?.cleanup??{status:"Failed"}} identity={{name:run.name,uid:run.uid}}/></div>
}

function RunActions({run,refresh}:{run:Run;refresh:()=>Promise<unknown>}) {
  const [stop,setStop]=React.useState(false),[unknown,setUnknown]=React.useState(false),send=useChillMutation()
  const actions=evaluationActions(run.phase)
  async function remove() {
    if(!run.uid||send.isPending||unknown||send.isSuccess)return
    try {await evaluationOperation(()=>send.mutateAsync({path:`v1/evaluations/${encodeURIComponent(run.name)}?${new URLSearchParams({uid:run.uid})}`,method:"DELETE"}),refresh);await refresh()}
    catch(error){if(error instanceof ApiError&&(error.unknownOutcome||error.status===0))setUnknown(true)}
  }
  return <div className="flex flex-wrap gap-2">{actions.edit&&<Button variant="outline" asChild><Link href={`/experiments/runs/${encodeURIComponent(run.name)}/edit?${new URLSearchParams({uid:run.uid})}`}>수정</Link></Button>}<EvaluationLaunch sourceName={run.name} label="다시 실행"/><Button variant="outline" onClick={()=>setStop(true)}>{actions.remove}</Button><Dialog open={stop} onOpenChange={setStop}><DialogContent aria-describedby={undefined}><DialogHeader><DialogTitle>{run.name} · {actions.remove} 확인</DialogTitle></DialogHeader><span className="break-all text-sm">UID {run.uid}</span><Failure error={send.error}/>{(unknown||send.isSuccess)&&<strong role="status">{unknown?"결과 미확인":`${actions.remove} 접수`}</strong>}<Button disabled={!run.uid||send.isPending||unknown||send.isSuccess} onClick={()=>void remove()}>확인</Button>{unknown&&<Button variant="outline" onClick={()=>void refresh()}>다시 조회</Button>}</DialogContent></Dialog></div>
}

export function EvaluationsPage() {
  const list=useChillQuery<{items:Run[]}>("v1/evaluations",{poll:true,decoder:parseSchedulerJSON})
  const rows=list.data?.items??[]
  return <Screen width="data" title="실험" actionPlacement="header" action={<div className="flex flex-wrap items-center gap-2"><nav aria-label="실험 목록 보기" className="flex gap-2"><Button variant="secondary" asChild><Link href="/experiments" aria-current="page">실행</Link></Button><Button variant="outline" asChild><Link href="/experiments?view=published&offset=0">게시 결과</Link></Button></nav><Button variant="outline" asChild><Link href="/experiments/load-scenarios">부하 설정</Link></Button><EvaluationLaunch/></div>}>
    <QueryGate query={list}><section className="min-w-0 overflow-x-auto" aria-label="실험 실행 목록"><table className="w-full text-left text-sm max-md:[&>thead]:sr-only max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr>td]:flex max-md:[&>tbody>tr>td]:justify-between max-md:[&>tbody>tr>td]:gap-3 max-md:[&>tbody>tr>td]:before:content-[attr(data-label)]"><thead><tr>{["이름","실행 상태","단계","시작 (KST)","종료 (KST)","풀 상태","결과"].map(label=><th key={label} className="p-2">{label}</th>)}</tr></thead><tbody>{rows.map(run=><tr key={run.uid} className="border-t">
      <th scope="row" className="break-all p-2 font-normal"><Link className="inline-flex min-h-11 items-center underline" href={runHref(run)}>{run.name}</Link></th><td data-label="실행 상태">{phases[run.phase]??"미확인"}</td><td data-label="단계">{typeof run.currentStage==="string"||typeof run.currentStage==="number"?String(run.currentStage):"미기록"}</td><td data-label="시작 (KST)">{run.startedAt?formatMeasurementTime(run.startedAt):"미기록"}</td><td data-label="종료 (KST)">{run.finishedAt?formatMeasurementTime(run.finishedAt):"미기록"}</td><td data-label="풀 상태">{run.conditions?.find(c=>c.type==="PoolAcquired")?.status==="True"?"점유 중":run.conditions?.find(c=>c.type==="PoolAcquired")?.status==="False"?"반환 관측":"미확인"}</td><td data-label="결과">{run.publicationID?<Link className="inline-flex min-h-11 items-center underline" href={`/experiments/results?${new URLSearchParams({a:run.publicationID})}`}>게시 결과</Link>:"미게시"}</td>
    </tr>)}{!rows.length&&<tr><td colSpan={7}>항목 없음</td></tr>}</tbody></table></section></QueryGate>
  </Screen>
}

export function EvaluationRunPage({name}:{name:string}) {
  const params=useSearchParams(),router=useRouter(),uid=params.get("uid")
  const [now,setNow]=React.useState(0),[help,setHelp]=React.useState(false)
  const detail=useChillQuery<Run>(`v1/evaluations/${encodeURIComponent(name)}`,{poll:true,decoder:parseSchedulerJSON})
  const run=detail.data?.name===name&&!detail.error?detail.data:undefined
  const replaced=!!uid&&!!run&&run.uid!==uid
  const publication=useChillQuery<Experiment>(experimentPath(run?.publicationID??""),{enabled:!!run?.publicationID&&!replaced,poll:true,decoder:decodeExperiment})
  const published=run?.publicationID&&publication.data?.id===run.publicationID&&!publication.error?publication.data:undefined
  const frozen=published?.definition?.run.uid===run?.uid?published?.definition:undefined
  const planSpec=run?.publicationID?frozen?.spec:run?.spec
  const definitionNotice=run?.publicationID?(publication.error?"조회 실패":publication.isPending?"조회 중":!frozen?"미기록":undefined):undefined
  const loads=useChillQuery<{items:LoadScenario[]}>("v1/evaluations/load-scenarios",{enabled:!!run&&!replaced&&!run.publicationID,decoder:parseSchedulerJSON})
  const active=useChillQuery<ActiveEvaluations>("v1/evaluations/active",{enabled:!!run&&!replaced&&!run.finishedAt&&run.phase!=="Done",poll:true})
  const activeStage=!active.error&&!replaced&&!run?.finishedAt&&run?.phase!=="Done"?active.data?.items?.find(item=>item.name===name&&item.uid===run?.uid)?.stage:undefined
  const activeStageProgress=run?.stages?.find(stage=>stage.id===activeStage?.id)
  const goAt=activeStage?.goAt?Date.parse(activeStage.goAt):NaN
  const loadElapsed=now&&Number.isFinite(goAt)&&now>=goAt&&typeof activeStage?.plannedSeconds==="number"&&Number.isFinite(activeStage.plannedSeconds)&&activeStage.plannedSeconds>0?(now-goAt)/1000:null
  const stages=records(planSpec?.stages)
  const stagesNotice=definitionNotice??(!Array.isArray(planSpec?.stages)?"미기록":undefined)
  const sources=run?.publicationID?frozen?.loadScenarios:loads.error?undefined:loads.data?.items
  const loadNotice=definitionNotice??(run?.publicationID?!Array.isArray(sources)?"미기록":undefined:loads.error?"조회 실패":loads.isPending?"조회 중":!Array.isArray(sources)?"미기록":undefined)
  const plans=stages.map(stage=>{
    const ref=object(stage.loadScenarioRef)
    const scenario=sources?.find(item=>item.uid===ref.uid&&item.name===ref.name)
    const parts=records(scenario?.spec.parts)
    let offset=0
    const currentPart=parts.findIndex(part=>{
      const duration=finite(part.durationSeconds),start=offset
      offset=duration!==null&&duration>=0?offset+duration:NaN
      return activeStageProgress?.progress==null&&activeStage?.id===stage.id&&loadElapsed!==null&&loadElapsed<(activeStage?.plannedSeconds??0)&&loadElapsed>=start&&loadElapsed<offset
    })
    const notice=loadNotice??(!scenario||!Array.isArray(scenario.spec.parts)?"미기록":undefined)
    return {stage,scenario,parts,currentPart,notice}
  })
  const completePlan=!stagesNotice&&plans.every(plan=>!plan.notice)
  const workloads=[...new Set(plans.flatMap(plan=>[...records(object(plan.scenario?.spec).workloads).flatMap(workload=>typeof workload.name==="string"?[workload.name]:[]),...plan.parts.flatMap(part=>Object.keys(object(part.rates)))]))]
  const parts=plans.flatMap(plan=>plan.parts)
  const plannedSeconds=completePlan&&parts.every(part=>finite(part.durationSeconds)!==null)?parts.reduce((sum,part)=>sum+Number(part.durationSeconds),0):null
  const failedServices=[...new Set((published?.stages??[]).flatMap(stage=>experimentRows(stage).filter(row=>row.original.D1==="FAIL").map(row=>row.name)))]
  const verdict=published?.outcome?.measurement?.verdict
  const verdictLabels:Record<string,string>={FAIL:published?.outcome?.measurement?.reason==="SLOFailed"?"SLO 미충족":"측정 실패",PASS:"SLO 충족",OBSERVED_THRESHOLD_MET:"SLO 충족",NOT_EVALUATED:"미평가"}
  const resultLabel=published?`게시 판정 ${published.status}${verdict?` · ${verdictLabels[verdict]??verdict}`:""}`:run?.publicationID?`결과 ${publication.error?"조회 실패":publication.isPending?"조회 중":"미기록"}`:"결과 미게시"
  const finished=!!run?.finishedAt||run?.phase==="Done"
  const provisional=finished?undefined:run?.stages?.find(stage=>stage.id===run.currentStage&&!stage.finishedAt)?.provisionalD1
  const elapsed=run?.startedAt&&(run.finishedAt||!finished&&now)?Math.max(0,Math.floor(((run.finishedAt?Date.parse(run.finishedAt):now)-Date.parse(run.startedAt))/1000)):null
  const currentHref=run?runHref(run,new URLSearchParams(params)):""
  React.useEffect(()=>{if(run&&!uid)router.replace(currentHref,{scroll:false})},[run,uid,currentHref,router])
  React.useEffect(()=>{const timer=window.setInterval(()=>setNow(Date.now()),1000);return ()=>window.clearInterval(timer)},[])
  const resultParams=new URLSearchParams(params)
  for(const key of ["name","uid","view"])resultParams.delete(key)
  if(run?.publicationID)resultParams.set("a",run.publicationID)
  return <Screen width="data" title={name} back={["/experiments","실험"]} actionPlacement="header" action={run&&!replaced?<RunActions key={run.uid} run={run} refresh={detail.refetch}/>:undefined}>
    {detail.error&&detail.error.status!==404&&<p role="status">실행 조회 실패</p>}
    {detail.error?.status===404?<strong role="status">실행 없음</strong>:<QueryGate query={detail}>{run&&(replaced?<div className="space-y-2"><strong role="status">실행 교체됨</strong><Link className="inline-flex min-h-11 items-center underline" href={currentHref}>현재 실행 선택</Link></div>:<>
      <section aria-label="실험 요약" className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className={`text-lg font-semibold ${verdict==="FAIL"?"text-destructive":""}`}>{phases[run.phase]??"미확인"} · {resultLabel}{failedServices.length>0&&` · 실패 서비스 ${failedServices.join(", ")}`}</h2>
          <div className="flex items-center gap-2">{run.publicationID?<Button variant="outline" asChild><Link href={`/experiments/results?${resultParams}`}>결과 보기</Link></Button>:<span className="text-sm text-muted-foreground">게시 후 결과 제공</span>}<TooltipProvider><Tooltip open={help} onOpenChange={setHelp}><TooltipTrigger asChild><Button variant="ghost" className="min-h-11" onClick={()=>setHelp(value=>!value)}>도움말</Button></TooltipTrigger><TooltipContent className="max-w-sm">등록 질문은 작성자의 설명이며 부하 표가 실제 참조 계획입니다. 실행 단계와 부하 구간은 다릅니다. 조건은 각 항목의 최신 상태로 전체 이력이 아니며 시각은 마지막 변경입니다. 풀 반환 시각으로 확보 시각을 알 수 없습니다. 구간 경계는 서버가 기록한 시각이며 빠진 경계를 추정하지 않습니다. flush·수집 중에는 주기 신호가 없을 수 있고 신호 없는 경과만으로 멈춤을 판단할 수 없습니다. 구간 기록이 없는 구 서버의 진행률·남은 시간은 GO 기준 계획값이며 판정·정리 시간은 포함하지 않습니다. 실행 중 요청 집계는 잠정이며 결과 대기 요청은 이후 성공으로 바뀔 수 있습니다. 최종 판정은 요청 배출 후 게시됩니다. 중지 접수와 풀 반환은 별개이며 결과 미확인 작업은 다시 보내지 않고 조회합니다.</TooltipContent></Tooltip></TooltipProvider></div>
        </div>
        <p className="break-words text-sm"><span className="text-muted-foreground">등록 질문 </span>{definitionNotice??(typeof planSpec?.question==="string"?planSpec.question:"미기록")}</p>
        <p className="flex flex-wrap gap-x-5 gap-y-1 text-sm tabular-nums"><strong>워크로드 {completePlan?`${workloads.length}개`:stagesNotice??loadNotice??"미기록"}</strong><span>실행 단계 {stagesNotice??`${stages.length}개`}</span><span>부하 구간 {completePlan?`${parts.length}개`:stagesNotice??loadNotice??"미기록"}</span><span>부하 계획 {plannedSeconds===null?stagesNotice??loadNotice??"미기록":`${plannedSeconds.toLocaleString("ko-KR")}초`}</span><span>{finished?"전체 실행":"실행 경과"} {elapsed!==null&&Number.isFinite(elapsed)?`${elapsed.toLocaleString("ko-KR")}초`:"미기록"}</span></p>
        {!run.finishedAt&&run.phase!=="Done"&&<EvaluationActiveDetails key={run.uid} active={active} name={name} uid={uid||run.uid} now={now} stageProgress={activeStageProgress} stageActive={run.phase==="Running"&&!run.currentProcedure&&run.currentStage===activeStage?.id} procedures={run.procedures}/>}
        <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">{[["시작 (KST)",run.startedAt],["종료 (KST)",run.finishedAt]].map(([label,value])=><div key={label} className="flex gap-2"><dt>{label}</dt><dd>{value?formatMeasurementTime(value):"미기록"}</dd></div>)}</dl>
      </section>
      {provisional&&<section aria-label="실행 중 잠정 서비스 결과"><ExperimentStageResults provisional={provisional}/></section>}
      <section aria-label="진행 단계" className="space-y-2">
        <h2 className="font-semibold">진행 단계 <span className="text-xs font-normal text-muted-foreground">{Array.isArray(run.procedures)?"기록된 절차 순서 · 시작·종료 (KST)":"조건별 최신 상태 · 마지막 변경 (KST)"}</span></h2>
        <div className="space-y-3">
          {Array.isArray(run.procedures)&&<ProcedureTimeline title="실행 절차" procedures={run.procedures} current={run.currentProcedure} finished={!!run.finishedAt||run.phase==="Done"} publishing={!run.publicationID} now={now}/>}
          {run.stages?.map(stage=><section key={stage.uid} className="space-y-1 rounded border p-3">
            <ProcedureTimeline title={`실행 단계 ${stage.id}`} procedures={stage.procedures} current={stage.currentProcedure} finished={!!stage.finishedAt||!!run.finishedAt||run.phase==="Done"} now={now}/>
            {stage.loadJobs!=null&&<div className="overflow-x-auto"><table className="w-full text-left text-sm tabular-nums" aria-label="부하 Job"><caption className="py-2 text-left">부하 Job</caption><thead><tr>{["이름","상태","시작 (KST)","완료 (KST)","실패 사유"].map(label=><th key={label} className="whitespace-nowrap p-2">{label}</th>)}</tr></thead><tbody>{stage.loadJobs.map(job=><tr key={job.uid} className="border-t"><th scope="row" className="break-all p-2 font-normal">{job.name}</th><td className="whitespace-nowrap p-2">{loadJobStatuses[job.status]}</td><td className="whitespace-nowrap p-2">{job.startedAt?formatMeasurementTime(job.startedAt):"미기록"}</td><td className="whitespace-nowrap p-2">{job.completedAt?formatMeasurementTime(job.completedAt):"미기록"}</td><td className="break-words p-2">{job.reason??"미기록"}</td></tr>)}{!stage.loadJobs.length&&<tr><td colSpan={5} className="p-2">부하 Job 없음</td></tr>}</tbody></table></div>}
            <LatestConditions conditions={stage.conditions}/>
            {stage.measurementVerdict&&<p className="text-sm">단계 측정 판정 {stage.measurementVerdict}</p>}
          </section>)}
        </div>
        <LatestConditions conditions={run.conditions}/>
        {evaluationActions(run.phase).release&&<BlockedCleanup key={run.uid} run={run}/>}
      </section>
      <section aria-label="부하 계획" className="space-y-2">
        <h2 className="font-semibold">부하 계획 <span className="text-xs font-normal text-muted-foreground">{run.publicationID?"게시 당시 동결 정의":"참조된 부하 정의"}</span></h2>
        {(stagesNotice??loadNotice)&&<p role="status" className="text-sm">부하 계획 {stagesNotice??loadNotice}</p>}
        <div className="overflow-x-auto"><table className="w-full text-left text-sm tabular-nums"><thead><tr><th className="p-2">실행 단계</th><th className="p-2">부하 구간</th><th className="p-2 text-right">기간 (초)</th>{workloads.map(workload=><th key={workload} className="min-w-36 p-2 text-right">{workload}<span className="block text-xs font-normal text-muted-foreground">계획 요청/s</span></th>)}</tr></thead><tbody>{plans.map((plan,index)=>plan.parts.length?plan.parts.map((part,partIndex)=><tr key={`${index}/${partIndex}`} aria-current={plan.currentPart===partIndex?"step":undefined} className={plan.currentPart===partIndex?"border-t bg-primary/10 font-medium":"border-t"}><th className="break-all p-2 font-normal">{String(plan.stage.id??"미기록")}</th><th className="break-all p-2 font-normal">{String(part.id??"미기록")}{plan.currentPart===partIndex&&<strong className="ml-2 text-primary">계획상 구간 {String(part.id??"미기록")}</strong>}</th><td className="p-2 text-right">{finite(part.durationSeconds)??"미기록"}</td>{workloads.map(workload=><td key={workload} className="p-2 text-right">{finite(object(part.rates)[workload])?.toLocaleString("ko-KR",{maximumSignificantDigits:6})??"미기록"}</td>)}</tr>):<tr key={index} className="border-t"><th className="p-2 font-normal">{String(plan.stage.id??"미기록")}</th><td className="p-2" colSpan={workloads.length+2}>부하 계획 {plan.notice??"구간 없음"}</td></tr>)}{!plans.length&&<tr><td className="p-2" colSpan={workloads.length+3}>부하 계획 {stagesNotice??"단계 없음"}</td></tr>}</tbody></table></div>
        {plans.map((plan,index)=><table key={index} className="w-full text-left text-sm"><caption className="py-2 text-left font-medium">{String(plan.stage.id??"미기록")} · 서비스 도착 분포</caption><thead><tr><th className="p-2">서비스</th><th className="p-2">도착 분포</th></tr></thead><tbody>{plan.notice?<tr><td colSpan={2} className="p-2">{plan.notice}</td></tr>:workloads.map(workload=>{const distribution=object(object(plan.scenario?.spec).arrivalDistribution)[workload];return <tr key={workload} className="border-t"><th className="p-2 font-normal">{workload}</th><td className="p-2">{typeof distribution==="string"?arrivalLabels[distribution]??distribution:"미기록"}</td></tr>})}</tbody></table>)}
        <SettingsChanges title="실험 전체" patch={planSpec?.settingsPatch} unavailable={definitionNotice}/>
        {plans.map((plan,index)=><SettingsChanges key={index} title={`실행 단계 ${String(plan.stage.id??"미기록")}`} patch={plan.stage.settingsPatch}/>)}
      </section>
      <section aria-label="실행 근거" className="space-y-2">
        <details><summary className="min-h-11 cursor-pointer py-3 text-sm">평가 대상 CHILL · 이미지 digest</summary><EvaluationTargets title="평가 대상 CHILL" targets={run.liveTarget}/></details>
        <details><summary className="min-h-11 cursor-pointer py-3 text-sm">조건·정의 원문</summary><SourceJSON inline title="조건 원문" value={run.conditions}/><SourceJSON inline title="마지막 실행 단계 원문" value={run.currentStage}/>{definitionNotice?<p>실험 정의 {definitionNotice}</p>:<SourceJSON inline title="실험 정의 원문" value={planSpec}/>}<SourceJSON inline title="참조 부하 정의" value={plans.map(plan=>({id:plan.stage.id,...(plan.notice?{source:plan.notice}:{definition:plan.scenario})}))}/></details>
        <details><summary className="min-h-11 cursor-pointer py-3 text-sm">기록된 절차·단계 원문</summary><SourceJSON inline title="절차·단계 원문" value={{currentProcedure:run.currentProcedure,procedures:run.procedures,stages:run.stages}}/></details>
      </section>
    </>)}</QueryGate>}
  </Screen>
}
