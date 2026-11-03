"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient, type QueryClient } from "@tanstack/react-query"
import { useChillQuery, useChillMutation, useSession, ApiError, type ChillMutationInput, type RelayResponse } from "@/lib/client"
import type { OperationResult, PoolsView, Routing, SchedulerSettings, SchedulerSettingsChange, SchedulerControllerState, SchedulerObservationPage, WireInteger } from "@/lib/read-types"
import { millisecondsToNanos, nanosToMilliseconds, wireInteger, marginMilliseconds, preciseJSONSupported, parseSchedulerJSON, stringifySchedulerJSON, settingsEqual } from "@/lib/scheduler"
import { settingFields as fields, settingControls as controls, settingTargets, editableSettingKeys, type SettingField as Field } from "@/lib/scheduler-settings"
import { safeSource } from "@/lib/source-export"
import { QueryGate, SourceJSON, SourceExport } from "@/components/read-region"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DataTable } from "@/components/data-table"
import { SettingsNavigation } from "@/components/settings-navigation"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"

const onOff = (on:boolean) => on?"사용":"사용 안 함"
const reserveFraction = (text:string) => {
  const value=Number(text)
  if(!text.trim()||!Number.isFinite(value)||value<0||value>1)throw new Error("0~1 사이의 유한한 값을 적어 주세요.")
  return value
}
const autonomous = (settings:SchedulerSettings) => `${settings.autonomousWorkloads?.length??0}개`
// Nodes the planner never changes, like measurement Nodes.
export const excludedNodes = (settings:SchedulerSettings) => settings.excludedNodes?.length?settings.excludedNodes.join(", "):"없음"
const nodeList = (names:string[]) => {
  const selected=names.map(name=>name.trim()).filter(Boolean)
  if(new Set(selected).size!==selected.length)throw new Error("같은 장비 이름을 두 번 적었어요.")
  return selected
}
const mib = (bytes:WireInteger|string) => `약 ${(Number(bytes)/1048576).toLocaleString("ko-KR",{maximumFractionDigits:3})}MiB`
const time = (at:string) => new Date(at).toLocaleString("ko-KR")
function plain(field:Field,value:WireInteger):string {
  if(value==null)return "기록 없음"
  if(field.key==="servingMemoryBytes")return mib(value)
  if(!field.key.endsWith("Nanos"))return `${value}${field.unit??""}`
  const nanos=BigInt(value)
  return nanos>0n&&nanos%60000000000n===0n?`${nanos/60000000000n}분`:nanos>0n&&nanos%1000000000n===0n?`${nanos/1000000000n}초`:`${nanosToMilliseconds(nanos)}ms`
}
function changedRows(before:SchedulerSettings,after:SchedulerSettings,names:Record<string,string>={}) {
  const service=(uid:string)=>names[uid]??uid
  const rows:[string,string,string][]=[]
  for(const uid of new Set([...(before.autonomousWorkloads??[]),...(after.autonomousWorkloads??[])])) {
    const was=before.autonomousWorkloads?.includes(uid)??false,now=after.autonomousWorkloads?.includes(uid)??false
    if(was!==now)rows.push([`CHILL 자동 운영 · ${service(uid)}`,was?"켜짐":"꺼짐",now?"켜짐":"꺼짐"])
  }
  if(String(before.excludedNodes??[])!==String(after.excludedNodes??[]))rows.push(["계획 제외 장비",excludedNodes(before),excludedNodes(after)])
  for(const uid of new Set([...Object.keys(before.minimumDemand??{}),...Object.keys(after.minimumDemand??{})])) {
    const was=before.minimumDemand?.[uid],now=after.minimumDemand?.[uid]
    if(was!==now)rows.push([`최소 수요 · ${service(uid)}`,was===undefined?"미설정":`${was} req/s`,now===undefined?"미설정":`${now} req/s`])
  }
  for(const field of fields)if(String(before[field.key])!==String(after[field.key]))rows.push([field.label,plain(field,before[field.key]),plain(field,after[field.key])])
  for(const control of controls)if(before.controls[control.key]!==after.controls[control.key])rows.push([control.label,onOff(before.controls[control.key]),onOff(after.controls[control.key])])
  return rows
}
export function SettingChanges({before,after,stored=false,names={}}:{before:SchedulerSettings;after:SchedulerSettings;stored?:boolean;names?:Record<string,string>}) {
  const rows=changedRows(before,after,names)
  return rows.length?<table className="w-full text-left text-sm"><thead><tr><th className="p-2">항목</th><th className="p-2">변경 전</th><th className="p-2">변경 후</th></tr></thead><tbody>{rows.map(([label,previous,next])=><tr key={label} className="border-t"><th scope="row" className="p-2 font-normal">{label}</th><td className="break-all p-2">{previous}</td><td className="break-all p-2">{next}</td></tr>)}</tbody></table>:<p>{stored?"기록의 전후 설정이 같아요.":"바꾼 값과 현재 설정이 같아요."}</p>
}
export function SettingsSummary({settings}:{settings:SchedulerSettings}) {
  return <Facts items={[["CHILL 자동 운영 서비스",autonomous(settings)],["계획 제외 장비",excludedNodes(settings)],...Object.entries(settings.minimumDemand??{}).map(([uid,rate])=>[`최소 수요 · ${uid}`,`${rate} req/s`] as [string,string]),...fields.map(field=>[field.label,plain(field,settings[field.key])] as [string,string]),...controls.map(control=>[control.label,onOff(settings.controls[control.key])] as [string,string])]}/>
}

// The settings page is the only caller. Only editable fields cross the PUT boundary.
export async function putSchedulerSettings(mutate:(input:ChillMutationInput)=>Promise<RelayResponse>,client:QueryClient,settings:SchedulerSettings,version?:string|null) {
  const body=Object.fromEntries(editableSettingKeys.filter(key=>Object.hasOwn(settings,key)).map(key=>[key,settings[key]]))
  const response=await mutate({path:"v1/scheduler/settings",method:"PUT",body:stringifySchedulerJSON(body),...(version?{version}:{})})
  const applied=parseSchedulerJSON(response.body) as SchedulerSettingsChange
  await client.invalidateQueries({predicate:query=>typeof query.queryKey[1]==="string"&&(query.queryKey[1].startsWith("v1/scheduler")||query.queryKey[1].startsWith("v1/observations/scheduler"))})
  return {change:applied,etag:response.etag??null}
}

