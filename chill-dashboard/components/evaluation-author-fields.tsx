"use client"

import { useEffect, useState, type ReactNode } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { specValue, patchSpec, parseSpec, specJSON, type Spec } from "@/lib/evaluation-author"
import { parseSchedulerJSON } from "@/lib/scheduler"
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
