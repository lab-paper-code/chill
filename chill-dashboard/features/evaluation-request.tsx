"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery } from "@/lib/client"
import { parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { evaluationActions, evaluationAdoptDigests, evaluationCanSubmit, evaluationDigestChanges, evaluationOperation, evaluationPreflight, evaluationReleaseBody, evaluationSameSpec, type EvaluationSpec } from "@/lib/evaluation-request"
import { decodeExperiment, experimentPath, type Experiment, type OutcomeAxis } from "@/lib/experiments"
import { evaluationReplacement } from "@/lib/evaluation-author"
import { EvaluationAuthor } from "@/features/evaluation-author"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { Screen } from "@/components/screen"

type Run = {name:string;uid:string;spec:EvaluationSpec;phase:string;conditions?:{type:string;status:string;reason?:string}[];currentStage?:unknown;startedAt?:string;finishedAt?:string;publicationID?:string}
type Created = {name:string;uid:string}
const encode=(value:unknown)=>stringifySchedulerJSON(value)
const phases:Record<string,string>={Pending:"시작 전",Running:"실행 중",Finishing:"정리 중",Done:"종료",Blocked:"정리 실패"}
function Failure({error}:{error:ApiError|null}) {
  if(!error)return null
  const value=error.data as {code?:string;message?:string}|null
  return <strong role="alert" className="text-sm text-destructive" title={value?.message??error.body}>{error.status===403?"권한 없음(평가 운영자)":error.unknownOutcome?"결과 미확인":value?.code??error.message}</strong>
}

export function EvaluationSubmit({name,spec,disabled=false,existing,onSubmitted,onSpecChange}:{name:string;spec:EvaluationSpec;disabled?:boolean;existing?:Created;onSubmitted?:(value:Created)=>void;onSpecChange:(spec:EvaluationSpec)=>void}) {
  const pre=useChillMutation(),send=useChillMutation(),cache=useQueryClient()
  const [checkedBody,setCheckedBody]=React.useState(""),[unknown,setUnknown]=React.useState(false),[selected,setSelected]=React.useState<string[]>([]),[created,setCreated]=React.useState<Created|null>(null)
  const [images,setImages]=React.useState<Record<string,string>>({})
  const [replacement,setReplacement]=React.useState<"unchanged"|"deleted"|"unknown"|"blocked"|null>(null)
  const [attempt,setAttempt]=React.useState<{name:string;spec:EvaluationSpec}|null>(null)
  const body=encode({name,spec}),answer=checkedBody===body?evaluationPreflight(pre.data?.data):null
  const lookup=useChillQuery<Run>(`v1/evaluations/${encodeURIComponent((unknown?attempt?.name:name)||"_")}`,{enabled:false,decoder:parseSchedulerJSON})
  const changes=evaluationDigestChanges(spec,answer?.liveTarget)
  const unsupported=answer?.liveTarget!==undefined&&!Array.isArray(answer.liveTarget)
  const confirmed=unknown&&attempt&&lookup.data?.name===attempt.name&&evaluationSameSpec(lookup.data.spec,attempt.spec)&&(!existing||lookup.data.uid!==existing.uid)
  const busy=pre.isPending||send.isPending
  async function refresh() {
    await Promise.allSettled([lookup.refetch(),cache.invalidateQueries({queryKey:["chill","v1/evaluations"]}),cache.invalidateQueries({queryKey:["chill","v1/evaluations/active"]})])
  }
  function verify() {
    if(disabled||busy||unknown||created)return
    setCheckedBody(body);setSelected([]);setImages({})
    pre.mutate({path:"v1/evaluations/preflight",method:"POST",body})
  }
  async function submit() {
    if(disabled||busy||!evaluationCanSubmit(answer,checkedBody,body,unknown)||unsupported||created)return
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
        const read=await lookup.refetch(),result=evaluationReplacement(existing.uid,spec,read.error?.status??200,read.data)
        if(result.state==="updated"){const value={name,uid:result.uid};setCreated(value);onSubmitted?.(value);await refresh()}
        else {setReplacement(result.state);setUnknown(result.state==="unknown");if(result.state==="deleted"||result.state==="unchanged")setCheckedBody("")}
      } else if(error instanceof ApiError&&(error.unknownOutcome||error.status===0))setUnknown(true)
    }
  }
  return <div className="space-y-3">
    {answer&&<table className="w-full text-left text-sm"><thead><tr><th>검사</th><th>상태</th><th>사유</th></tr></thead><tbody>{answer.checks.map((check,index)=><tr key={`${check.id}/${index}`} className="border-t"><th className="py-2 font-normal">{check.id}</th><td>{check.passed?"통과":"실패"}</td><td>{check.reason??"—"}</td></tr>)}</tbody></table>}
    {!!changes.length&&<div className="overflow-x-auto"><table className="w-full text-left text-sm"><caption className="text-left">이미지 digest 차이</caption><thead><tr><th>반영</th><th>구성요소</th><th>정의</th><th>현재 imageID</th></tr></thead><tbody>{changes.map(change=><tr key={change.name} className="border-t"><td><input type="checkbox" aria-label={`${change.name} digest 반영`} disabled={!change.images.length} checked={selected.includes(change.name)} onChange={event=>setSelected(values=>event.target.checked?[...values,change.name]:values.filter(name=>name!==change.name))}/></td><th className="p-2 font-normal">{change.name}</th><td className="max-w-60 break-all p-2">{change.before}</td><td className="max-w-60 break-all p-2">{change.images.length>1?<select className="max-w-60 rounded border bg-background p-2" aria-label={`${change.name} imageID 선택`} value={images[change.name]??""} onChange={event=>setImages(values=>({...values,[change.name]:event.target.value}))}><option value="">선택</option>{change.images.map(image=><option key={image} value={image}>{image}</option>)}</select>:change.after||"미관측"}</td></tr>)}</tbody></table><Button variant="outline" disabled={!selected.length||busy||unknown||selected.some(name=>{const change=changes.find(change=>change.name===name);return !change?.after&&!images[name]})} onClick={()=>{onSpecChange(evaluationAdoptDigests(spec,changes.map(change=>({...change,after:images[change.name]??change.after})),selected));setSelected([])}}>선택 반영</Button></div>}
    {unsupported&&<><strong role="alert">대상 형식 미확인</strong><SourceJSON inline title="현재 대상" value={answer?.liveTarget}/></>}
    {pre.data&&checkedBody===body&&!answer&&<strong role="alert">점검 응답 형식 오류</strong>}
    <Failure error={pre.error}/><Failure error={send.error}/>
    {replacement&&<strong role="status">{replacement==="deleted"?"삭제됨·재생성 필요":replacement==="unchanged"?"미변경":replacement==="blocked"?"중지 후 다시 실행":"결과 미확인"}</strong>}
    {(unknown||created)&&<div role="status" className="flex flex-wrap items-center gap-3 text-sm"><strong>{created?existing?"수정 접수":"실행 접수":confirmed?"실행 확인":"결과 미확인"}</strong>{(created||confirmed)&&<Link className="underline" href={`/evaluations?${new URLSearchParams({name:created?.name??attempt?.name??name})}`}>실험 상태</Link>}{unknown&&<Button variant="outline" onClick={()=>void refresh()} disabled={lookup.isFetching}>다시 조회</Button>}</div>}
    <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={disabled||busy||unknown||!!created} onClick={verify}>{pre.isPending?"점검 중":"사전 점검"}</Button><Button disabled={disabled||busy||!!created||unsupported||!evaluationCanSubmit(answer,checkedBody,body,unknown)} onClick={()=>void submit()}>{send.isPending?"제출 중":replacement==="deleted"?"재생성 제출":existing?"수정 제출":"실행 제출"}</Button></div>
  </div>
}

