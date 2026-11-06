"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient, type QueryClient } from "@tanstack/react-query"
import { useChillQuery, useChillMutation, useSession, ApiError, type ChillMutationInput, type RelayResponse } from "@/lib/client"
import type { OperationResult, PoolsView, Routing, SchedulerSettings, SchedulerSettingsChange, SchedulerControllerState, SchedulerObservationPage, WireInteger } from "@/lib/read-types"
import { millisecondsToNanos, nanosToMilliseconds, wireInteger, marginMilliseconds, preciseJSONSupported, parseSchedulerJSON, stringifySchedulerJSON, settingsEqual } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"
import { QueryGate, SourceJSON, SourceExport } from "@/components/read-region"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DataTable } from "@/components/data-table"
import { SettingsNavigation } from "@/components/settings-navigation"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet"

type NumericKey = Exclude<keyof SchedulerSettings,"controls"|"autonomousWorkloads"|"excludedNodes"|"minimumDemand"|"staticRouting"|"staticRoutingValidatedAt"|"meters"|"requestTimeout">
type Field = {key:NumericKey;label:string;help:string;group:"cycle"|"limit"|"capacity"|"advanced";unit?:string;kind?:"strategy"|"boolean";defaultValue?:string|boolean|number}
const fields: Field[] = [
  {key:"observationWindowNanos",label:"부하를 살펴볼 구간",help:"판단할 때 최근 요청 부하를 이만큼 살펴봐요.",group:"cycle"},
  {key:"controlIntervalNanos",label:"운영 판단 주기",help:"이 간격마다 새로 판단해요.",group:"cycle"},
  {key:"comparisonPeriodNanos",label:"에너지 비교 기간",help:"유지와 모든 후보를 같은 고정 기간으로 비교해요. 이보다 긴 전환은 제외해요.",group:"cycle"},
  {key:"replacementDwellNanos",label:"교체 후 최소 체류 기간",help:"같은 노드·워크로드의 에너지 절약만을 위한 재교체를 보류해요. 0은 에너지 비교 기간을 사용해요. 용량 부족·고장 복구는 기다리지 않아요.",group:"cycle"},
  {key:"scaleDownStabilizationNanos",label:"하강 안정화 기간",help:"이 기간의 최대 유효 수요가 현재 준비된 공급보다 작아야 축소와 에너지 교체를 허용해요. 후보도 최대 수요를 감당해야 해요. 완전한 관측 창이 필요하며, 0도 현재 수요·공급 검사는 유지해요.",group:"cycle"},
  {key:"transitionReserveFraction",label:"전환 예비 비율",help:"전환 중 실제 준비된 용량으로 수요와 예비를 감당해요. 0은 별도 예비 없음, 범위는 0~1이에요.",group:"cycle"},
  {key:"scaleDownHeadroomFraction",label:"하강 용량 여유",help:"최종 하강 경로가 관측 창 최대 수요의 (1 + 비율)을 감당해야 해요. 0은 추가 여유 없음, 범위는 0~1이에요. 회복이나 중간 전환 예비를 부풀리지 않아요.",group:"cycle"},
  {key:"maxObservationAgeNanos",label:"관측을 쓸 수 있는 시간",help:"이보다 오래된 관측은 판단에 쓰지 않아요.",group:"cycle"},
  {key:"completionMarginMS",label:"완료 여유",help:"실행이 끝난 뒤 응답까지 요청마다 이만큼 더해 예상 완료 시각을 계산해요.",group:"capacity",unit:"ms"},
  {key:"servingMemoryBytes",label:"서비스 실행 메모리",help:"측정한 모델 메모리에 인스턴스마다 더하는 메모리예요. 절반을 대기 요청에 써요. 노드 자원은 배포 설정에서 관리해요.",group:"limit"},
  {key:"maxReplicas",label:"복제 수 상한",help:"서비스 복제 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"maxInstances",label:"인스턴스 수 상한",help:"인스턴스 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"maxCPUs",label:"CPU 수 상한",help:"CPU 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"capacityWindowNanos",label:"용량 계산 창 길이",help:"용량을 계산할 때 창마다 적어도 이 시간만큼의 요청을 재생해요.",group:"capacity"},
  {key:"capacityWindows",label:"용량 계산 창 수",help:"요청률마다 이만큼의 독립 창을 재생해요.",group:"capacity",unit:"개"},
  {key:"capacityPassingWindows",label:"용량 충족 창 수",help:"이만큼의 창이 SLO를 지키면 그 요청률을 감당한다고 봐요. 창 수 이하로 정해요.",group:"capacity",unit:"개"},
  {key:"capacityMinimumRequests",label:"용량 계산 최소 요청 수",help:"창마다 적어도 이만큼의 요청을 재생해요.",group:"capacity",unit:"건"},
  {key:"capacityGridSteps",label:"용량 계산 격자 수",help:"최대 처리율을 이만큼 나누어 모든 요청률을 판정해요. 1 이상이에요.",group:"capacity",unit:"개"},
  {key:"capacityBisectionWidth",label:"용량 이분 탐색 폭",help:"최대 처리율에 대한 탐색 종료 폭이에요. 0 초과 1 이하예요.",group:"capacity"},
  {key:"capacityTailSamples",label:"용량 꼬리 표본 수",help:"SLO 백분위 위에 확보할 창별 표본 수예요. 1 이상이에요.",group:"capacity",unit:"개"},
  {key:"planSearchWorkers",label:"구성 탐색 동시 계산 수",help:"운영 판단이 구성 후보를 동시에 이만큼 계산해요. 탐색은 판단 주기의 절반 안에 끝나요.",group:"advanced",unit:"개"},
  {key:"planSearchStrategy",label:"구성 탐색 전략",help:"v1은 기존 탐색, v2는 한계비용 탐욕 탐색이에요. shadow는 v1으로 결정하며 v2를 비교 기록해요.",group:"advanced",kind:"strategy",defaultValue:"v1"},
  {key:"planSearchEvaluations",label:"구성 탐색 평가 예산",help:"v2의 증분 평가를 이 횟수까지 계산해요.",group:"advanced",unit:"회",defaultValue:500000},
  {key:"planExactFinalists",label:"정확 검증 후보 수",help:"증분 평가의 상위 후보를 이만큼 정확히 검증해요.",group:"advanced",unit:"개",defaultValue:16},
  {key:"planPolishActive",label:"결합 연마 활성 계획 수",help:"결합 연마에서 살펴볼 활성 계획 수예요. 0은 전체 계획이에요.",group:"advanced",unit:"개",defaultValue:8},
  {key:"planPostFilterFinalists",label:"후처리 뒤 후보 비교",help:"후보마다 교체 회수와 최소 이득을 적용한 뒤 비교해요.",group:"advanced",kind:"boolean",defaultValue:false},
  {key:"planPricedStart",label:"가격 유도 출발점",help:"용량 가격으로 만든 출발점도 v2 탐색에 사용해요.",group:"advanced",kind:"boolean",defaultValue:true},
]
const controls: {key:keyof SchedulerSettings["controls"];label:string}[] = [
  {key:"placement",label:"배치 위치"},{key:"resources",label:"자원 할당"},
  {key:"replicas",label:"복제 수"},{key:"routing",label:"요청 분배"},
  {key:"batching",label:"배칭"},{key:"power",label:"전력모드"},{key:"membership",label:"실행 그룹 소속"},
]
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
function plain(field:Field,value:WireInteger|string|boolean|undefined):string {
  if(value==null)return "기록 없음"
  if(typeof value==="boolean")return onOff(value)
  if(field.key==="servingMemoryBytes")return mib(value)
  if(!field.key.endsWith("Nanos"))return `${value}${field.unit??""}`
  const nanos=BigInt(value)
  return nanos>0n&&nanos%60000000000n===0n?`${nanos/60000000000n}분`:nanos>0n&&nanos%1000000000n===0n?`${nanos/1000000000n}초`:`${nanosToMilliseconds(nanos)}ms`
}
function changedRows(before:SchedulerSettings,after:SchedulerSettings) {
  return [
    ...(String(before.autonomousWorkloads)!==String(after.autonomousWorkloads)?[["자동 제어 서비스",`${autonomous(before)} → ${autonomous(after)}`] as [string,string]]:[]),
    ...(String(before.excludedNodes??[])!==String(after.excludedNodes??[])?[["계획에서 뺀 장비",`${excludedNodes(before)} → ${excludedNodes(after)}`] as [string,string]]:[]),
    ...fields.filter(field=>String(before[field.key])!==String(after[field.key])).map(field=>[field.label,`${plain(field,before[field.key])} → ${plain(field,after[field.key])}`] as [string,string]),
    ...controls.filter(control=>before.controls[control.key]!==after.controls[control.key]).map(control=>[control.label,`${onOff(before.controls[control.key])} → ${onOff(after.controls[control.key])}`] as [string,string]),
  ]
}
export function SettingChanges({before,after,stored=false}:{before:SchedulerSettings;after:SchedulerSettings;stored?:boolean}) {
  const rows=changedRows(before,after)
  return rows.length?<Facts items={rows}/>:<p>{stored?"기록의 전후 설정이 같아요.":"바꾼 값과 현재 설정이 같아요."}</p>
}
export function SettingsSummary({settings}:{settings:SchedulerSettings}) {
  return <Facts items={[["자동 제어 서비스",autonomous(settings)],["계획에서 뺀 장비",excludedNodes(settings)],...fields.map(field=>[field.label,plain(field,settings[field.key])] as [string,string]),...controls.map(control=>[control.label,onOff(settings.controls[control.key])] as [string,string])]}/>
}

// The one settings write: routing, meters and runtime timeout are read-only. Shared with the Workload screen.
export async function putSchedulerSettings(mutate:(input:ChillMutationInput)=>Promise<RelayResponse>,client:QueryClient,settings:SchedulerSettings) {
  const body:SchedulerSettings&{staticRoutingValidatedAt?:string}={...settings};delete body.staticRouting;delete body.staticRoutingValidatedAt;delete body.meters;delete body.requestTimeout
  const response=await mutate({path:"v1/scheduler/settings",method:"PUT",body:stringifySchedulerJSON(body)})
  const applied=parseSchedulerJSON(response.body) as SchedulerSettingsChange
  await client.invalidateQueries({predicate:query=>typeof query.queryKey[1]==="string"&&(query.queryKey[1].startsWith("v1/scheduler")||query.queryKey[1].startsWith("v1/observations/scheduler"))})
  return applied
}

// One row, one value: the dialog edits only this value and adds it to the draft.
function EditRow({editKey,label,help,value,changed,initial,unit,range,save,disabled,nodeNames=[],options}:{editKey:string;label:string;help:string;value:string;changed:boolean;initial:string|boolean|string[];unit?:string;range?:string;save:(value:string|boolean|string[])=>void;disabled:boolean;nodeNames?:string[];options?:string[]}) {
  const params=useSearchParams(),router=useRouter(),open=params.get("edit")===editKey
  const [next,setNext]=React.useState(initial),[error,setError]=React.useState(""),id=React.useId()
  function setOpen(state:boolean) {
    const query=new URLSearchParams(params.toString())
    if(state)query.set("edit",editKey);else query.delete("edit")
    router[state?"push":"replace"](`/settings?${query}`,{scroll:false})
    setNext(initial);setError("")
  }
  return <Sheet open={open} onOpenChange={setOpen}><SheetTrigger asChild><Button variant="outline" disabled={disabled} aria-label={`${label} 수정`}>수정{changed?" · 변경":""}</Button></SheetTrigger><SheetContent className="w-full overflow-y-auto sm:max-w-xl" aria-describedby={undefined}><SheetHeader><SheetTitle>{label}</SheetTitle></SheetHeader><div className="grid gap-4 px-4 pb-6"><Facts items={[["현재값",value]]}/><details><summary className="min-h-11 cursor-pointer py-2">항목 도움말</summary>{help}</details>
    <form className="grid gap-4" onSubmit={event=>{event.preventDefault();try {save(Array.isArray(next)?nodeList(next):next);setOpen(false)} catch(caught) {setError(caught instanceof Error?caught.message:"값을 확인해 주세요.")}}}>
      {Array.isArray(next)?<div className="grid gap-3"><fieldset className="flex flex-wrap gap-2"><legend className="mb-2">관측된 장비 선택</legend>{nodeNames.map(name=><label key={name} className="flex min-h-11 items-center gap-2 rounded-md border px-3"><input type="checkbox" value={name} checked={next.includes(name)} onChange={event=>setNext(event.target.checked?[...next,name]:next.filter(item=>item!==name))}/>{name}</label>)}{!nodeNames.length&&<p>관측된 장비 목록 없음 · 현재 설정은 유지돼요.</p>}</fieldset>{next.map((name,index)=>!nodeNames.includes(name)&&<div key={index} className="flex min-w-0 gap-2"><Input aria-label={`현재 목록에 없는 장비 ${index+1}`} value={name} onChange={event=>setNext(next.map((item,i)=>i===index?event.target.value:item))}/><Button type="button" variant="outline" onClick={()=>setNext(next.filter((_,i)=>i!==index))}>제외 해제</Button></div>)}</div>:typeof next==="boolean"
        ?<label className="switch"><input type="checkbox" role="switch" checked={next} onChange={event=>setNext(event.target.checked)}/>{onOff(next)}</label>
        :options?<label className="grid gap-2" htmlFor={id}>값<select id={id} className="min-h-11 rounded-md border bg-background px-3" value={next} onChange={event=>setNext(event.target.value)}>{options.map(option=><option key={option} value={option}>{option}</option>)}</select></label>
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
  const section=params.get("section"),category:Field["group"]|"controls"=section==="limit"||section==="controls"||section==="capacity"||section==="advanced"?section:"cycle"
  const settings=useChillQuery<SchedulerSettings>("v1/scheduler/settings",{decoder:parseSchedulerJSON,poll:true})
  const state=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON})
  const pools=useChillQuery<PoolsView>("v1/pools",{enabled:params.get("edit")==="excludedNodes"})
  const knownNodeNames=[...new Set([
    ...(state.data?.snapshot?.nodes??[]).map(item=>item.node.name),
    ...(pools.data?.available??[]).map(item=>item.node.name),
    ...(pools.data?.other??[]).map(item=>item.node.name),
    ...(pools.data?.measurement??[]).map(item=>item.node.name),
    ...(pools.data?.workloads??[]).flatMap(pool=>(pool.nodes??[]).map(node=>node.name)),
  ])]
  const [baseline,setBaseline]=React.useState<SchedulerSettings|null>(null),[draft,setDraft]=React.useState<SchedulerSettings|null>(null)
  const [reviewOpen,setReviewOpen]=React.useState(false),[checkError,setCheckError]=React.useState("")
  const [review,setReview]=React.useState<SchedulerSettings|null>(null),[latest,setLatest]=React.useState<SchedulerSettings|null>(null)
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
  const count=baseline&&draft?changedRows(baseline,draft).length:0
  const shown=count?draft:settings.data
  // Explicit routing operations open in the same detail view as the transitions list.
  const routing=state.data?.results?.filter(item=>["route","unroute","release"].includes(item.operation.kind)).reduce<OperationResult|undefined>((latest,item)=>!latest||Date.parse(item.startedAt)>Date.parse(latest.startedAt)?item:latest,undefined)
  const back:[string,string]=["/settings","설정"]

  // Edits accumulate on the draft; the first edit pins the baseline used for the concurrency check.
  function edit(change:(base:SchedulerSettings)=>SchedulerSettings) {
    if(!count)setBaseline(settings.data!)
    setDraft(change(count?draft!:settings.data!))
  }
  async function inspect() {
    if(blocked||!draft||!baseline)return
    setReviewOpen(true);setReviewing(true);setReview(null);setLatest(null);setCheckError("");setResult(null)
    try {
      const [current]=await Promise.all([settings.refetch(),state.refetch()])
      if(current.error||!current.data){setCheckError("현재 설정을 불러오지 못했어요. 바꾼 값은 그대로 있어요.");return}
      if(!settingsEqual(baseline,current.data)){setLatest(current.data);return}
      setReview(draft)
    } finally {setReviewing(false)}
  }
  async function submit(selected:SchedulerSettings) {
    saving.current=true;setSubmittedAt(new Date().toISOString());setRecoveryStarted(0);setHistoryWindow(null);setResult(null);setResponseError(null)
    try {
      const applied=await putSchedulerSettings(mutation.mutateAsync,client,selected)
      setResult(applied)
      setBaseline(null);setDraft(null)
    } catch(error) {if(!(error instanceof ApiError))setResponseError("적용 응답을 해석하지 못했어요. 현재 설정과 최근 변경 기록을 확인해 주세요.")}
    finally {saving.current=false}
  }
  async function save() {
    if(!review||blocked||saving.current)return
    const selected=review;setReview(null);setReviewOpen(false)
    await submit(selected)
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
    const message=mutation.error?.status===403?"조회 권한과 별도로 설정 변경 권한과 설정 저장소(ConfigMap) 수정 권한이 모두 필요해요.":responseError??serverError?.message??mutation.error?.body
    const field=serverError?.field&&(fields.find(item=>item.key===serverError.field)?.label??({settings:"운영 설정 전체",staticRouting:"요청 분배",reason:"분배 선택 이유"} as Record<string,string>)[serverError.field]??serverError.field)
    if(!unknown)return <Screen back={back} title={mutation.error?.status===403?"변경 권한 없음":"설정 적용 실패"} action={<Button onClick={()=>mutation.reset()}>다시 수정하기</Button>}>
      <Facts items={[["원인",message??"미제공"],["작성값","유지"],...(field?[["관련 항목",field] as [string,string]]:[])]}/>
    </Screen>
    const events=(history.data?.events??[]).filter(event=>event.settings)
    return <Screen back={back} title="적용 결과 미확인"
      action={recovered&&settings.data
        ?<Button onClick={()=>{setBaseline(settings.data!);setLatest(null);setResponseError(null);mutation.reset();setHistoryWindow(null)}}>조회한 현재 값으로 다시 검토하기</Button>
        :<Button onClick={()=>void recover()} disabled={settings.isFetching||history.isFetching}>현재 설정과 최근 변경 기록 확인하기</Button>}>
      <Facts items={[["원인",message??"응답 미수신"],["작성값","유지"],["자동 재제출","차단"]]}/>
      {historyWindow&&<QueryGate query={history}>{events.length?<Facts items={events.map(event=>[time(event.at),`${event.settings!.actorUsername} · ${event.settings!.appliedAt?"적용됨":event.settings!.error??"적용 전 확인만 됐어요"}`] as [string,string])}/>:<Empty title="이 구간에 운영 설정 변경 기록이 없어요"/>}</QueryGate>}
      {historyWindow&&<Rows>
        <Row href={`/records?scope=scheduler&filter=settings&${new URLSearchParams(historyWindow)}`} label="구간 변경 기록" hint={history.data?.nextCursor?"추가 기록 있음":undefined}/>
        {recovered&&settings.data&&<RowDialog label="현재 값과 바꾼 값 비교" title="현재 값과 바꾼 값" description="방금 조회한 현재 설정과 바꾼 값을 비교해요."><SettingChanges before={settings.data} after={draft??settings.data}/></RowDialog>}
      </Rows>}
    </Screen>
  }

  const editField=(field:Field)=>shown&&<EditRow key={field.key} editKey={field.key} label={field.label} help={field.help} value={plain(field,shown[field.key])} changed={!!count&&String(baseline![field.key])!==String(shown[field.key])}
    initial={field.kind==="boolean"?Boolean(shown[field.key]??field.defaultValue):field.key.endsWith("Nanos")?nanosToMilliseconds(shown[field.key] as WireInteger):String(shown[field.key]??field.defaultValue??"")} options={field.kind==="strategy"?["v1","v2","shadow"]:undefined} unit={field.key.endsWith("Nanos")?"ms":field.key==="servingMemoryBytes"?"bytes":field.unit}
    range={field.key==="planPolishActive"?"0 이상의 정수, 0은 전체":field.key==="scaleDownStabilizationNanos"||field.key==="replacementDwellNanos"?"0 이상의 밀리초, 소수점 아래 6자리까지":field.key==="transitionReserveFraction"||field.key==="scaleDownHeadroomFraction"?"0 ~ 1 사이 유한한 수":field.key.endsWith("Nanos")?"0보다 큰 밀리초, 소수점 아래 6자리까지":field.key==="completionMarginMS"?"0 이상의 밀리초":field.key==="servingMemoryBytes"?"0 ~ 18446744073709551615 사이 바이트 수":"1 이상의 정수"}
    save={value=>{
      // Same validation as the wire: ms with ≤6 decimals → nanos, nonnegative margin ms, uint64 memory bytes, positive int64 otherwise.
      if(field.kind){edit(base=>({...base,[field.key]:value}));return}
      const text=value as string,parsed=field.key.endsWith("Nanos")?millisecondsToNanos(text,field.key==="scaleDownStabilizationNanos"||field.key==="replacementDwellNanos"?0n:1n):field.key==="transitionReserveFraction"||field.key==="scaleDownHeadroomFraction"||field.key==="capacityBisectionWidth"?reserveFraction(text):field.key==="completionMarginMS"?marginMilliseconds(text):field.key==="servingMemoryBytes"?wireInteger(text,0n,18446744073709551615n):wireInteger(text,field.key==="planPolishActive"?0n:1n,9223372036854775807n)
      if(field.key==="capacityBisectionWidth"&&(Number(parsed)<=0||Number(parsed)>1))throw new Error("0 초과 1 이하의 유한한 값을 적어 주세요.")
      edit(base=>({...base,[field.key]:parsed}))
    }} disabled={blocked}/>
  const routingFailure=state.error?.data
  const routingFailureCode=routingFailure&&typeof routingFailure==="object"&&"code" in routingFailure&&typeof routingFailure.code==="string"?routingFailure.code:"오류 코드 미제공"
  const routingFailureReason=routingFailure&&typeof routingFailure==="object"&&"message" in routingFailure&&typeof routingFailure.message==="string"?routingFailure.message:"상세 사유 미제공"
  return <Screen title="설정" width="data" actionPlacement="header"
    action={count>0&&<Button ref={reviewLauncher} onClick={()=>void inspect()} disabled={blocked}>{reviewing?"현재값 조회 중":`변경 ${count}건 검토`}</Button>}>
    <SettingsNavigation active="configuration"/>
    {!precise&&<strong role="alert">큰 정수 처리 미지원 · 설정 변경 차단</strong>}
    <label className="grid max-w-sm gap-2 text-sm">운영 설정 범위<select className="min-h-11 rounded-md border bg-background px-3" value={category} onChange={event=>{const query=new URLSearchParams(params.toString());query.set("section",event.target.value);query.delete("edit");router.push(`/settings?${query}`,{scroll:false})}}>{([["cycle","판단 주기와 범위"],["limit","자원 한도"],["controls","자동 조정 항목"],["capacity","용량 설정"],["advanced","고급"]] as const).map(([value,label])=><option key={value} value={value}>{label}</option>)}</select></label>
    <QueryGate query={settings}>{shown&&<>
      <section aria-label="현재값·정의 표">{category==="controls"?<DataTable data={controls} columns={[
        {key:"name",label:"항목",value:control=>control.label},
        {key:"value",label:"현재값",value:control=>onOff(shown.controls[control.key])},
        {key:"edit",label:"작업",value:control=><EditRow editKey={`controls.${control.key}`} label={control.label} help={`운영 판단에서 ${control.label}도 함께 조정해요.`} value={onOff(shown.controls[control.key])} changed={!!count&&baseline!.controls[control.key]!==shown.controls[control.key]} initial={shown.controls[control.key]} save={value=>edit(base=>({...base,controls:{...base.controls,[control.key]:value as boolean}}))} disabled={blocked}/>},
      ]}/>:<DataTable data={fields.filter(field=>field.group===category)} columns={[
        {key:"name",label:"항목",value:field=>field.label},
        {key:"value",label:count?"작성값":"현재값",value:field=>plain(field,shown[field.key])},
        {key:"edit",label:"작업",value:editField},
      ]}/>}</section>
      <details><summary className="min-h-11 cursor-pointer py-2">연결 대상·작업</summary><Facts items={[["자동 제어 서비스",autonomous(shown)],["계획 제외 장비",excludedNodes(shown)]]}/><Rows>
        <li className="flex min-h-11 items-center justify-between gap-3"><span>계획 제외 장비</span><EditRow editKey="excludedNodes" label="계획 제외 장비" help="운영 판단의 배치·제거·전력모드 변경 제외 대상" value={excludedNodes(shown)} changed={!!count&&String(baseline!.excludedNodes??[])!==String(shown.excludedNodes??[])} initial={shown.excludedNodes??[]} nodeNames={knownNodeNames} save={value=>edit(base=>({...base,excludedNodes:value as string[]}))} disabled={blocked}/></li>
        <Row href="/?evidence=capacity" label="용량 계산 근거"/>
        <Row href="/records?scope=scheduler&filter=settings" label="최근 변경 기록"/>
        {state.error?<Row href="/records?filter=action" label="설정한 요청 분배" value={<span role="status" className="break-words">조회 실패 · HTTP {state.error.status} · {routingFailureCode} · {routingFailureReason}</span>}/>:routing
          ?<Row href={`/records?${new URLSearchParams({filter:"action",id:routing.operation.id,view:"current",stage:"result",node:routing.operation.node.uid,startedAt:routing.startedAt})}`} label="설정한 요청 분배"/>
          :<Row href="/records?filter=action" label="설정한 요청 분배" value={state.data?"기록 없음":state.error?"조회 실패":"조회 중"}/>}
        <RoutingEdit initial={settings.data?.staticRouting} snapshot={state.data?.snapshot} disabled={blocked||count>0} apply={(before,after,reason)=>void applyRouting(before,after,reason)}/>
        <Row href="/" label="운영 현황"/>
        {settings.data&&<RowDialog label="원본 자료 보기" title="현재 운영 설정 원본"><pre className="source-json">{stringifySchedulerJSON(safeSource(settings.data),2)}</pre><SourceExport value={settings.data} filename="chill-scheduler-settings.json"/></RowDialog>}
      </Rows></details>
    </>}</QueryGate>
    <Dialog open={reviewOpen} onOpenChange={open=>{if(!open){setReviewOpen(false);setReview(null)}}}><DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-xl" onCloseAutoFocus={event=>{event.preventDefault();reviewLauncher.current?.focus()}}>
      <DialogHeader><DialogTitle>{latest?"다른 사람이 먼저 설정을 바꿨어요":`변경 ${count}건을 적용할까요?`}</DialogTitle><DialogDescription>{latest?"최신 값과 바꾼 값을 비교해요. 바꾼 값은 그대로 있어요.":"적용하면 운영 설정 전체를 한 번에 저장해요. 여러 사람이 바꾸면 마지막에 적용한 값이 남아요."}</DialogDescription></DialogHeader>
      {reviewing&&<p role="status">현재 설정을 다시 확인하고 있어요.</p>}
      {checkError&&<p role="alert">{checkError}</p>}
      {latest&&draft&&<><SettingChanges before={latest} after={draft}/><Button onClick={()=>{setBaseline(latest);setLatest(null);setReviewOpen(false)}}>최신 값을 기준으로 다시 검토하기</Button></>}
      {review&&baseline&&<>
        <SettingChanges before={baseline} after={review}/>
        <h3>마지막으로 확인한 노드</h3>
        {state.isSuccess?(state.data?.snapshot.nodes?.length?<p>{state.data.snapshot.nodes.map(node=>node.node.name).join(", ")}</p>:<p>확인된 노드가 없어요.</p>):<p>노드 목록을 불러오지 못했어요.</p>}
        <p className="text-sm text-muted-foreground">마지막 구성 관측에 있던 노드예요. 영향 예측은 아니에요. 적용 직전에 다시 확인해도 동시 변경을 완전히 막지는 못해요.</p>
        <SourceJSON inline title="변경 원본" value={{before:baseline,after:review}}/>
        <Button onClick={()=>void save()} disabled={blocked}>설정 적용하기</Button>
      </>}
    </DialogContent></Dialog>
  </Screen>
}
