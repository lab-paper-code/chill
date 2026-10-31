"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { formatMeasurementTime } from "@/lib/measurements"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Screen } from "@/components/screen"
import { QueryGate } from "@/components/read-region"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog"
import { ApiError, useChillMutation, useChillQuery } from "@/lib/client"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { evaluationPreflight, evaluationSameSpec, type EvaluationPreflight } from "@/lib/evaluation-request"
import { object, records, patchSpec, specJSON, copyAuthorDraft, workloadChanges, adoptWorkloads, canDeleteLoad, canCreateLoad, renameLoadWorkload, removeLoadWorkload, evaluationReplacement, type LoadScenario, type Spec } from "@/lib/evaluation-author"
import { AuthorSelect, AuthorField, AuthorJSON, AuthorHelp, EvidencePicker } from "@/features/evaluation-author"

const base="v1/evaluations/load-scenarios"
const endpoint=(item:{name:string;uid:string})=>`${base}/${encodeURIComponent(item.name)}?${new URLSearchParams({uid:item.uid})}`
const failure=(error:unknown)=>error instanceof ApiError?error.unknownOutcome?"결과 미확인":error.status===403?"권한 없음(실험 운영자)":`${error.message} · ${error.body}`:error instanceof Error?error.message:"요청 실패"
function PartsPreview({spec}:{spec:Spec}) {
  const parts=records(spec.parts),workloads=records(spec.workloads),total=parts.reduce((sum,part)=>sum+(typeof part.durationSeconds==="number"&&part.durationSeconds>0?part.durationSeconds:0),0)
  return <div className="grid gap-3" aria-label="구간 길이·요청률 미리보기">{workloads.map((workload,i)=>{
    const name=String(workload.name??""),max=Math.max(0,...parts.map(part=>typeof object(part.rates)[name]==="number"?Number(object(part.rates)[name]):0)),alphaMax=Math.max(0,...parts.map(part=>typeof object(part.alpha)[name]==="number"?Number(object(part.alpha)[name]):0))
    return <div key={i} className="grid gap-1"><span className="text-sm">{name}</span><div className="flex h-20 items-end gap-px border-b" role="img" aria-label={`${name} · 구간 폭: s, 높이: 요청/s 또는 α`}>{parts.map((part,index)=>{
      const alpha=part.alpha!==undefined,rawValue=object(alpha?part.alpha:part.rates)[name],value=typeof rawValue==="number"?rawValue:undefined,duration=typeof part.durationSeconds==="number"&&part.durationSeconds>0?part.durationSeconds:0,ceiling=alpha?alphaMax:max
      return <div key={index} className="flex h-full items-end" style={{width:total?`${duration/total*100}%`:0}}><div className={`w-full ${alpha?"border border-primary bg-primary/20":"bg-primary"}`} style={{height:value!==undefined&&ceiling>0?`${Math.max(0,value/ceiling)*100}%`:0}} title={`${String(part.id)} · ${part.durationSeconds??"미기록"} s · ${value??"미기록"} ${alpha?"α":"요청/s"}`}/></div>
    })}</div></div>
  })}</div>
}
function LoadSpecEditor({spec,onChange,onValidityChange}:{spec:Spec;onChange:(spec:Spec)=>void;onValidityChange:(valid:boolean)=>void}) {
  const [tab,setTab]=useState("parts"),[invalid,setInvalid]=useState(false),parts=records(spec.parts),workloads=records(spec.workloads)
  return <div className="grid gap-4"><div className="flex flex-wrap gap-2" role="group" aria-label="부하 편집 항목">{[["parts","구간"],["workloads","Workload·도착 분포"],["inputs","입력 객체"],["json","JSON"]].map(([key,label])=><Button key={key} type="button" disabled={invalid&&key!==tab} aria-pressed={tab===key} variant={tab===key?"default":"outline"} onClick={()=>setTab(key)}>{label}</Button>)}</div>
    {tab==="json"&&<AuthorJSON label="부하 spec JSON" value={spec} onChange={onChange} onValidityChange={valid=>{setInvalid(!valid);onValidityChange(valid)}}/>}
    {tab==="parts"&&<><PartsPreview spec={spec}/><div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th>구간</th><th>길이 (s)</th><th>단위</th>{workloads.map((w,i)=><th key={i}>{String(w.name)} (요청/s 또는 α)</th>)}<th>조작</th></tr></thead><tbody>{parts.map((part,index)=>{
      const mode=part.alpha!==undefined?"alpha":"rates"
      return <tr key={index}><td className="min-w-28 p-1"><AuthorField label="구간 ID" compact spec={spec} path={["parts",index,"id"]} onChange={onChange}/></td><td className="min-w-28 p-1"><AuthorField label="길이 (s)" compact spec={spec} path={["parts",index,"durationSeconds"]} numeric onChange={onChange}/></td><td className="min-w-28 p-1"><AuthorSelect label="부하 단위" compact value={mode} onChange={value=>{const next={...part,[value]:structuredClone(object(part[mode]))};delete next[value==="alpha"?"rates":"alpha"];onChange(patchSpec(spec,["parts",index],next))}}><option value="rates">요청/s</option><option value="alpha">α</option></AuthorSelect></td>{workloads.map((w,i)=><td key={i} className="min-w-28 p-1"><AuthorField label={String(w.name)} compact spec={spec} path={["parts",index,mode,String(w.name)]} numeric onChange={onChange}/></td>)}<td><Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["parts"],parts.filter((_,i)=>i!==index)))}>삭제</Button></td></tr>
    })}</tbody></table></div><Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["parts"],[...parts,{id:"",rates:{}}]))}>구간 추가</Button></>}
    {tab==="workloads"&&<><div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th>이름</th><th>UID</th><th>세대</th><th>도착 분포</th><th>조작</th></tr></thead><tbody>{workloads.map((w,index)=><tr key={index}><td className="min-w-40 p-1"><label className="grid gap-1 text-sm"><span className="sr-only">Workload 이름</span><Input value={String(w.name??"")} onChange={event=>onChange(renameLoadWorkload(spec,index,event.target.value))}/></label></td><td className="min-w-48 p-1"><AuthorField label="Workload UID" compact spec={spec} path={["workloads",index,"uid"]} onChange={onChange}/></td><td className="min-w-24 p-1"><AuthorField label="세대" compact spec={spec} path={["workloads",index,"generation"]} numeric onChange={onChange}/></td><td className="min-w-40 p-1"><AuthorField label="도착 분포" compact spec={spec} path={["arrivalDistribution",String(w.name)]} onChange={onChange}/></td><td><Button type="button" variant="outline" onClick={()=>onChange(removeLoadWorkload(spec,index))}>삭제</Button></td></tr>)}</tbody></table></div><Button type="button" variant="outline" onClick={()=>onChange(patchSpec(spec,["workloads"],[...workloads,{name:"",uid:""}]))}>Workload 추가</Button>{workloads.map((w,i)=><EvidencePicker key={i} label={`${String(w.name)} trace`} value={object(spec.traceRefs)[String(w.name)]} onChange={value=>onChange(patchSpec(spec,["traceRefs",String(w.name)],value))}/>)}</>}
    {tab==="inputs"&&[["seedRef","난수 seed"],["payloadManifestRef","입력 번들"],["rateBasisRef","요청률 기준"],["kappaRef","분배 계수"]].map(([key,label])=><EvidencePicker key={key} label={label} value={spec[key]} onChange={value=>onChange(patchSpec(spec,[key],value))}/>)}
  </div>
}
function LoadEditor({source,editing,onSaved,onConfirmed}:{source?:LoadScenario;editing:boolean;onSaved:()=>Promise<unknown>;onConfirmed?:(value:{name:string;uid:string})=>void}) {
  const draft=source?copyAuthorDraft(source):{name:"",spec:{workloads:[],parts:[],arrivalDistribution:{}}},[name,setName]=useState(editing?source?.name??"":draft.name),[spec,setSpec]=useState<Spec>(draft.spec),[valid,setValid]=useState(true)
  const [answer,setAnswer]=useState<EvaluationPreflight|null>(null),[checked,setChecked]=useState(""),[live,setLive]=useState<unknown>(null),[selected,setSelected]=useState<string[]>([]),[message,setMessage]=useState(""),[unknown,setUnknown]=useState(false),[uid,setUID]=useState(editing?source?.uid??"":"")
  const [replacementLost,setReplacementLost]=useState(false),[inUse,setInUse]=useState<string[]>(source?.inUse??[])
  const mutation=useChillMutation(),detail=useChillQuery<LoadScenario>(`${base}/${encodeURIComponent(name)}`,{enabled:false,decoder:parseSchedulerJSON}),body=specJSON({name,spec}),changes=workloadChanges(spec,live)
  const replacing=editing&&!replacementLost,blocked=mutation.isPending||!valid||!name.trim()||(!editing&&name===source?.name)||(replacing&&inUse.length>0)
  async function reconcile() {
    const response=await detail.refetch();await onSaved()
    if(replacing) {
      const result=evaluationReplacement(uid,spec,response.error?.status??(response.data?200:0),response.data)
      setAnswer(null)
      if(result.state==="updated"){setUID(result.uid);setMessage(`수정 확인 · ${result.uid}`);setUnknown(false);setInUse(response.data?.inUse??[]);onConfirmed?.({name,uid:result.uid})}
      else if(result.state==="unchanged"){setMessage("미변경 · 수정 실패");setUnknown(false)}
      else if(result.state==="deleted"){setMessage("삭제됨·재생성 필요");setUnknown(false);setReplacementLost(true)}
      else {setMessage("결과 미확인");setUnknown(true)}
    } else if(!response.error&&response.data&&evaluationSameSpec(response.data.spec,spec)) {
      setUID(response.data.uid);setMessage(`저장 확인 · ${response.data.uid}`);setUnknown(false);setAnswer(null);onConfirmed?.({name,uid:response.data.uid})
    } else if(response.error?.status===404){setMessage(replacementLost?"삭제됨·재생성 필요":"생성 미확인 · 재점검 필요");setUnknown(false);setAnswer(null)}
    else {setMessage("결과 미확인");setUnknown(true)}
  }
  async function preflight() {
    setAnswer(null);setSelected([]);setLive(null);setMessage("")
    try {const result=await mutation.mutateAsync({path:`${base}/preflight`,method:"POST",body}),next=evaluationPreflight(result.data);if(!next)throw new Error("사전점검 응답 형식 확인");setAnswer(next);setLive(object(result.data).liveWorkloads);setChecked(body)}catch(error){setMessage(failure(error))}
  }
  async function save() {
    if(!canCreateLoad(answer,checked,body,unknown)||blocked)return
    try {
      const result=await mutation.mutateAsync(replacing?{path:endpoint({name,uid}),method:"PUT",body:specJSON({spec})}:{path:base,method:"POST",body}),saved=object(result.data)
      if(typeof saved.uid!=="string"||!saved.uid){setUnknown(true);await reconcile();return}
      setUID(saved.uid);setReplacementLost(false);setMessage(`저장됨 · ${saved.uid}`);setAnswer(null);await onSaved();onConfirmed?.({name,uid:saved.uid})
    } catch(error) {
      setMessage(failure(error))
      if(error instanceof ApiError&&error.status===403){setAnswer(null);return}
      if(error instanceof ApiError&&error.status===409) {
        const latest=await detail.refetch();await onSaved()
        if(!latest.error&&latest.data&&latest.data.inUse?.length){setInUse(latest.data.inUse);setMessage("사용 중")}
      } else if(replacing||error instanceof ApiError&&(error.unknownOutcome||error.status===0)){setUnknown(true);await reconcile()}
    }
  }
  return <div className="grid gap-4"><label className="grid gap-1 text-sm">부하 설정 이름<Input value={name} disabled={editing||unknown} onChange={event=>setName(event.target.value)}/></label>{!editing&&name===source?.name&&<span role="alert">새 이름 필요</span>}
    {inUse.length>0&&<div className="flex flex-wrap gap-2" role="status"><span>사용 중</span>{inUse.map(run=><Link key={run} className="rounded-full border px-2 py-1 text-xs" href={`/experiments?name=${encodeURIComponent(run)}`}>{run}</Link>)}</div>}
    <fieldset disabled={unknown||mutation.isPending}><LoadSpecEditor spec={spec} onChange={setSpec} onValidityChange={setValid}/></fieldset>
    {changes.length>0&&<div className="grid gap-3 overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th>반영</th><th>Workload</th><th>정의 UID</th><th>현재 UID</th><th>정의 세대</th><th>현재 세대</th></tr></thead><tbody>{changes.map(change=><tr key={change.name}><td><input type="checkbox" aria-label={`${change.name} 현재 식별자 반영`} checked={selected.includes(change.name)} onChange={event=>setSelected(event.target.checked?[...selected,change.name]:selected.filter(x=>x!==change.name))}/></td><th>{change.name}</th><td>{String(change.before.uid)}</td><td>{String(change.after.uid)}</td><td>{String(change.before.generation)}</td><td>{String(change.after.generation)}</td></tr>)}</tbody></table><Button type="button" variant="outline" disabled={!selected.length} onClick={()=>{setSpec(adoptWorkloads(spec,changes,selected));setAnswer(null);setLive(null);setSelected([])}}>선택한 현재 식별자 반영</Button></div>}
    {answer&&<table className="w-full text-left text-sm"><thead><tr><th>검사</th><th>상태</th><th>사유</th></tr></thead><tbody>{answer.checks.map(check=><tr key={check.id}><th>{check.id}</th><td>{check.passed?"통과":"실패"}</td><td>{check.reason??"미기록"}</td></tr>)}</tbody></table>}
    {message&&<output className="break-all text-sm" role="status">{message}</output>}<div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={blocked||unknown} onClick={()=>void preflight()}>사전점검</Button><Button type="button" disabled={blocked||!canCreateLoad(answer,checked,body,unknown)} onClick={()=>void save()}>{replacementLost?"다시 만들기":editing?"수정":"생성"}</Button>{unknown&&<Button type="button" variant="outline" onClick={()=>void reconcile()}>결과 다시 조회</Button>}</div>
  </div>
}
export function LoadScenarioPage() {
  const query=useChillQuery<{items:LoadScenario[]}>(base,{decoder:parseSchedulerJSON})
  const mutation=useChillMutation()
  const params=useSearchParams(),[search,setSearch]=useState("")
  const [remove,setRemove]=useState<LoadScenario|null>(null)
  const [confirmation,setConfirmation]=useState("")
  const [message,setMessage]=useState("")
  const [uncertain,setUncertain]=useState<LoadScenario|null>(null)
  const view=params.get("name")??""
  const detail=useChillQuery<LoadScenario>(`${base}/${encodeURIComponent(uncertain?.name??remove?.name??"")}`,{enabled:false,decoder:parseSchedulerJSON})
  const definition=useChillQuery<LoadScenario>(`${base}/${encodeURIComponent(view)}`,{enabled:!!view,decoder:parseSchedulerJSON})
  async function checkDeletion(item=uncertain) {
    if(!item)return
    const result=await detail.refetch()
    await query.refetch()
    if(result.error?.status===404||!result.error&&result.data&&result.data.uid!==item.uid) {
      setMessage("원래 UID 삭제 확인");setUncertain(null)
    } else setMessage(result.data&&!result.error?`원래 UID 남음 · ${result.data.uid}`:"삭제 결과 조회 실패")
  }
  async function deleteLoad() {
    if(!remove||!canDeleteLoad(remove)||confirmation!==remove.name||uncertain)return
    try {
      await mutation.mutateAsync({path:endpoint(remove),method:"DELETE"})
      setMessage("삭제 요청됨");setRemove(null);await query.refetch()
    } catch(error) {
      setMessage(failure(error))
      if(error instanceof ApiError&&(error.unknownOutcome||error.status===0)) {
        setUncertain(remove);await checkDeletion(remove);setRemove(null)
      } else if(error instanceof ApiError&&error.status===409) {
        setMessage("사용 중");await query.refetch()
      }
    }
  }
  return <Screen title="부하 설정" width="data" actionPlacement="header" action={<Button asChild><Link href="/experiments/load-scenarios/new">새 부하 설정</Link></Button>}>
    <nav className="flex flex-wrap items-center gap-4 text-sm"><Link href="/experiments">실행</Link><Link href="/experiments/results">결과</Link><AuthorHelp/><Input aria-label="부하 설정 검색" value={search} onChange={event=>setSearch(event.target.value)} className="max-w-60"/></nav>
    <section className="grid gap-3" aria-label="부하 설정 목록">
      <QueryGate query={query}>{null}</QueryGate>
      <div className="overflow-x-auto"><table className="w-full text-left text-sm">
        <thead><tr><th>이름</th><th>UID</th><th>사용 중 실험</th><th>생성 시각</th><th>조작</th></tr></thead>
        <tbody>{query.data?.items.filter(item=>item.name.includes(search)).map(item=><tr key={item.uid} className="border-b">
          <th className="py-3"><Link className="underline" href={`/experiments/load-scenarios?${new URLSearchParams({name:item.name,uid:item.uid})}`}>{item.name}</Link></th><td className="break-all">{item.uid}</td>
          <td>{Array.isArray(item.inUse)?item.inUse.length?<div className="flex flex-wrap gap-1">{item.inUse.map(run=><Link className="rounded-full border px-2 py-1 text-xs" href={`/experiments?name=${encodeURIComponent(run)}`} key={run}>{run}</Link>)}</div>:"없음":"미기록"}</td>
          <td>{item.createdAt?formatMeasurementTime(item.createdAt):"미기록"}</td><td><div className="flex flex-wrap gap-2">
            <Button variant="outline" asChild><Link href={`/experiments/load-scenarios/new?${new URLSearchParams({copy:item.name})}`}>복사</Link></Button>
            <Button variant="outline" disabled={!canDeleteLoad(item)} asChild><Link aria-disabled={!canDeleteLoad(item)} tabIndex={canDeleteLoad(item)?0:-1} className={!canDeleteLoad(item)?"pointer-events-none":""} href={`/experiments/load-scenarios/${encodeURIComponent(item.name)}/edit?${new URLSearchParams({uid:item.uid})}`}>수정</Link></Button>
            <Button variant="outline" disabled={!canDeleteLoad(item)||!!uncertain} onClick={()=>{setRemove(item);setConfirmation("")}}>삭제</Button>
          </div></td>
        </tr>)}</tbody>
      </table></div>
      {query.data?.items.length===0&&<span role="status">부하 설정 없음</span>}
      {message&&<output className="break-all text-sm" role="status">{message}</output>}
      {uncertain&&<Button variant="outline" onClick={()=>void checkDeletion()}>삭제 결과 다시 조회</Button>}
    </section>
    <Sheet open={!!view} onOpenChange={open=>{if(!open)window.history.replaceState(null,"","/experiments/load-scenarios")}}><SheetContent className="overflow-y-auto sm:max-w-3xl" aria-describedby={undefined}>
      <SheetHeader><SheetTitle>{view}</SheetTitle></SheetHeader>
      <QueryGate query={definition}>{definition.data&&<div className="space-y-4 p-4"><PartsPreview spec={definition.data.spec}/><table className="w-full text-sm"><thead><tr><th>사용 중 실행</th></tr></thead><tbody>{definition.data.inUse?.map(run=><tr key={run}><td><Link href={`/experiments?name=${encodeURIComponent(run)}`}>{run}</Link></td></tr>)}</tbody></table><details><summary>정의 원본</summary><pre className="overflow-x-auto text-xs">{specJSON(definition.data.spec)}</pre></details></div>}</QueryGate>
    </SheetContent></Sheet>
    <Dialog open={!!remove} onOpenChange={open=>{if(!open)setRemove(null)}}><DialogContent>
      <DialogHeader><DialogTitle>부하 설정 삭제</DialogTitle><DialogDescription className="sr-only">이름을 확인한 후 UID 조건부 삭제</DialogDescription></DialogHeader>
      <label className="grid gap-2 text-sm">{remove?.name}<Input aria-label="삭제할 부하 설정 이름 확인" value={confirmation} onChange={event=>setConfirmation(event.target.value)}/></label>
      <Button variant="destructive" disabled={mutation.isPending||!remove||!canDeleteLoad(remove)||confirmation!==remove.name} onClick={()=>void deleteLoad()}>삭제</Button>
    </DialogContent></Dialog>
  </Screen>
}

export function LoadScenarioAuthorPage({name}:{name?:string}) {
  const params=useSearchParams(),router=useRouter(),cache=useQueryClient(),source=name??params.get("copy")??""
  const query=useChillQuery<LoadScenario>(`${base}/${encodeURIComponent(source||"_")}`,{enabled:!!source,decoder:parseSchedulerJSON})
  const identityChanged=!!name&&!!params.get("uid")&&query.data?.uid!==params.get("uid")
  const saved=()=>cache.invalidateQueries({queryKey:["chill",base]})
  const confirmed=(value:{name:string;uid:string})=>router.replace(`/experiments/load-scenarios?${new URLSearchParams(value)}`)
  return <Screen title={name?"부하 설정 수정":"새 부하 설정"} width="data" back={["/experiments/load-scenarios","부하 설정"]}>
    {source?<QueryGate query={query}>{query.data&&(identityChanged?<strong role="alert">설정 교체 확인</strong>:<LoadEditor key={query.data.uid} source={query.data} editing={!!name} onSaved={saved} onConfirmed={confirmed}/>)}</QueryGate>:<LoadEditor editing={false} onSaved={saved} onConfirmed={confirmed}/>}
  </Screen>
}