export function EvaluationLaunch({spec,sourceName,label="실행"}:{spec?:EvaluationSpec;sourceName?:string;label?:string}) {
  const [open,setOpen]=React.useState(false)
  return <><Button variant="outline" onClick={()=>setOpen(true)}>{label}</Button><Dialog open={open} onOpenChange={setOpen}><DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-4xl" aria-describedby={undefined}><DialogHeader><DialogTitle>{label}</DialogTitle></DialogHeader>{open&&<EvaluationAuthor initialSpec={spec} initialName={sourceName}/>}</DialogContent></Dialog></>
}

function CleanupPanel({cleanup,identity}:{cleanup:OutcomeAxis|undefined;identity:Created|undefined}) {
  const [open,setOpen]=React.useState(false),[reason,setReason]=React.useState(""),[unknown,setUnknown]=React.useState(false)
  const send=useChillMutation(),cache=useQueryClient()
  const current=useChillQuery<Run>(`v1/evaluations/${encodeURIComponent(identity?.name??"_")}`,{enabled:open&&!!identity,decoder:parseSchedulerJSON})
  if(cleanup?.status!=="Failed")return null
  async function release() {
    if(!identity||!reason.trim()||send.isPending||unknown||send.isSuccess||current.data?.uid!==identity.uid)return
    try {
      await evaluationOperation(()=>send.mutateAsync({path:`v1/evaluations/${encodeURIComponent(identity.name)}/release`,method:"POST",body:evaluationReleaseBody(identity.uid,reason)}),()=>current.refetch())
      await Promise.allSettled([current.refetch(),cache.invalidateQueries({queryKey:["chill","v1/evaluations/active"]})])
    } catch(error){if(error instanceof ApiError&&(error.unknownOutcome||error.status===0))setUnknown(true)}
  }
  return <div className="space-y-2"><div className="overflow-x-auto"><table className="w-full text-left text-sm"><caption className="text-left">정리 잔여 자원</caption><thead><tr><th>종류</th><th>이름</th><th>사유</th></tr></thead><tbody>{cleanup.residuals?.map((row,index)=><tr key={`${row.kind}/${row.name}/${index}`} className="border-t"><td className="py-2">{row.kind}</td><th className="break-all font-normal">{row.name}</th><td>{row.reason}</td></tr>)}{!cleanup.residuals?.length&&<tr><td colSpan={3}>미기록</td></tr>}</tbody></table></div><Button variant="outline" disabled={!identity} onClick={()=>setOpen(true)}>release 확인</Button><Dialog open={open} onOpenChange={setOpen}><DialogContent aria-describedby={undefined}><DialogHeader><DialogTitle>release 확인</DialogTitle></DialogHeader><label className="grid gap-2 text-sm">확인 사유<Input value={reason} onChange={event=>setReason(event.target.value)} required disabled={send.isPending||unknown||send.isSuccess}/></label><span className="text-sm" title="실제 반환이 관측될 때만 풀이 해제됩니다. 실패 판정은 유지됩니다.">운영자 확인</span><Failure error={send.error}/><Failure error={current.error}/>{current.data&&current.data.uid!==identity?.uid&&<strong role="alert">UID 변경</strong>}{(unknown||send.isSuccess)&&<strong role="status">{unknown?"결과 미확인":"확인 접수"}</strong>}<Button disabled={!identity||!reason.trim()||send.isPending||unknown||send.isSuccess||current.data?.uid!==identity?.uid} onClick={()=>void release()}>확인 제출</Button>{unknown&&<Button variant="outline" onClick={()=>void current.refetch()}>다시 조회</Button>}</DialogContent></Dialog></div>
}