function FieldHelp({help}:{help:string}) {
  return <TooltipProvider><Tooltip><TooltipTrigger asChild><Button type="button" variant="ghost" aria-label="항목 도움말">도움말</Button></TooltipTrigger><TooltipContent className="max-w-sm">{help}</TooltipContent></Tooltip></TooltipProvider>
}

type ServiceChoice={uid:string;name:string}
type SettingsSnapshot={settings:SchedulerSettings;etag:string|null;declarationsKnown?:boolean}
function ServiceSettingsEdit({editKey,label,help,initial,version=null,services,loadError,disabled,changed,save}:{editKey:"autonomousWorkloads"|"minimumDemand";label:string;help:string;initial:SchedulerSettings;version?:string|null;services:ServiceChoice[];loadError?:string;disabled:boolean;changed:boolean;save:(value:string[]|Record<string,number>,source:SchedulerSettings,version:string|null)=>void}) {
  const params=useSearchParams(),router=useRouter(),open=params.get("edit")===editKey
  const [draft,setDraft]=React.useState<{settings:SchedulerSettings;version:string|null;selected:string[];demand:Record<string,string>}|null>(null),[error,setError]=React.useState("")
  // Until the first input, GET updates remain visible. Afterwards this exact settings/version pair is fixed.
  const form=draft??{settings:initial,version,selected:initial.autonomousWorkloads??[],demand:Object.fromEntries(Object.entries(initial.minimumDemand??{}).map(([uid,rate])=>[uid,String(rate)]))}
  const {selected,demand}=form
  function update(change:{selected?:string[];demand?:Record<string,string>}) {setDraft({...form,...change})}
  function setOpen(next:boolean) {
    const query=new URLSearchParams(params.toString())
    if(next)query.set("edit",editKey);else query.delete("edit")
    router[next?"push":"replace"](`/settings?${query}`,{scroll:false})
    setDraft(null);setError("")
  }
  const known=new Map(services.map(service=>[service.uid,service.name]))
  const uids=editKey==="autonomousWorkloads"?[...new Set([...services.map(service=>service.uid),...selected])]:Object.keys(demand)
  const focused=params.get("workload")
  if(focused&&uids.includes(focused))uids.splice(0,0,...uids.splice(uids.indexOf(focused),1))
  return <Sheet open={open} onOpenChange={setOpen}><SheetTrigger asChild><Button variant="outline" disabled={disabled} aria-label={`${label} 수정`}>수정{changed?" · 변경":""}</Button></SheetTrigger><SheetContent className="w-full overflow-y-auto sm:max-w-2xl" aria-describedby={undefined}><SheetHeader><SheetTitle>{label}</SheetTitle></SheetHeader>
    <form className="grid gap-4 px-4 pb-6" onSubmit={event=>{event.preventDefault();try {
      if(editKey==="autonomousWorkloads")save(selected,form.settings,form.version)
      else {
        const rates:Record<string,number>={}
        for(const [uid,text] of Object.entries(demand)) {
          const rate=Number(text)
          if(!uid||!text.trim()||!Number.isFinite(rate)||rate<0)throw new Error("최소 수요는 0 이상의 유한한 req/s로 적어 주세요.")
          rates[uid]=rate
        }
        save(rates,form.settings,form.version)
      }
      setOpen(false)
    } catch(caught) {setError(caught instanceof Error?caught.message:"값을 확인해 주세요.")}}}>
      <FieldHelp help={help}/>
      {loadError&&<p role="status">{loadError} · 저장된 서비스 항목은 유지돼요.</p>}
      <table className="w-full text-left text-sm"><thead><tr><th className="p-2">서비스</th><th className="p-2">{editKey==="autonomousWorkloads"?"CHILL 자동 운영":"최소 수요 req/s"}</th>{editKey==="minimumDemand"&&<th>작업</th>}</tr></thead><tbody>{uids.map((uid,index)=><tr key={uid} className={`border-t ${uid===focused?"bg-muted":""}`}><th scope="row" className="p-2 font-normal"><span title={uid}>{known.get(uid)??`현재 목록에 없는 서비스 ${index+1}`}</span></th>
        <td className="p-2">{editKey==="autonomousWorkloads"?<label className="flex min-h-11 items-center gap-2"><input aria-label={`${known.get(uid)??uid} CHILL 자동 운영`} type="checkbox" checked={selected.includes(uid)} onChange={event=>update({selected:event.target.checked?[...selected,uid]:selected.filter(item=>item!==uid)})}/>{selected.includes(uid)?"켜짐":"꺼짐"}</label>:<Input aria-label={`${known.get(uid)??uid} 최소 수요 req/s`} type="number" min={0} step="any" required value={demand[uid]} onChange={event=>{update({demand:{...demand,[uid]:event.target.value}});setError("")}}/>}</td>
        {editKey==="minimumDemand"&&<td><Button type="button" variant="outline" onClick={()=>{const next={...demand};delete next[uid];update({demand:next})}}>미설정으로</Button></td>}
      </tr>)}</tbody></table>
      {!uids.length&&<p>설정된 서비스 없음</p>}
      {editKey==="minimumDemand"&&<label className="grid gap-2">최소 수요 서비스 추가<select className="min-h-11 w-full rounded-md border bg-background px-2" value="" onChange={event=>{if(event.target.value)update({demand:{...demand,[event.target.value]:""}})}}><option value="">서비스 선택</option>{services.filter(service=>!Object.hasOwn(demand,service.uid)).map(service=><option key={service.uid} value={service.uid}>{service.name}</option>)}</select></label>}
      {error&&<p role="alert">{error}</p>}
      <Button type="submit" disabled={disabled}>바꾸기</Button>
    </form>
  </SheetContent></Sheet>
}

