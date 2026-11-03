"use client"

import { useEffect, useId, useState, type ReactNode } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { object, specValue, patchSpec, parseSpec, specJSON, type Spec, type EvidenceInput } from "@/lib/evaluation-author"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { useChillQuery, useSession } from "@/lib/client"
import { QueryGate } from "@/components/read-region"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
export function useUnsavedAuthor(dirty:boolean) {
  useEffect(()=>{
    if(!dirty)return
    const warn=(event:BeforeUnloadEvent)=>{event.preventDefault();event.returnValue=""}
    window.addEventListener("beforeunload",warn)
    return ()=>window.removeEventListener("beforeunload",warn)
  },[dirty])
}
const selectClass="min-h-11 w-full min-w-0 max-w-full rounded-md border bg-background px-3 text-sm"
export function AuthorSelect({label,value,onChange,children,disabled=false,compact=false}:{label:string;value:string;onChange:(value:string)=>void;children:ReactNode;disabled?:boolean;compact?:boolean}) {
  return <label className="grid min-w-0 gap-1 text-sm"><span className={compact?"sr-only":undefined}>{label}</span><select className={selectClass} value={value} disabled={disabled} onChange={event=>onChange(event.target.value)}>{children}</select></label>
}
export function AuthorField({label,spec,path,onChange,numeric=false,compact=false}:{label:string;spec:Spec;path:(string|number)[];onChange:(spec:Spec)=>void;numeric?:boolean;compact?:boolean}) {
  const value=specValue(spec,path)
  return <label className="grid min-w-0 gap-1 text-sm"><span className={compact?"sr-only":undefined}>{label}</span><Input type={numeric?"number":"text"} step={numeric?"any":undefined} value={value===undefined||value===null?"":String(value)} onChange={event=>{
    const text=event.target.value
    onChange(patchSpec(spec,path,text===""?undefined:numeric?parseSchedulerJSON(text):text))
  }}/></label>
}
export function AuthorJSON({label,value,onChange,onValidityChange,disabled=false}:{label:string;value:Spec;onChange:(value:Spec)=>void;onValidityChange?:(valid:boolean)=>void;disabled?:boolean}) {
  const canonical=specJSON(value),[draft,setDraft]=useState<{text:string;base:string}|null>(null),[error,setError]=useState("")
  return <div className="grid gap-2 text-sm"><label className="grid gap-2">{label}<textarea disabled={disabled} className="min-h-48 w-full rounded-md border bg-background p-3 font-mono text-xs" spellCheck={false} value={error?draft?.text??canonical:draft?.base===canonical?draft.text:canonical} aria-invalid={!!error} onChange={event=>{
    const text=event.target.value
    try {const next=parseSpec(text);setDraft({text,base:specJSON(next)});setError("");onChange(next);onValidityChange?.(true)}
    catch {setDraft({text,base:canonical});setError("JSON 객체 형식 확인");onValidityChange?.(false)}
  }}/></label>{error&&<><span role="alert" className="text-destructive">{error}</span><Button type="button" variant="outline" onClick={()=>{setDraft(null);setError("");onValidityChange?.(true)}}>JSON 변경 취소</Button></>}</div>
}
export function EvidencePicker({label,value,onChange}:{label:string;value:unknown;onChange:(value:Spec|undefined)=>void}) {
  const [set,setSet]=useState(""),[offsets,setOffsets]=useState([0]),[chosen,setChosen]=useState<EvidenceInput|null>(null),setList=useId()
  const {config}=useSession()
  const params=new URLSearchParams({limit:String(Math.min(25,config.maxPageSize)),offset:String(offsets.at(-1)??0)})
  params.set("kind","trace")
  if(set)params.set("set",set)
  const query=useChillQuery<{items:EvidenceInput[];page:{offset:number;limit:number;total:number;nextOffset?:number}}>(`v1/evaluations/inputs?${params}`,{decoder:parseSchedulerJSON})
  const items=query.data?.items??[],current=object(value)
  const identity=(item:Spec)=>JSON.stringify([item.store,item.key])
  const selected=current.key?identity(current):"",next=query.data?.page.nextOffset
  const selectedItem=items.find(item=>identity(item)===selected)??(chosen&&identity(chosen)===selected?chosen:current)
  return <div className="grid min-w-0 gap-2 sm:grid-cols-2">
    <label className="grid min-w-0 gap-1 text-sm">부하 설정 이름<Input list={setList} value={set} onChange={event=>{setSet(event.target.value);setOffsets([0])}}/><datalist id={setList}>{[...new Set(items.map(item=>item.set).filter(Boolean))].map(value=><option key={value} value={value}/>)}</datalist></label>
    <div className="min-w-0 sm:col-span-2"><AuthorSelect label={label} value={selected} onChange={key=>{const item=items.find(item=>identity(item)===key);setChosen(item??null);onChange(item?{...current,store:item.store,key:item.key,size:item.size}:undefined)}}>
      <option value="">미선택</option>{selected&&!items.some(item=>identity(item)===selected)&&<option value={selected}>{String(selectedItem.name||current.key)}</option>}
      {[...new Set(items.map(item=>item.set??""))].map(group=><optgroup key={group} label={group||"묶음 미기록"}>{items.filter(item=>(item.set??"")===group).map(item=><option key={identity(item)} value={identity(item)}>{item.name||item.key}</option>)}</optgroup>)}
    </AuthorSelect></div>
    <div className="flex gap-2 sm:col-span-2"><Button type="button" variant="outline" disabled={offsets.length===1||query.isFetching} onClick={()=>setOffsets(offsets.slice(0,-1))}>이전</Button><Button type="button" variant="outline" disabled={next===undefined||query.isFetching} onClick={()=>{if(next!==undefined)setOffsets([...offsets,next])}}>다음</Button></div>
    <div className="min-w-0 sm:col-span-2"><QueryGate query={query}>{null}</QueryGate>{query.data&&<table className="w-full text-left text-sm tabular-nums"><tbody><tr><th className="font-normal">전체 입력</th><td className="text-right">{query.data.page.total}</td></tr><tr><th className="font-normal">현재 입력</th><td className="text-right">{items.length}</td></tr></tbody></table>}{selected&&<><table className="w-full text-left text-sm tabular-nums"><tbody><tr><th className="font-normal">선택 입력 크기 (B)</th><td className="text-right">{String(current.size??"미기록")}</td></tr></tbody></table><details className="min-w-0"><summary className="min-h-11 cursor-pointer text-sm">선택 입력 원문</summary><pre className="max-w-full overflow-x-auto whitespace-pre-wrap break-all text-xs">{specJSON(current)}</pre></details></>}</div>
  </div>
}
export function AuthorHelp() {
  return <TooltipProvider><Tooltip><TooltipTrigger asChild><Button type="button" variant="ghost">편집 도움말</Button></TooltipTrigger><TooltipContent className="max-w-sm"><table className="w-full text-left text-xs"><caption className="sr-only">작성 제약</caption><tbody>{[["점검 기준","서버 사전점검"],["게시 복사","새 이름"],["복사에서 제외됨","옛 계약에서 삭제된 필드 경로만 표시·제외해요. stages[0]은 첫 단계예요. 원본 실행·게시·부하는 변경하지 않아요. 누락된 현재 필수 입력은 직접 채운 뒤 재점검해요."],["수정 식별자","같은 이름 · 새 UID"],["사용 중 부하","수정·삭제 제한"],["현재 식별자 반영","선택 사항 · 반영 후 재점검"],["원문 편집","기존 필드·정밀 정수 보존"],["운영 설정 패치","운영 설정과 같은 항목·단위예요. 시간 항목은 ms로 입력하고 원문 JSON에는 ns로 저장해요."],["단계 패치","미설정·null은 전체 실험 설정 상속, 빈 객체 {}도 단계 패치로 전체 대체해요. 항목을 모두 제거해도 빈 대체 패치는 유지돼요. 상속하려면 패치 미설정을 선택해요."],["패치 원문","JSON 객체를 그대로 전달하며 서버 사전점검으로 검증해요."],["미리보기 폭","구간 길이"],["미리보기 높이","절대 요청률 (req/s)"]].map(([label,value])=><tr key={label}><th className="p-1 font-normal">{label}</th><td className="p-1">{value}</td></tr>)}</tbody></table></TooltipContent></Tooltip></TooltipProvider>
}