export function EvaluationCleanup({run}:{run:Experiment}) {
  return <CleanupPanel cleanup={run.outcome?.cleanup} identity={run.definition?.run}/>
}

function BlockedCleanup({run}:{run:Run}) {
  const publication=useChillQuery<Experiment>(experimentPath(run.publicationID??""),{enabled:!!run.publicationID,decoder:decodeExperiment})
  return <details className="mt-2"><summary>풀 묶임</summary>{publication.error&&<Failure error={publication.error}/>}<CleanupPanel cleanup={publication.data?.outcome?.cleanup??{status:"Failed"}} identity={{name:run.name,uid:run.uid}}/></details>
}

function RunActions({run,refresh}:{run:Run;refresh:()=>Promise<unknown>}) {
  const [edit,setEdit]=React.useState(false),[stop,setStop]=React.useState(false),[unknown,setUnknown]=React.useState(false),send=useChillMutation()
  const actions=evaluationActions(run.phase)
  async function remove() {
    if(!run.uid||send.isPending||unknown||send.isSuccess)return
    try {await evaluationOperation(()=>send.mutateAsync({path:`v1/evaluations/${encodeURIComponent(run.name)}?${new URLSearchParams({uid:run.uid})}`,method:"DELETE"}),refresh);await refresh()}
    catch(error){if(error instanceof ApiError&&(error.unknownOutcome||error.status===0))setUnknown(true)}
  }
  return <div className="space-y-2"><div className="flex flex-wrap gap-2">{actions.edit&&<Button variant="outline" onClick={()=>setEdit(true)}>수정</Button>}<EvaluationLaunch spec={run.spec} sourceName={run.name} label="다시 실행"/><Button variant="outline" onClick={()=>setStop(true)}>{actions.remove}</Button><Dialog open={edit} onOpenChange={setEdit}><DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-4xl" aria-describedby={undefined}><DialogHeader><DialogTitle>실험 수정</DialogTitle></DialogHeader>{edit&&<EvaluationAuthor initialSpec={run.spec} existing={{name:run.name,uid:run.uid}} onSubmitted={()=>{setEdit(false);void refresh()}}/>}</DialogContent></Dialog><Dialog open={stop} onOpenChange={setStop}><DialogContent aria-describedby={undefined}><DialogHeader><DialogTitle>{run.name} · {actions.remove} 확인</DialogTitle></DialogHeader><span className="break-all text-sm">UID {run.uid}</span><Failure error={send.error}/>{(unknown||send.isSuccess)&&<strong role="status">{unknown?"결과 미확인":`${actions.remove} 접수`}</strong>}<Button disabled={!run.uid||send.isPending||unknown||send.isSuccess} onClick={()=>void remove()}>확인</Button>{unknown&&<Button variant="outline" onClick={()=>void refresh()}>다시 조회</Button>}</DialogContent></Dialog></div>{actions.release&&<BlockedCleanup run={run}/>}</div>
}