// One row, one value: the dialog edits only this value and adds it to the draft.
function EditRow({editKey,label,help,changed,initial,unit,range,save,disabled,nodeNames=[]}:{editKey:string;label:string;help:string;changed:boolean;initial:string|boolean|string[];unit?:string;range?:string;save:(value:string|boolean|string[])=>void;disabled:boolean;nodeNames?:string[]}) {
  const params=useSearchParams(),router=useRouter(),open=params.get("edit")===editKey
  const [next,setNext]=React.useState(initial),[error,setError]=React.useState(""),id=React.useId()
  function setOpen(state:boolean) {
    const query=new URLSearchParams(params.toString())
    if(state)query.set("edit",editKey);else query.delete("edit")
    router[state?"push":"replace"](`/settings?${query}`,{scroll:false})
    setNext(initial);setError("")
  }
  return <Sheet open={open} onOpenChange={setOpen}><SheetTrigger asChild><Button variant="outline" disabled={disabled} aria-label={`${label} 수정`}>수정{changed?" · 변경":""}</Button></SheetTrigger><SheetContent className="w-full overflow-y-auto sm:max-w-xl" aria-describedby={undefined}><SheetHeader><SheetTitle>{label}</SheetTitle></SheetHeader><div className="grid gap-4 px-4 pb-6"><FieldHelp help={help}/>
    <form className="grid gap-4" onSubmit={event=>{event.preventDefault();try {save(Array.isArray(next)?nodeList(next):next);setOpen(false)} catch(caught) {setError(caught instanceof Error?caught.message:"값을 확인해 주세요.")}}}>
      {Array.isArray(next)?<div className="grid gap-3"><fieldset className="flex flex-wrap gap-2"><legend className="mb-2">관측된 장비 선택</legend>{nodeNames.map(name=><label key={name} className="flex min-h-11 items-center gap-2 rounded-md border px-3"><input type="checkbox" value={name} checked={next.includes(name)} onChange={event=>setNext(event.target.checked?[...next,name]:next.filter(item=>item!==name))}/>{name}</label>)}{!nodeNames.length&&<p>관측된 장비 목록 없음 · 현재 설정은 유지돼요.</p>}</fieldset>{next.map((name,index)=>!nodeNames.includes(name)&&<div key={index} className="flex min-w-0 gap-2"><Input aria-label={`현재 목록에 없는 장비 ${index+1}`} value={name} onChange={event=>setNext(next.map((item,i)=>i===index?event.target.value:item))}/><Button type="button" variant="outline" onClick={()=>setNext(next.filter((_,i)=>i!==index))}>제외 해제</Button></div>)}</div>:typeof next==="boolean"
        ?<label className="switch"><input type="checkbox" role="switch" checked={next} onChange={event=>setNext(event.target.checked)}/>{onOff(next)}</label>
        :<div className="grid gap-2"><label htmlFor={id}>{unit?`값 (${unit})`:"값"}</label><Input id={id} autoFocus required inputMode={unit==="ms"?"decimal":"text"} value={next} onChange={event=>{setNext(event.target.value);setError("")}} aria-invalid={!!error} aria-describedby={`${id}-help`}/><span id={`${id}-help`} className="text-sm text-muted-foreground">{range}{unit==="bytes"&&/^\d+$/.test(next)?` · ${mib(next)}`:""}</span></div>}
      {error&&<p role="alert">{error}</p>}
      <Button type="submit" disabled={disabled}>바꾸기</Button>
    </form>
  </div></SheetContent></Sheet>
}

