"use client"

import * as React from "react"
import { useQueryClient, type QueryClient } from "@tanstack/react-query"
import { useChillQuery, useChillMutation, useSession, ApiError, type ChillMutationInput, type RelayResponse } from "@/lib/client"
import type { OperationResult, Routing, SchedulerSettings, SchedulerSettingsChange, SchedulerControllerState, SchedulerObservationPage, WireInteger } from "@/lib/read-types"
import { millisecondsToNanos, nanosToMilliseconds, wireInteger, marginMilliseconds, preciseJSONSupported, parseSchedulerJSON, stringifySchedulerJSON, settingsEqual } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"
import { QueryGate, SourceJSON, SourceExport } from "@/components/read-region"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

type NumericKey = Exclude<keyof SchedulerSettings,"controls"|"autonomousWorkloads"|"excludedNodes"|"minimumDemand"|"staticRouting">
type Field = {key:NumericKey;label:string;help:string;group:"cycle"|"limit"|"advanced";unit?:string}
const fields: Field[] = [
  {key:"observationWindowNanos",label:"부하를 살펴볼 구간",help:"판단할 때 최근 요청 부하를 이만큼 살펴봐요.",group:"cycle"},
  {key:"controlIntervalNanos",label:"운영 판단 주기",help:"이 간격마다 새로 판단해요.",group:"cycle"},
  {key:"comparisonPeriodNanos",label:"에너지 비교 기간",help:"후보마다 앞으로 이 기간의 에너지를 비교해요. 가장 긴 전환이 더 길면 그 전환 시간으로 비교해요.",group:"cycle"},
  {key:"maxObservationAgeNanos",label:"관측을 쓸 수 있는 시간",help:"이보다 오래된 관측은 판단에 쓰지 않아요.",group:"cycle"},
  {key:"completionMarginMS",label:"완료 여유",help:"실행이 끝난 뒤 응답까지 요청마다 이만큼 더해 예상 완료 시각을 계산해요.",group:"cycle",unit:"ms"},
  {key:"servingMemoryBytes",label:"서비스 실행 메모리",help:"측정한 모델 메모리에 인스턴스마다 더하는 메모리예요. 절반을 대기 요청에 써요. 노드 자원은 배포 설정에서 관리해요.",group:"limit"},
  {key:"maxReplicas",label:"복제 수 상한",help:"서비스 복제 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"maxInstances",label:"인스턴스 수 상한",help:"인스턴스 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"maxCPUs",label:"CPU 수 상한",help:"CPU 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"capacityWindowNanos",label:"용량 계산 창 길이",help:"용량을 계산할 때 창마다 적어도 이 시간만큼의 요청을 재생해요.",group:"advanced"},
  {key:"capacityWindows",label:"용량 계산 창 수",help:"요청률마다 이만큼의 독립 창을 재생해요.",group:"advanced",unit:"개"},
  {key:"capacityPassingWindows",label:"용량 충족 창 수",help:"이만큼의 창이 SLO를 지키면 그 요청률을 감당한다고 봐요. 창 수 이하로 정해요.",group:"advanced",unit:"개"},
  {key:"capacityMinimumRequests",label:"용량 계산 최소 요청 수",help:"창마다 적어도 이만큼의 요청을 재생해요.",group:"advanced",unit:"건"},
  {key:"planSearchWorkers",label:"구성 탐색 동시 계산 수",help:"운영 판단이 구성 후보를 동시에 이만큼 계산해요. 탐색은 판단 주기의 절반 안에 끝나요.",group:"advanced",unit:"개"},
]
const controls: {key:keyof SchedulerSettings["controls"];label:string}[] = [
  {key:"placement",label:"배치 위치"},{key:"resources",label:"자원 할당"},
  {key:"replicas",label:"복제 수"},{key:"routing",label:"요청 분배"},
  {key:"batching",label:"배칭"},{key:"power",label:"전력모드"},{key:"membership",label:"실행 그룹 소속"},
]
const onOff = (on:boolean) => on?"사용":"사용 안 함"
const autonomous = (settings:SchedulerSettings) => `${settings.autonomousWorkloads?.length??0}개`
// Nodes the planner never changes, like measurement Nodes.
export const excludedNodes = (settings:SchedulerSettings) => settings.excludedNodes?.length?settings.excludedNodes.join(", "):"없음"
const nodeList = (text:string) => {
  const names=text.trim()==="없음"?[]:text.split(",").map(name=>name.trim()).filter(Boolean)
  if(new Set(names).size!==names.length)throw new Error("같은 장비 이름을 두 번 적었어요.")
  return names
}
const mib = (bytes:WireInteger|string) => `약 ${(Number(bytes)/1048576).toLocaleString("ko-KR",{maximumFractionDigits:3})}MiB`
const time = (at:string) => new Date(at).toLocaleString("ko-KR")
function plain(field:Field,value:WireInteger):string {
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

// The one settings write: PUT the full settings, then refresh every scheduler read. Shared with the Workload screen.
// A routing change replaces the whole staticRouting declaration and carries its selection reason;
// a settings-only write leaves staticRouting out so the stored declaration stays.
export async function putSchedulerSettings(mutate:(input:ChillMutationInput)=>Promise<RelayResponse>,client:QueryClient,settings:SchedulerSettings,routing?:{staticRouting:Routing;reason:string}) {
  const body={...settings};delete body.staticRouting
  const response=await mutate({path:"v1/scheduler/settings",method:"PUT",body:stringifySchedulerJSON({...body,...routing})})
  const applied=parseSchedulerJSON(response.body) as SchedulerSettingsChange
  await client.invalidateQueries({predicate:query=>typeof query.queryKey[1]==="string"&&(query.queryKey[1].startsWith("v1/scheduler")||query.queryKey[1].startsWith("v1/observations/scheduler"))})
  return applied
}

// One row, one value: the dialog edits only this value and adds it to the draft.
function EditRow({label,help,value,changed,initial,unit,range,save,disabled}:{label:string;help:string;value:string;changed:boolean;initial:string|boolean;unit?:string;range?:string;save:(value:string|boolean)=>void;disabled:boolean}) {
  const [open,setOpen]=React.useState(false),[next,setNext]=React.useState(initial),[error,setError]=React.useState(""),id=React.useId()
  return <RowDialog label={label} hint={changed?"바꾼 값이에요":undefined} value={value} title={label} description={help} open={open} onOpenChange={state=>{setOpen(state);setNext(initial);setError("")}}>
    <form className="grid gap-4" onSubmit={event=>{event.preventDefault();try {save(next);setOpen(false)} catch(caught) {setError(caught instanceof Error?caught.message:"값을 확인해 주세요.")}}}>
      {typeof next==="boolean"
        ?<label className="switch"><input type="checkbox" role="switch" checked={next} onChange={event=>setNext(event.target.checked)}/>{onOff(next)}</label>
        :<div className="grid gap-2"><label htmlFor={id}>{unit?`값 (${unit})`:"값"}</label><Input id={id} autoFocus required inputMode={unit==="ms"?"decimal":"text"} value={next} onChange={event=>{setNext(event.target.value);setError("")}} aria-invalid={!!error} aria-describedby={`${id}-help`}/><span id={`${id}-help`} className="text-sm text-muted-foreground">{range}{unit==="bytes"&&/^\d+$/.test(next)?` · ${mib(next)}`:""}</span></div>}
      {error&&<p role="alert">{error}</p>}
      <Button type="submit" disabled={disabled}>바꾸기</Button>
    </form>
  </RowDialog>
}

// The JSON is the whole declaration: service UID → instance ID → weight. {} removes it.
// The stored declaration from GET settings prefills it; without one the field starts empty.
function RoutingEdit({initial,disabled,apply}:{initial?:Routing;disabled:boolean;apply:(staticRouting:Routing,reason:string)=>void}) {
  const [open,setOpen]=React.useState(false),[text,setText]=React.useState(""),[reason,setReason]=React.useState(""),[error,setError]=React.useState(""),id=React.useId()
  return <RowDialog label="요청 분배 바꾸기" hint="자동 제어하지 않는 서비스의 인스턴스 비율과 선택 이유" title="요청 분배 바꾸기" description="서비스 UID → 인스턴스 ID → 가중치를 JSON으로 적어요. 적은 내용이 설정한 분배 전체가 되고, 적지 않은 서비스는 지금 분배를 유지해요. {}는 설정한 분배를 지워요." open={open} onOpenChange={state=>{setOpen(state);setText(initial?JSON.stringify(initial,null,2):"");setReason("");setError("")}}>
    <form className="grid gap-4" onSubmit={event=>{event.preventDefault();try {
      const parsed:unknown=JSON.parse(text)
      if(!parsed||typeof parsed!=="object"||Array.isArray(parsed))throw new Error("JSON 객체로 적어 주세요.")
      apply(parsed as Routing,reason.trim());setOpen(false)
    } catch(caught) {setError(caught instanceof Error?caught.message:"값을 확인해 주세요.")}}}>
      <div className="grid gap-2"><label htmlFor={`${id}-routing`}>분배</label><textarea id={`${id}-routing`} className="min-h-40 rounded-md border p-2 font-mono text-sm" required value={text} onChange={event=>{setText(event.target.value);setError("")}} aria-invalid={!!error}/></div>
      <div className="grid gap-2"><label htmlFor={`${id}-reason`}>선택 이유</label><Input id={`${id}-reason`} required value={reason} onChange={event=>setReason(event.target.value)}/></div>
      {error&&<p role="alert">{error}</p>}
      <Button type="submit" disabled={disabled||!reason.trim()}>분배 적용하기</Button>
    </form>
  </RowDialog>
}


export function SchedulerSettingsPage() {
  const settings=useChillQuery<SchedulerSettings>("v1/scheduler/settings",{decoder:parseSchedulerJSON,poll:true})
  const state=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON})
  const [baseline,setBaseline]=React.useState<SchedulerSettings|null>(null),[draft,setDraft]=React.useState<SchedulerSettings|null>(null)
  const [reviewOpen,setReviewOpen]=React.useState(false),[checkError,setCheckError]=React.useState("")
  const [review,setReview]=React.useState<SchedulerSettings|null>(null),[latest,setLatest]=React.useState<SchedulerSettings|null>(null)
  const [reviewing,setReviewing]=React.useState(false),[result,setResult]=React.useState<SchedulerSettingsChange|null>(null)
  const [responseError,setResponseError]=React.useState<string|null>(null)
  const [submittedAt,setSubmittedAt]=React.useState("")
  const [historyWindow,setHistoryWindow]=React.useState<{from:string;to:string}|null>(null)
  const [recoveryStarted,setRecoveryStarted]=React.useState(0)
  const reviewLauncher=React.useRef<HTMLButtonElement|null>(null)
  const {config}=useSession(),client=useQueryClient(),mutation=useChillMutation(),saving=React.useRef(false)
  const history=useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${new URLSearchParams(historyWindow??{from:submittedAt,to:submittedAt})}&limit=${config.pageSize}`,{decoder:parseSchedulerJSON,enabled:!!historyWindow})
  const precise=preciseJSONSupported()
  const unknown=!!responseError||(mutation.error?.unknownOutcome??false)
  const recovered=recoveryStarted>0&&settings.dataUpdatedAt>=recoveryStarted&&!settings.isFetching&&history.isSuccess&&!history.isFetching
  const blocked=mutation.isPending||reviewing||(unknown&&!recovered)||!precise||[401,403].includes(settings.error?.status??0)
  const serverError=mutation.error?.data&&typeof mutation.error.data==="object"?mutation.error.data as {field?:string;message?:string;code?:string}:undefined
  const count=baseline&&draft?changedRows(baseline,draft).length:0
  const shown=count?draft:settings.data
  // The newest configuration-sourced routing result opens in its /transitions detail (same identity as the transitions list).
  const routing=state.data?.results?.filter(item=>item.operation.kind==="manual-static-routing").reduce<OperationResult|undefined>((latest,item)=>!latest||Date.parse(item.startedAt)>Date.parse(latest.startedAt)?item:latest,undefined)
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
  async function submit(selected:SchedulerSettings,routing?:{staticRouting:Routing;reason:string}) {
    saving.current=true;setSubmittedAt(new Date().toISOString());setRecoveryStarted(0);setHistoryWindow(null);setResult(null);setResponseError(null)
    try {
      const applied=await putSchedulerSettings(mutation.mutateAsync,client,selected,routing)
      setResult(applied)
      if(!routing){setBaseline(null);setDraft(null)}
    } catch(error) {if(!(error instanceof ApiError))setResponseError("적용 응답을 해석하지 못했어요. 현재 설정과 최근 변경 기록을 확인해 주세요.")}
    finally {saving.current=false}
  }
  async function save() {
    if(!review||blocked||saving.current)return
    const selected=review;setReview(null);setReviewOpen(false)
    await submit(selected)
  }
  // A routing change keeps the settings the server holds right now.
  async function applyRouting(staticRouting:Routing,reason:string) {
    if(blocked||saving.current)return
    const current=await settings.refetch()
    if(current.data)await submit(current.data,{staticRouting,reason})
  }
  async function recover() {
    const now=Date.now();setRecoveryStarted(now)
    setHistoryWindow({from:new Date(Date.parse(submittedAt)-60000).toISOString(),to:new Date(now).toISOString()})
    await settings.refetch()
  }

  if(result)return <Screen back={back} title={result.appliedAt?"설정을 적용했어요":"적용 결과를 확인해 주세요"} lede="실제 구성 변경은 따로 진행돼요. 진행 상황은 아래에서 볼 수 있어요." action={<Button onClick={()=>setResult(null)}>확인</Button>}>
    <Facts items={[["적용 시각",result.appliedAt?time(result.appliedAt):"아직 없어요"],["확인 시각",time(result.validatedAt)],["바꾼 사람",result.actorUsername],...(result.reason?[["분배 선택 이유",result.reason] as [string,string]]:[]),["마지막으로 확인한 노드",result.affected?.map(node=>node.name).join(", ")||"없음"],...(result.error?[["오류",result.error] as [string,string]]:[])]}/>
    <Rows><Row href="/transitions" label="실제 변경 진행 보기"/><RowDialog label="원본 자료 보기" title="적용 결과 원본"><SourceJSON inline value={result}/></RowDialog></Rows>
  </Screen>

  if(mutation.error||responseError) {
    const message=mutation.error?.status===403?"조회 권한과 별도로 설정 변경 권한과 설정 저장소(ConfigMap) 수정 권한이 모두 필요해요.":responseError??serverError?.message??mutation.error?.body
    const field=serverError?.field&&(fields.find(item=>item.key===serverError.field)?.label??({settings:"운영 설정 전체",staticRouting:"요청 분배",reason:"분배 선택 이유"} as Record<string,string>)[serverError.field]??serverError.field)
    if(!unknown)return <Screen back={back} title={mutation.error?.status===403?"설정을 바꿀 권한이 없어요":"설정을 적용하지 못했어요"} lede={`${message??""} 바꾼 값은 그대로 있어요.`} action={<Button onClick={()=>mutation.reset()}>다시 수정하기</Button>}>
      {field&&<Facts items={[["관련 항목",field]]}/>}
    </Screen>
    const events=(history.data?.events??[]).filter(event=>event.settings)
    return <Screen back={back} title="적용됐는지 확인이 필요해요" lede={`${message??"응답을 받지 못했어요."} 바꾼 값은 그대로 있고, 자동으로 다시 보내지 않아요.`}
      action={recovered&&settings.data
        ?<Button onClick={()=>{setBaseline(settings.data!);setLatest(null);setResponseError(null);mutation.reset();setHistoryWindow(null)}}>조회한 현재 값으로 다시 검토하기</Button>
        :<Button onClick={()=>void recover()} disabled={settings.isFetching||history.isFetching}>현재 설정과 최근 변경 기록 확인하기</Button>}>
      {historyWindow&&<QueryGate query={history}>{events.length?<Facts items={events.map(event=>[time(event.at),`${event.settings!.actorUsername} · ${event.settings!.appliedAt?"적용됨":event.settings!.error??"적용 전 확인만 됐어요"}`] as [string,string])}/>:<Empty title="이 구간에 운영 설정 변경 기록이 없어요"/>}</QueryGate>}
      {historyWindow&&<Rows>
        <Row href={`/changes?scope=scheduler&${new URLSearchParams(historyWindow)}`} label="이 구간의 변경 기록 모두 보기" hint={history.data?.nextCursor?"이 화면에 없는 기록이 더 있어요":undefined}/>
        {recovered&&settings.data&&<RowDialog label="현재 값과 바꾼 값 비교" title="현재 값과 바꾼 값" description="방금 조회한 현재 설정과 바꾼 값을 비교해요."><SettingChanges before={settings.data} after={draft??settings.data}/></RowDialog>}
      </Rows>}
    </Screen>
  }

  const group=(name:Field["group"])=>shown&&fields.filter(field=>field.group===name).map(field=><EditRow key={field.key} label={field.label} help={field.help} value={plain(field,shown[field.key])} changed={!!count&&String(baseline![field.key])!==String(shown[field.key])}
    initial={field.key.endsWith("Nanos")?nanosToMilliseconds(shown[field.key]):String(shown[field.key])} unit={field.key.endsWith("Nanos")?"ms":field.key==="servingMemoryBytes"?"bytes":field.unit}
    range={field.key.endsWith("Nanos")?"0보다 큰 밀리초, 소수점 아래 6자리까지":field.key==="completionMarginMS"?"0 이상의 밀리초":field.key==="servingMemoryBytes"?"0 ~ 18446744073709551615 사이 바이트 수":"1 이상의 정수"}
    save={value=>{
      // Same validation as the wire: ms with ≤6 decimals → nanos, nonnegative margin ms, uint64 memory bytes, positive int64 otherwise.
      const text=value as string,parsed=field.key.endsWith("Nanos")?millisecondsToNanos(text):field.key==="completionMarginMS"?marginMilliseconds(text):field.key==="servingMemoryBytes"?wireInteger(text,0n,18446744073709551615n):wireInteger(text,1n,9223372036854775807n)
      edit(base=>({...base,[field.key]:parsed}))
    }} disabled={blocked}/>)
  return <Screen back={back} title="운영 설정"
    lede={!precise?"이 브라우저는 큰 정수를 정확히 다루지 못해 설정을 바꿀 수 없어요. 최신 브라우저에서 열어 주세요.":settings.data?(settings.data.autonomousWorkloads?.length?`서비스 ${settings.data.autonomousWorkloads.length}개를 자동으로 제어하고 있어요. 서비스 화면에서 켜고 끌 수 있어요.`:"모든 서비스가 마지막 구성을 유지해요. 서비스 화면에서 자동 제어를 켤 수 있어요."):undefined}
    action={count>0&&<Button ref={reviewLauncher} onClick={()=>void inspect()} disabled={blocked}>{reviewing?"현재 값을 확인하고 있어요":`변경 ${count}건 검토`}</Button>}>
    <QueryGate query={settings}>{shown&&<>
      <Rows label="판단 주기와 범위">{group("cycle")}</Rows>
      <Rows label="자원 한도">{group("limit")}
        <EditRow label="계획에서 뺀 장비" help="운영 판단이 이 장비에는 서비스를 놓거나 빼거나 전력 모드를 바꾸지 않아요. 측정 중인 장비처럼 그대로 둬요." value={excludedNodes(shown)} changed={!!count&&String(baseline!.excludedNodes??[])!==String(shown.excludedNodes??[])}
          initial={(shown.excludedNodes??[]).join(", ")||"없음"} range="쉼표로 구분한 장비 이름, 비우려면 없음" save={value=>{const names=nodeList(value as string);edit(base=>({...base,excludedNodes:names}))}} disabled={blocked}/>
      </Rows>
      <Rows label="자동 조정 항목">{controls.map(control=><EditRow key={control.key} label={control.label} help={`운영 판단에서 ${control.label}도 함께 조정해요.`} value={onOff(shown.controls[control.key])} changed={!!count&&baseline!.controls[control.key]!==shown.controls[control.key]}
        initial={shown.controls[control.key]} save={value=>edit(base=>({...base,controls:{...base.controls,[control.key]:value as boolean}}))} disabled={blocked}/>)}</Rows>
      <Rows label="고급">{group("advanced")}</Rows>
      <Rows>
        <Row href="/changes?scope=scheduler" label="최근 변경 기록" hint="누가 언제 운영 설정을 바꿨는지"/>
        {routing
          ?<Row href={`/transitions/${encodeURIComponent(routing.operation.id)}?${new URLSearchParams({view:"current",stage:"result",node:routing.operation.node.uid,startedAt:routing.startedAt})}`} label="설정한 요청 분배" hint="가장 최근에 적용한 서비스 → 인스턴스 비율"/>
          :<Row href="/transitions" label="설정한 요청 분배" hint={state.data?"설정 기반 분배 기록이 비어 있어요":state.error?"분배 기록을 불러오지 못했어요":"분배 기록을 확인하고 있어요"}/>}
        <RoutingEdit initial={settings.data?.staticRouting} disabled={blocked||count>0} apply={(staticRouting,reason)=>void applyRouting(staticRouting,reason)}/>
        <Row href="/scheduler" label="지금 운영 판단 보기"/>
        {settings.data&&<RowDialog label="원본 자료 보기" title="현재 운영 설정 원본"><pre className="source-json">{stringifySchedulerJSON(safeSource(settings.data),2)}</pre><SourceExport value={settings.data} filename="chill-scheduler-settings.json"/></RowDialog>}
      </Rows>
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
