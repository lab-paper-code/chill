"use client"

import * as React from "react"
import { useQueryClient } from "@tanstack/react-query"
import { useChillQuery, useChillMutation, useSession, ApiError } from "@/lib/client"
import type { SchedulerAlgorithm, SchedulerSettings, SchedulerSettingsChange, SchedulerControllerState, SchedulerObservationPage, WireInteger } from "@/lib/read-types"
import { millisecondsToNanos, nanosToMilliseconds, wireInteger, preciseJSONSupported, parseSchedulerJSON, stringifySchedulerJSON, settingsEqual } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"
import { QueryGate, SourceJSON, SourceExport } from "@/components/read-region"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

type NumericKey = Exclude<keyof SchedulerSettings,"controls"|"algorithm">
// internal/serving/scheduler_policy.go: the owner rejects any other value.
export const algorithms:Record<SchedulerAlgorithm,{label:string;help:string}> = {
  "fixed-horizon":{label:"고정 기간 비교",help:"후보마다 정한 기간 동안의 에너지를 비교해요."},
  "projected-work-history":{label:"작업 이력 예측",help:"최근 작업 이력으로 앞으로의 부하를 예측해 비교해요."},
}
export const algorithmLabel = (value:string) => algorithms[value as SchedulerAlgorithm]?.label ?? "확인이 필요해요"
type Field = {key:NumericKey;label:string;help:string;group:"cycle"|"limit"|"advanced";unit?:string}
const fields: Field[] = [
  {key:"observationWindowNanos",label:"부하를 살펴볼 구간",help:"판단할 때 최근 요청 부하를 이만큼 살펴봐요.",group:"cycle"},
  {key:"controlIntervalNanos",label:"운영 판단 주기",help:"이 간격마다 새로 판단해요.",group:"cycle"},
  {key:"horizonNanos",label:"에너지 비교 기간",help:"후보마다 앞으로 이 기간의 에너지를 비교해요.",group:"cycle"},
  {key:"maxObservationAgeNanos",label:"관측을 쓸 수 있는 시간",help:"이보다 오래된 관측은 판단에 쓰지 않아요.",group:"cycle"},
  {key:"servingMemoryBytes",label:"서비스 실행 메모리",help:"모델 메모리 외에 인스턴스마다 확보할 실행 메모리예요. 노드 자원은 배포 설정에서 관리해요.",group:"limit"},
  {key:"maxReplicas",label:"복제 수 상한",help:"서비스 복제 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"maxInstances",label:"인스턴스 수 상한",help:"인스턴스 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"maxCPUs",label:"CPU 수 상한",help:"CPU 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"replays",label:"비교 반복 횟수",help:"후보마다 비교를 반복하는 횟수예요.",group:"advanced",unit:"회"},
  {key:"maxCandidates",label:"후보 수 상한",help:"한 번에 비교할 후보 수의 상한이에요.",group:"advanced",unit:"개"},
  {key:"maxReplayRequests",label:"비교 요청 수 상한",help:"반복 비교에 쓰는 요청 수의 상한이에요.",group:"advanced",unit:"건"},
  {key:"seed",label:"비교 재현용 시드",help:"같은 조건을 다시 만들 때 쓰는 값이에요.",group:"advanced"},
]
const controls: {key:keyof SchedulerSettings["controls"];label:string}[] = [
  {key:"enabled",label:"자동 적용"},{key:"placement",label:"배치 위치"},{key:"resources",label:"자원 할당"},
  {key:"replicas",label:"복제 수"},{key:"sharing",label:"인스턴스 공유"},{key:"routing",label:"요청 분배"},
  {key:"batching",label:"배칭"},{key:"power",label:"전력모드"},{key:"membership",label:"실행 그룹 소속"},
]
const onOff = (on:boolean) => on?"사용":"사용 안 함"
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
    ...(before.algorithm!==after.algorithm?[["판단 방식",`${algorithmLabel(before.algorithm)} → ${algorithmLabel(after.algorithm)}`] as [string,string]]:[]),
    ...fields.filter(field=>String(before[field.key])!==String(after[field.key])).map(field=>[field.label,`${plain(field,before[field.key])} → ${plain(field,after[field.key])}`] as [string,string]),
    ...controls.filter(control=>before.controls[control.key]!==after.controls[control.key]).map(control=>[control.label,`${onOff(before.controls[control.key])} → ${onOff(after.controls[control.key])}`] as [string,string]),
  ]
}
export function SettingChanges({before,after,stored=false}:{before:SchedulerSettings;after:SchedulerSettings;stored?:boolean}) {
  const rows=changedRows(before,after)
  return rows.length?<Facts items={rows}/>:<p>{stored?"기록의 전후 설정이 같아요.":"바꾼 값과 현재 설정이 같아요."}</p>
}
export function SettingsSummary({settings}:{settings:SchedulerSettings}) {
  return <Facts items={[["판단 방식",algorithmLabel(settings.algorithm)],...fields.map(field=>[field.label,plain(field,settings[field.key])] as [string,string]),...controls.map(control=>[control.label,onOff(settings.controls[control.key])] as [string,string])]}/>
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

function AlgorithmRow({value,changed,save,disabled}:{value:SchedulerAlgorithm;changed:boolean;save:(value:SchedulerAlgorithm)=>void;disabled:boolean}) {
  const [open,setOpen]=React.useState(false),[next,setNext]=React.useState(value)
  return <RowDialog label="운영 판단 알고리즘" hint={changed?"바꾼 값이에요":undefined} value={algorithmLabel(value)} title="운영 판단 알고리즘" description="바꾸면 다음 판단부터 이 방식으로 후보를 비교해요. 자동 적용이 꺼져 있으면 실제 구성은 바뀌지 않아요." open={open} onOpenChange={state=>{setOpen(state);setNext(value)}}>
    <form className="grid gap-4" onSubmit={event=>{event.preventDefault();save(next);setOpen(false)}}>
      <ul className="rows">{(Object.keys(algorithms) as SchedulerAlgorithm[]).map(key=><li key={key}><label className="row"><input type="radio" name="algorithm" checked={next===key} onChange={()=>setNext(key)}/><span className="row-main"><span className="row-text">{algorithms[key].label}</span><span className="row-hint">{algorithms[key].help}</span></span></label></li>)}</ul>
      <Button type="submit" disabled={disabled}>바꾸기</Button>
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
  async function save() {
    if(!review||blocked||saving.current)return
    saving.current=true;setSubmittedAt(new Date().toISOString());setRecoveryStarted(0);setHistoryWindow(null)
    const selected=review;setReview(null);setReviewOpen(false);setResult(null);setResponseError(null)
    try {
      const response=await mutation.mutateAsync({path:"v1/scheduler/settings",method:"PUT",body:stringifySchedulerJSON(selected)})
      const applied=parseSchedulerJSON(response.body) as SchedulerSettingsChange
      setResult(applied);setBaseline(null);setDraft(null)
      await client.invalidateQueries({predicate:query=>typeof query.queryKey[1]==="string"&&(query.queryKey[1].startsWith("v1/scheduler")||query.queryKey[1].startsWith("v1/observations/scheduler"))})
    } catch(error) {if(!(error instanceof ApiError))setResponseError("적용 응답을 해석하지 못했어요. 현재 설정과 최근 변경 기록을 확인해 주세요.")}
    finally {saving.current=false}
  }
  async function recover() {
    const now=Date.now();setRecoveryStarted(now)
    setHistoryWindow({from:new Date(Date.parse(submittedAt)-60000).toISOString(),to:new Date(now).toISOString()})
    await settings.refetch()
  }

  if(result)return <Screen back={back} title={result.appliedAt?"설정을 적용했어요":"적용 결과를 확인해 주세요"} lede="실제 구성 변경은 따로 진행돼요. 진행 상황은 아래에서 볼 수 있어요." action={<Button onClick={()=>setResult(null)}>확인</Button>}>
    <Facts items={[["적용 시각",result.appliedAt?time(result.appliedAt):"아직 없어요"],["확인 시각",time(result.validatedAt)],["바꾼 사람",result.actorUsername],["마지막으로 확인한 노드",result.affected?.map(node=>node.name).join(", ")||"없음"],...(result.error?[["오류",result.error] as [string,string]]:[])]}/>
    <Rows><Row href="/transitions" label="실제 변경 진행 보기"/><RowDialog label="원본 자료 보기" title="적용 결과 원본"><SourceJSON inline value={result}/></RowDialog></Rows>
  </Screen>

  if(mutation.error||responseError) {
    const message=mutation.error?.status===403?"조회 권한과 별도로 설정 변경 권한과 설정 저장소(ConfigMap) 수정 권한이 모두 필요해요.":responseError??serverError?.message??mutation.error?.body
    const field=serverError?.field&&(fields.find(item=>item.key===serverError.field)?.label??(serverError.field==="settings"?"운영 설정 전체":serverError.field))
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
    range={field.key.endsWith("Nanos")?"0보다 큰 밀리초, 소수점 아래 6자리까지":field.key==="seed"?"-9223372036854775808 ~ 9223372036854775807 사이 정수":field.key==="servingMemoryBytes"?"0 ~ 18446744073709551615 사이 바이트 수":"1 이상의 정수"}
    save={value=>{
      // Same validation as the wire: ms with ≤6 decimals → nanos, signed int64 seed, uint64 memory bytes, positive int64 otherwise.
      const text=value as string,parsed=field.key.endsWith("Nanos")?millisecondsToNanos(text):field.key==="seed"?wireInteger(text,-9223372036854775808n,9223372036854775807n):field.key==="servingMemoryBytes"?wireInteger(text,0n,18446744073709551615n):wireInteger(text,1n,9223372036854775807n)
      edit(base=>({...base,[field.key]:parsed}))
    }} disabled={blocked}/>)
  return <Screen back={back} title="운영 설정"
    lede={!precise?"이 브라우저는 큰 정수를 정확히 다루지 못해 설정을 바꿀 수 없어요. 최신 브라우저에서 열어 주세요.":settings.data?(settings.data.controls.enabled?"판단 결과를 자동으로 적용하고 있어요.":"판단만 하고 자동으로 적용하지는 않아요."):undefined}
    action={count>0&&<Button ref={reviewLauncher} onClick={()=>void inspect()} disabled={blocked}>{reviewing?"현재 값을 확인하고 있어요":`변경 ${count}건 검토`}</Button>}>
    <QueryGate query={settings}>{shown&&<>
      <Rows label="판단 방식"><AlgorithmRow value={shown.algorithm} changed={!!count&&baseline!.algorithm!==shown.algorithm} save={algorithm=>edit(base=>({...base,algorithm}))} disabled={blocked}/></Rows>
      <Rows label="판단 주기와 범위">{group("cycle")}</Rows>
      <Rows label="자원 한도">{group("limit")}</Rows>
      <Rows label="자동 조정 항목">{controls.map(control=><EditRow key={control.key} label={control.label} help={control.key==="enabled"?"켜면 판단 결과를 실제로 적용해요. 끄면 판단만 해요.":`운영 판단에서 ${control.label}도 함께 조정해요.`} value={onOff(shown.controls[control.key])} changed={!!count&&baseline!.controls[control.key]!==shown.controls[control.key]}
        initial={shown.controls[control.key]} save={value=>edit(base=>({...base,controls:{...base.controls,[control.key]:value as boolean}}))} disabled={blocked}/>)}</Rows>
      <Rows label="고급">{group("advanced")}</Rows>
      <Rows>
        <Row href="/changes?scope=scheduler" label="최근 변경 기록" hint="누가 언제 운영 설정을 바꿨는지"/>
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