// Diff the declaration shown when the dialog opened, so unrelated UID changes stay untouched.
function RoutingEdit({initial,snapshot,disabled,apply}:{initial?:Routing;snapshot?:SchedulerControllerState["snapshot"];disabled:boolean;apply:(before:Routing,after:Routing,reason:string)=>void}) {
  const [open,setOpen]=React.useState(false),[text,setText]=React.useState(""),[reason,setReason]=React.useState(""),[error,setError]=React.useState(""),[before,setBefore]=React.useState<Routing>({}),id=React.useId()
  let routing:Routing={}
  try {
    const parsed:unknown=JSON.parse(text)
    if(parsed&&typeof parsed==="object"&&!Array.isArray(parsed)&&Object.values(parsed).every(weights=>weights&&typeof weights==="object"&&!Array.isArray(weights)))routing=parsed as Routing
  } catch {}
  const services=snapshot?.workloads?.filter(service=>service!==null)??[]
  const instances=snapshot?.instances??[]
  function update(next:Routing) {setText(JSON.stringify(next,null,2));setError("")}
  return <RowDialog label="요청 분배 바꾸기" title="요청 분배 바꾸기" open={open} onOpenChange={state=>{setOpen(state);setBefore(initial??{});setText(JSON.stringify(initial??{},null,2));setReason("");setError("")}}>
    <form className="grid gap-4" onSubmit={event=>{event.preventDefault();try {
      const parsed:unknown=JSON.parse(text)
      if(!parsed||typeof parsed!=="object"||Array.isArray(parsed))throw new Error("JSON 객체로 적어 주세요.")
      for(const [uid,weights] of Object.entries(parsed)) {
        if(!uid.trim()||!weights||typeof weights!=="object"||Array.isArray(weights))throw new Error("서비스 UID마다 가중치 객체를 적어 주세요.")
        const entries=Object.entries(weights)
        if(entries.some(([instance,weight])=>!instance.trim()||typeof weight!=="number"||!Number.isInteger(weight)||weight<0||weight>2147483647)||entries.length&&!entries.some(([,weight])=>typeof weight==="number"&&weight>0))throw new Error("가중치는 0 이상 int32 정수이고, 빈 객체가 아니면 양수 대상이 하나 이상이어야 해요.")
      }
      if(!reason.trim())throw new Error("선택 이유를 적어 주세요.")
      apply(before,parsed as Routing,reason.trim());setOpen(false)
    } catch(caught) {setError(caught instanceof Error?caught.message:"값을 확인해 주세요.")}}}>
      {Object.entries(routing).map(([uid,weights],index)=><fieldset key={index} className="grid min-w-0 gap-3 rounded-md border p-3">
        <legend className="px-1">서비스 {index+1}</legend>
        <label className="grid min-w-0 gap-2">서비스<select aria-label={`서비스 ${index+1}`} className="min-h-11 w-full min-w-0 rounded-md border bg-background px-2" value={uid} onChange={event=>{
          const next={...routing};delete next[uid];next[event.target.value]=weights;update(next)
        }}>
          {!services.some(service=>service.uid===uid)&&<option value={uid}>{uid||"서비스 선택"}</option>}
          {services.map(service=><option key={service.uid} value={service.uid} disabled={service.uid!==uid&&Object.hasOwn(routing,service.uid)}>{service.name}</option>)}
        </select></label>
        <DataTable searchable={false} data={Object.entries(weights)} columns={[
          {key:"instance",label:"인스턴스",value:([instance,weight])=><select aria-label={`서비스 ${index+1} 인스턴스 ${Object.keys(weights).indexOf(instance)+1}`} className="min-h-11 w-full min-w-0 rounded-md border bg-background px-2" value={instance} onChange={event=>{const next={...weights};delete next[instance];next[event.target.value]=weight;update({...routing,[uid]:next})}}>
            {!instances.some(item=>item.id===instance)&&<option value={instance}>{instance||"인스턴스 선택"}</option>}
            {instances.filter(item=>item.workloadUID===uid||item.id===instance).map((item,i)=><option key={item.id} value={item.id} disabled={item.id!==instance&&Object.hasOwn(weights,item.id)}>{item.node.name} · 인스턴스 {i+1}</option>)}
          </select>},
          {key:"weight",label:"가중치",numeric:true,value:([instance,weight])=><Input aria-label={`서비스 ${index+1} 가중치 ${Object.keys(weights).indexOf(instance)+1}`} className="min-w-0" type="number" required min={0} max={2147483647} step={1} value={weight??""} onChange={event=>update({...routing,[uid]:{...weights,[instance]:event.target.value===""?NaN:Number(event.target.value)}})}/>},
          {key:"remove",label:"작업",value:([instance])=><Button type="button" variant="outline" onClick={()=>{const next={...weights};delete next[instance];update({...routing,[uid]:next})}}>대상 제외</Button>},
        ]}/>
        {!Object.keys(weights).length&&<Facts items={[["요청 접수","차단"]]} />}
        <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" onClick={()=>{const instance=instances.find(item=>item.workloadUID===uid&&!Object.hasOwn(weights,item.id));if(instance)update({...routing,[uid]:{...weights,[instance.id]:1}})}} disabled={!instances.some(item=>item.workloadUID===uid&&!Object.hasOwn(weights,item.id))}>인스턴스 추가</Button>
        <Button type="button" variant="outline" onClick={()=>update({...routing,[uid]:{}})}>새 요청 차단</Button>
        <Button type="button" variant="outline" onClick={()=>{const next={...routing};delete next[uid];update(next)}}>기본 분배로</Button></div>
      </fieldset>)}
      <Button type="button" variant="outline" disabled={!services.some(service=>!Object.hasOwn(routing,service.uid))} onClick={()=>{const service=services.find(service=>!Object.hasOwn(routing,service.uid));if(service)update({...routing,[service.uid]:{}})}}>서비스 추가</Button>
      <details><summary className="min-h-11 cursor-pointer py-2">분배 원문</summary><label className="grid gap-2" htmlFor={`${id}-routing`}>분배 JSON<textarea id={`${id}-routing`} className="min-h-40 w-full min-w-0 rounded-md border p-2 font-mono text-sm" required value={text} onChange={event=>{setText(event.target.value);setError("")}} aria-invalid={!!error}/></label></details>
      <div className="grid gap-2"><label htmlFor={`${id}-reason`}>선택 이유</label><Input id={`${id}-reason`} required value={reason} onChange={event=>setReason(event.target.value)}/></div>
      {error&&<p role="alert">{error}</p>}
      <Button type="submit" disabled={disabled||!reason.trim()}>분배 적용하기</Button>
    </form>
  </RowDialog>
}