export function EvaluationsPage() {
  const params=useSearchParams(),name=params.get("name")??""
  const list=useChillQuery<{items:Run[]}>("v1/evaluations",{poll:true,decoder:parseSchedulerJSON})
  const detail=useChillQuery<Run>(`v1/evaluations/${encodeURIComponent(name||"_")}`,{enabled:!!name,poll:true,decoder:parseSchedulerJSON})
  const rows=name?(detail.data?[detail.data]:[]):list.data?.items??[]
  async function refresh(){await Promise.allSettled([list.refetch(),...(name?[detail.refetch()]:[])])}
  return <Screen width="data" title="평가 실행" actionPlacement="header" action={<div className="flex flex-wrap gap-2"><Button variant="outline" asChild><Link href="/experiments">게시 결과</Link></Button><Button variant="outline" asChild><Link href="/evaluations/load-scenarios">부하 정의</Link></Button><EvaluationLaunch/>{name&&<Button variant="outline" asChild><Link href="/evaluations">목록</Link></Button>}</div>}><QueryGate query={name?detail:list}><section className="overflow-x-auto" aria-label="평가 실행 목록"><table className="w-full text-left text-sm"><thead><tr><th>이름</th><th>상태</th><th>시작 (UTC)</th><th>종료 (UTC)</th><th>게시</th><th>조작</th></tr></thead><tbody>{rows.map(run=><tr key={run.uid} className="border-t"><th className="p-2 font-normal"><Link className="underline" href={`/evaluations?${new URLSearchParams({name:run.name})}`}>{run.name}</Link></th><td><span title={run.phase} className={`inline-flex whitespace-nowrap rounded-full border px-2 py-1 text-xs ${run.phase==="Blocked"?"border-destructive text-destructive":""}`}>{phases[run.phase]??run.phase}</span></td><td>{run.startedAt??"—"}</td><td>{run.finishedAt??"—"}</td><td>{run.publicationID?<Link className="underline" href={`/experiments?${new URLSearchParams({a:run.publicationID})}`}>게시 결과</Link>:"미게시"}</td><td className="p-2"><RunActions run={run} refresh={refresh}/></td></tr>)}{!rows.length&&<tr><td colSpan={6}>평가 없음</td></tr>}</tbody></table></section>{name&&detail.data&&<section className="space-y-3" aria-label="평가 상세"><table className="w-full text-left text-sm"><thead><tr><th>조건</th><th>상태</th><th>사유</th></tr></thead><tbody>{detail.data.conditions?.map((condition,index)=><tr key={`${condition.type}/${index}`} className="border-t"><th className="py-2 font-normal">{condition.type}</th><td>{condition.status}</td><td>{condition.reason??"—"}</td></tr>)}</tbody></table><SourceJSON inline title="실험 정의" value={detail.data.spec}/></section>}</QueryGate></Screen>
}