export function SchedulerSettingsPage() {
  const params=useSearchParams(),router=useRouter()
  const section=params.get("section"),category:Field["group"]|"controls"|"targets"=section==="cycle"||section==="limit"||section==="controls"||section==="capacity"||section==="advanced"?section:"targets"
  const [read,setRead]=React.useState<SettingsSnapshot|null>(null),readRef=React.useRef<SettingsSnapshot|null>(null)
  const settings=useChillQuery<SchedulerSettings>("v1/scheduler/settings",{decoder:parseSchedulerJSON,poll:true,onResponse:(response,data)=>{
    const next={settings:data,etag:response.etag??null,declarationsKnown:true};readRef.current=next;setRead(next)
  }})
  const snapshot:SettingsSnapshot|null=read??(settings.data?{settings:settings.data,etag:null}:null)
  const currentSettings=settings.data===undefined?undefined:snapshot?.settings
  const versioned=snapshot?.etag!=null
  const state=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON})
  const pools=useChillQuery<PoolsView>("v1/pools",{enabled:params.get("edit")==="excludedNodes"})
  const workloads=useChillQuery<{desired:{metadata:ServiceChoice}}[]>("v1/workloads?view=summary")
  const services=workloads.data?.map(item=>item.desired.metadata)??[]
  const serviceNames=Object.fromEntries(services.map(service=>[service.uid,service.name]))
  const knownNodeNames=[...new Set([
    ...(state.data?.snapshot?.nodes??[]).map(item=>item.node.name),
    ...(pools.data?.available??[]).map(item=>item.node.name),
    ...(pools.data?.other??[]).map(item=>item.node.name),
    ...(pools.data?.measurement??[]).map(item=>item.node.name),
    ...(pools.data?.workloads??[]).flatMap(pool=>(pool.nodes??[]).map(node=>node.name)),
  ])]
  const [baseline,setBaseline]=React.useState<SettingsSnapshot|null>(null),[draft,setDraft]=React.useState<SchedulerSettings|null>(null)
  const [reviewOpen,setReviewOpen]=React.useState(false),[checkError,setCheckError]=React.useState("")
  const [review,setReview]=React.useState<SchedulerSettings|null>(null),[latest,setLatest]=React.useState<SettingsSnapshot|null>(null)
  const [reviewing,setReviewing]=React.useState(false),[result,setResult]=React.useState<SchedulerSettingsChange|null>(null)
  const [responseError,setResponseError]=React.useState<string|null>(null)
  const [routingResults,setRoutingResults]=React.useState<OperationResult[]|null>(null),[routingError,setRoutingError]=React.useState(""),[routingPending,setRoutingPending]=React.useState(false)
  const [submittedAt,setSubmittedAt]=React.useState("")
  const [historyWindow,setHistoryWindow]=React.useState<{from:string;to:string}|null>(null)
  const [recoveryStarted,setRecoveryStarted]=React.useState(0)
  const reviewLauncher=React.useRef<HTMLButtonElement|null>(null)
  const {config}=useSession(),client=useQueryClient(),mutation=useChillMutation(),saving=React.useRef(false)
  const history=useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${new URLSearchParams(historyWindow??{from:submittedAt,to:submittedAt})}&limit=${config.pageSize}`,{decoder:parseSchedulerJSON,enabled:!!historyWindow})
  const precise=preciseJSONSupported()
  const unknown=!!responseError||(mutation.error?.unknownOutcome??false)
  const recovered=recoveryStarted>0&&settings.dataUpdatedAt>=recoveryStarted&&!settings.isFetching&&history.isSuccess&&!history.isFetching
  const blocked=routingPending||mutation.isPending||reviewing||(unknown&&!recovered)||!precise||[401,403].includes(settings.error?.status??0)
  const serverError=mutation.error?.data&&typeof mutation.error.data==="object"?mutation.error.data as {field?:string;message?:string;code?:string}:undefined
  const count=baseline&&draft?changedRows(baseline.settings,draft).length:0
  const shown=count?draft:currentSettings
  // Explicit routing operations open in the same detail view as the transitions list.
  const routing=state.data?.results?.filter(item=>["route","unroute","release"].includes(item.operation.kind)).reduce<OperationResult|undefined>((latest,item)=>!latest||Date.parse(item.startedAt)>Date.parse(latest.startedAt)?item:latest,undefined)
  const back:[string,string]=["/settings","설정"]

  // Edits accumulate on the draft; the first edit pins the baseline used for the concurrency check.
  function edit(change:(base:SchedulerSettings)=>SchedulerSettings,source=snapshot!.settings,version=snapshot!.etag) {
    const next=change(count?draft!:source)
    if(next.capacityPassingWindows!==undefined&&next.capacityWindows!==undefined&&BigInt(next.capacityPassingWindows)>BigInt(next.capacityWindows))throw new Error("용량 충족 창 수는 용량 계산 창 수 이하여야 해요.")
    if(!count)setBaseline({settings:source,etag:version})
    setDraft(next)
  }
  async function inspect(conflict=false) {
    if(blocked||!draft||!baseline)return
    setReviewOpen(true);setReviewing(true);setReview(null);setLatest(null);setCheckError("");setResult(null)
    try {
      const [current]=await Promise.all([settings.refetch(),state.refetch()])
      if(current.error||!current.data){setCheckError("현재 설정을 불러오지 못했어요. 바꾼 값은 그대로 있어요.");return}
      const refreshed=readRef.current??{settings:current.data,etag:null}
      if(conflict||baseline.etag!==refreshed.etag||!settingsEqual(baseline.settings,refreshed.settings)){setLatest(refreshed);return}
      setReview(draft)
    } finally {setReviewing(false)}
  }
  async function submit(selected:SchedulerSettings,version:string|null) {
    saving.current=true;setSubmittedAt(new Date().toISOString());setRecoveryStarted(0);setHistoryWindow(null);setResult(null);setResponseError(null)
    const previousRead=readRef.current
    try {
      const applied=await putSchedulerSettings(mutation.mutateAsync,client,selected,version)
      if(readRef.current===previousRead){
        const appliedSnapshot={settings:{...(previousRead?.settings??selected),...(applied.change.after??selected)},etag:applied.etag,declarationsKnown:previousRead?.declarationsKnown??false}
        readRef.current=appliedSnapshot;setRead(appliedSnapshot)
      }
      setResult(applied.change)
      setBaseline(null);setDraft(null)
    } catch(error) {
      if(error instanceof ApiError&&error.status===409){mutation.reset();await inspect(true)}
      else if(!(error instanceof ApiError))setResponseError("적용 응답을 해석하지 못했어요. 현재 설정과 최근 변경 기록을 확인해 주세요.")
    }
    finally {saving.current=false}
  }
  async function save() {
    if(!review||!baseline||blocked||saving.current)return
    setReviewing(true);setCheckError("")
    try {
      const current=await settings.refetch()
      if(current.error||!current.data){setCheckError("현재 설정을 불러오지 못했어요. 작성값은 유지돼요.");return}
      const refreshed=readRef.current??{settings:current.data,etag:null}
      if(baseline.etag!==refreshed.etag||!settingsEqual(baseline.settings,refreshed.settings)){setLatest(refreshed);setReview(null);return}
      const selected=review;setReview(null);setReviewOpen(false)
      await submit(selected,baseline.etag)
    } finally {setReviewing(false)}
  }
  async function applyRouting(before:Routing,after:Routing,reason:string) {
    if(blocked||saving.current)return
    saving.current=true;setRoutingPending(true);setRoutingResults([]);setRoutingError("");mutation.reset()
    try {
      for(const uid of new Set([...Object.keys(before),...Object.keys(after)])) {
        const previous=before[uid],weights=after[uid]
        if(Object.hasOwn(before,uid)&&Object.hasOwn(after,uid)&&Object.keys(previous).length===Object.keys(weights).length&&Object.entries(previous).every(([id,weight])=>Object.hasOwn(weights,id)&&weights[id]===weight))continue
        const kind=!Object.hasOwn(after,uid)?"release":Object.keys(weights).length?"route":"unroute"
        let applied:OperationResult
        try {
          const response=await mutation.mutateAsync({path:"v1/scheduler/operations",method:"POST",body:JSON.stringify({kind,workloadUID:uid,...(kind==="route"?{weights}:{}),reason})})
          applied=parseSchedulerJSON(response.body) as OperationResult
        } catch(error) {
          if(!(error instanceof ApiError)||!error.data||typeof error.data!=="object"||!("operation" in error.data))throw error
          applied=error.data as OperationResult
        }
        if(!applied.operation?.id||!applied.operation.node||typeof applied.status!=="string"||!applied.before||!applied.actual)throw new Error("작업 결과 응답을 해석하지 못했어요.")
        setRoutingResults(results=>[...(results??[]),applied])
        if(applied.status!=="complete"||applied.error)break
      }
    } catch(error) {
      setRoutingError(error instanceof ApiError?(error.unknownOutcome?"응답을 받지 못해 결과를 확인해야 해요.":error.body):error instanceof Error?error.message:"작업 결과를 확인하지 못했어요.")
    } finally {
      saving.current=false;setRoutingPending(false)
      await client.invalidateQueries({predicate:query=>typeof query.queryKey[1]==="string"&&(query.queryKey[1].startsWith("v1/scheduler")||query.queryKey[1].startsWith("v1/observations/scheduler"))})
    }
  }
  async function recover() {
    const now=Date.now();setRecoveryStarted(now)
    setHistoryWindow({from:new Date(Date.parse(submittedAt)-60000).toISOString(),to:new Date(now).toISOString()})
    await settings.refetch()
  }

  if(routingResults) {
    const failed=routingResults.some(item=>item.status!=="complete"||item.error)
    return <Screen back={back} title={routingPending?"분배 적용 중":routingError||failed?"분배 결과 미확인":routingResults.length?"분배 적용 완료":"분배 변경 없음"} action={<Button disabled={routingPending} onClick={()=>{setRoutingResults(null);setRoutingError("");mutation.reset()}}>확인</Button>}>
      {routingError&&<p role="alert">{routingError}</p>}
      {routingResults.map(item=><React.Fragment key={item.operation.id}>
        <Facts items={[["작업",item.operation.kind],["결과",item.status],["시작",time(item.startedAt)],["종료",time(item.endedAt)],...(item.error?[["오류",item.error] as [string,string]]:[])]}/>
        <Rows><Row href={`/records?${new URLSearchParams({filter:"action",id:item.operation.id,view:"current",stage:"result",node:item.operation.node.uid,startedAt:item.startedAt})}`} label="실제 변경 진행"/><RowDialog label="원본 자료 보기" title="작업 결과 원본"><SourceJSON inline value={item}/></RowDialog></Rows>
      </React.Fragment>)}
      {routingError&&<Rows><Row href="/records?filter=action" label="실제 변경 진행"/></Rows>}
    </Screen>
  }
  if(result)return <Screen back={back} title={result.appliedAt?"설정 적용 완료":"적용 결과 미확인"} action={<Button onClick={()=>setResult(null)}>확인</Button>}>
    <Facts items={[["적용 시각",result.appliedAt?time(result.appliedAt):"아직 없어요"],["확인 시각",time(result.validatedAt)],["바꾼 사람",result.actorUsername],...(result.reason?[["분배 선택 이유",result.reason] as [string,string]]:[]),["마지막으로 확인한 노드",result.affected?.map(node=>node.name).join(", ")||"없음"],...(result.error?[["오류",result.error] as [string,string]]:[])]}/>
    <Rows><Row href="/records?filter=action" label="실제 변경 진행"/><RowDialog label="원본 자료 보기" title="적용 결과 원본"><SourceJSON inline value={result}/></RowDialog></Rows>
  </Screen>

  if(mutation.error||responseError) {
    const message=mutation.error?.status===428?"버전 없음 · 현재 설정을 다시 조회한 뒤 검토해 주세요. 작성값은 유지돼요.":mutation.error?.status===409?"동시 변경 충돌 · 작성값은 유지돼요. 현재 설정을 다시 조회한 뒤 검토해 주세요.":mutation.error?.status===403?"조회 권한과 별도로 설정 변경 권한과 설정 저장소(ConfigMap) 수정 권한이 모두 필요해요.":responseError??serverError?.message??mutation.error?.body
    const field=serverError?.field&&(fields.find(item=>item.key===serverError.field)?.label??({settings:"운영 설정 전체",staticRouting:"요청 분배",reason:"분배 선택 이유"} as Record<string,string>)[serverError.field]??serverError.field)
    if(!unknown)return <Screen back={back} title={mutation.error?.status===428?"버전 없음":mutation.error?.status===409?"설정 변경 충돌":mutation.error?.status===403?"변경 권한 없음":"설정 적용 실패"} action={<Button onClick={()=>{const status=mutation.error?.status;mutation.reset();if(status===409||status===428)void inspect(status===409)}}>{mutation.error?.status===409||mutation.error?.status===428?"현재 설정 다시 조회하기":"다시 수정하기"}</Button>}>
      <Facts items={[["원인",message??"미제공"],["작성값","유지"],...(field?[["관련 항목",field] as [string,string]]:[])]}/>
    </Screen>
    const events=(history.data?.events??[]).filter(event=>event.settings)
    return <Screen back={back} title="적용 결과 미확인"
      action={recovered&&currentSettings
        ?<Button onClick={()=>{setBaseline(snapshot!);setLatest(null);setResponseError(null);mutation.reset();setHistoryWindow(null)}}>조회한 현재 값으로 다시 검토하기</Button>
        :<Button onClick={()=>void recover()} disabled={settings.isFetching||history.isFetching}>현재 설정과 최근 변경 기록 확인하기</Button>}>
      <Facts items={[["원인",message??"응답 미수신"],["작성값","유지"],["자동 재제출","차단"]]}/>
      {historyWindow&&<QueryGate query={history}>{events.length?<Facts items={events.map(event=>[time(event.at),`${event.settings!.actorUsername} · ${event.settings!.appliedAt?"적용됨":event.settings!.error??"적용 전 확인만 됐어요"}`] as [string,string])}/>:<Empty title="이 구간에 운영 설정 변경 기록이 없어요"/>}</QueryGate>}
      {historyWindow&&<Rows>
        <Row href={`/records?scope=scheduler&filter=settings&${new URLSearchParams(historyWindow)}`} label="구간 변경 기록" hint={history.data?.nextCursor?"추가 기록 있음":undefined}/>
        {recovered&&currentSettings&&<RowDialog label="현재 값과 바꾼 값 비교" title="현재 값과 바꾼 값" description="방금 조회한 현재 설정과 바꾼 값을 비교해요."><SettingChanges before={currentSettings} after={draft??currentSettings}/></RowDialog>}
      </Rows>}
    </Screen>
  }

  const editField=(field:Field)=>shown&&<EditRow key={field.key} editKey={field.key} label={field.label} help={field.help} changed={!!count&&String(baseline!.settings[field.key])!==String(shown[field.key])}
    initial={field.key.endsWith("Nanos")?nanosToMilliseconds(shown[field.key]):String(shown[field.key])} unit={field.key.endsWith("Nanos")?"ms":field.key==="servingMemoryBytes"?"bytes":field.unit}
    range={field.key==="bootstrapCapacityWaitNanos"?"0 ~ 3600000 ms":field.key==="scaleDownStabilizationNanos"||field.key==="replacementDwellNanos"?"0 이상의 밀리초, 소수점 아래 6자리까지":field.key==="capacityBisectionWidth"?"0 초과 1 이하 유한한 수":field.key==="transitionReserveFraction"||field.key==="scaleDownHeadroomFraction"?"0 ~ 1 사이 유한한 수":field.key.endsWith("Nanos")?"0보다 큰 밀리초, 소수점 아래 6자리까지":field.key==="completionMarginMS"?"0 이상의 밀리초":field.key==="servingMemoryBytes"?"0 ~ 18446744073709551615 사이 바이트 수":"1 이상의 정수"}
    save={value=>{
      // Same validation as the wire: ms with ≤6 decimals → nanos, nonnegative margin ms, uint64 memory bytes, positive int64 otherwise.
      const text=value as string,parsed=field.key.endsWith("Nanos")?millisecondsToNanos(text,field.key==="scaleDownStabilizationNanos"||field.key==="replacementDwellNanos"||field.key==="bootstrapCapacityWaitNanos"?0n:1n):field.key==="transitionReserveFraction"||field.key==="scaleDownHeadroomFraction"||field.key==="capacityBisectionWidth"?reserveFraction(text):field.key==="completionMarginMS"?marginMilliseconds(text):field.key==="servingMemoryBytes"?wireInteger(text,0n,18446744073709551615n):wireInteger(text,1n,9223372036854775807n)
      if(field.key==="capacityBisectionWidth"&&(Number(parsed)<=0||Number(parsed)>1))throw new Error("0 초과 1 이하의 유한한 값을 적어 주세요.")
      if(field.key==="bootstrapCapacityWaitNanos"&&BigInt(parsed)>3600000000000n)throw new Error("초기 용량 대기는 0~3600000 ms로 적어 주세요.")
      edit(base=>({...base,[field.key]:parsed}))
    }} disabled={blocked}/>
  const routingFailure=state.error?.data
  const routingFailureCode=routingFailure&&typeof routingFailure==="object"&&"code" in routingFailure&&typeof routingFailure.code==="string"?routingFailure.code:"오류 코드 미제공"
  const routingFailureReason=routingFailure&&typeof routingFailure==="object"&&"message" in routingFailure&&typeof routingFailure.message==="string"?routingFailure.message:"상세 사유 미제공"
  return <Screen title="설정" width="data" actionPlacement="header"
    action={count>0&&<Button ref={reviewLauncher} onClick={()=>void inspect()} disabled={blocked}>{reviewing?"현재값 조회 중":`변경 ${count}건 검토`}</Button>}>
    <SettingsNavigation active="configuration"/>
    {!precise&&<strong role="alert">큰 정수 처리 미지원 · 설정 변경 차단</strong>}
    <label className="grid max-w-sm gap-2 text-sm">운영 설정 범위<select className="min-h-11 rounded-md border bg-background px-3" value={category} onChange={event=>{const query=new URLSearchParams(params.toString());query.set("section",event.target.value);query.delete("edit");router.push(`/settings?${query}`,{scroll:false})}}>{([["targets","서비스·장비"],["cycle","판단 주기와 범위"],["limit","자원 한도"],["controls","자동 조정 항목"],["capacity","용량 설정"],["advanced","고급"]] as const).map(([value,label])=><option key={value} value={value}>{label}</option>)}</select></label>
    <QueryGate query={settings}>{shown&&<>
      <section aria-label="현재값·정의 표">{category==="targets"?<table className="w-full text-left text-sm"><thead><tr><th className="p-3">항목</th><th className="p-3">{count?"작성값":"현재값"}</th><th className="p-3">작업</th></tr></thead><tbody>{settingTargets.map(target=><tr key={target.key} className="border-t">
        <th scope="row" className="p-3 font-normal">{target.label}</th>
        <td className="p-3">{target.key==="autonomousWorkloads"?(shown.autonomousWorkloads?.map(uid=>serviceNames[uid]??uid).join(", ")||"없음"):target.key==="excludedNodes"?excludedNodes(shown):services.length||Object.keys(shown.minimumDemand??{}).length?<table aria-label="서비스별 최소 수요" className="text-left text-sm"><thead><tr><th className="pr-4 font-normal">서비스</th><th className="text-right font-normal">req/s</th></tr></thead><tbody>{[...new Set([...services.map(service=>service.uid),...Object.keys(shown.minimumDemand??{})])].map(uid=><tr key={uid}><th scope="row" className="pr-4 font-normal">{serviceNames[uid]??uid}</th><td className="text-right tabular-nums">{shown.minimumDemand?.[uid]??"미설정"}</td></tr>)}</tbody></table>:"미설정"}</td>
        <td className="p-3">{target.key==="excludedNodes"?<EditRow editKey={target.key} label={target.label} help={target.help} changed={!!count&&String(baseline!.settings.excludedNodes??[])!==String(shown.excludedNodes??[])} initial={shown.excludedNodes??[]} nodeNames={knownNodeNames} save={value=>edit(base=>({...base,excludedNodes:value as string[]}))} disabled={blocked}/>:<ServiceSettingsEdit editKey={target.key} label={target.label} help={target.help} initial={shown} version={count?baseline!.etag:snapshot!.etag} services={services} loadError={workloads.error?"서비스 목록 조회 실패":!workloads.data?"서비스 목록 조회 중":undefined} changed={!!count&&stringifySchedulerJSON(baseline!.settings[target.key])!==stringifySchedulerJSON(shown[target.key])} disabled={blocked} save={(value,source,version)=>edit(base=>({...base,[target.key]:value}),source,version)}/>}</td>
      </tr>)}</tbody></table>:category==="controls"?<DataTable data={controls} columns={[
        {key:"name",label:"항목",value:control=>control.label},
        {key:"value",label:"현재값",value:control=>onOff(shown.controls[control.key])},
        {key:"edit",label:"작업",value:control=><EditRow editKey={`controls.${control.key}`} label={control.label} help={`운영 판단에서 ${control.label}도 함께 조정해요.`} changed={!!count&&baseline!.settings.controls[control.key]!==shown.controls[control.key]} initial={shown.controls[control.key]} save={value=>edit(base=>({...base,controls:{...base.controls,[control.key]:value as boolean}}))} disabled={blocked}/>},
      ]}/>:<DataTable data={fields.filter(field=>field.group===category)} columns={[
        {key:"name",label:"항목",value:field=>field.label},
        {key:"value",label:count?"작성값":"현재값",value:field=>plain(field,shown[field.key])},
        {key:"edit",label:"작업",value:editField},
      ]}/>}</section>
      <details><summary className="min-h-11 cursor-pointer py-2">근거·분배 작업</summary><Rows>
        <Row href="/?evidence=capacity" label="용량 계산 근거"/>
        <Row href="/records?scope=scheduler&filter=settings" label="최근 변경 기록"/>
        {state.error?<Row href="/records?filter=action" label="설정한 요청 분배" value={<span role="status" className="break-words">조회 실패 · HTTP {state.error.status} · {routingFailureCode} · {routingFailureReason}</span>}/>:routing
          ?<Row href={`/records?${new URLSearchParams({filter:"action",id:routing.operation.id,view:"current",stage:"result",node:routing.operation.node.uid,startedAt:routing.startedAt})}`} label="설정한 요청 분배"/>
          :<Row href="/records?filter=action" label="설정한 요청 분배" value={state.data?"기록 없음":state.error?"조회 실패":"조회 중"}/>}
        {snapshot?.declarationsKnown?<RoutingEdit initial={currentSettings?.staticRouting} snapshot={state.data?.snapshot} disabled={blocked||count>0} apply={(before,after,reason)=>void applyRouting(before,after,reason)}/>:<li className="flex min-h-11 items-center justify-between gap-3"><Button variant="outline" disabled>요청 분배 바꾸기</Button><span role="status">조회 필요</span></li>}
        <Row href="/" label="운영 현황"/>
        {currentSettings&&<RowDialog label="원본 자료 보기" title="현재 운영 설정 원본"><pre className="source-json">{stringifySchedulerJSON(safeSource(currentSettings),2)}</pre><SourceExport value={currentSettings} filename="chill-scheduler-settings.json"/></RowDialog>}
      </Rows></details>
    </>}</QueryGate>
    <Dialog open={reviewOpen} onOpenChange={open=>{if(!open){setReviewOpen(false);setReview(null)}}}><DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-xl" onCloseAutoFocus={event=>{event.preventDefault();reviewLauncher.current?.focus()}}>
      <DialogHeader><DialogTitle>{latest?"다른 사람이 먼저 설정을 바꿨어요":`변경 ${count}건을 적용할까요?`}</DialogTitle><DialogDescription>{latest?"설정 변경 충돌 · 작성값 유지":"운영 설정 전체 저장"}</DialogDescription></DialogHeader>
      {!versioned&&<FieldHelp help="서버는 버전 조건부 저장을 지원하지 않아 동시 변경을 완전히 막을 수 없어요."/>}
      {reviewing&&<p role="status">현재 설정을 다시 확인하고 있어요.</p>}
      {checkError&&<p role="alert">{checkError}</p>}
      {latest&&draft&&<><SettingChanges before={latest.settings} after={draft} names={serviceNames}/><Button onClick={()=>{setBaseline(latest);setLatest(null);setReviewOpen(false)}}>최신 값을 기준으로 다시 검토하기</Button></>}
      {review&&baseline&&<>
        <SettingChanges before={baseline.settings} after={review} names={serviceNames}/>
        <h3>마지막으로 확인한 노드</h3>
        {state.isSuccess?(state.data?.snapshot.nodes?.length?<p>{state.data.snapshot.nodes.map(node=>node.node.name).join(", ")}</p>:<p>확인된 노드가 없어요.</p>):<p>노드 목록을 불러오지 못했어요.</p>}
        <p className="text-sm text-muted-foreground">마지막 구성 관측의 노드 · 영향 예측 아님</p>
        <SourceJSON inline title="변경 원본" value={{before:baseline.settings,after:review}}/>
        <Button onClick={()=>void save()} disabled={blocked}>설정 적용하기</Button>
      </>}
    </DialogContent></Dialog>
  </Screen>
}
