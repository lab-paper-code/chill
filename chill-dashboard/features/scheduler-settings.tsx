"use client"

import * as React from "react"
import Link from "next/link"
import { useQueryClient, type QueryObserverBaseResult } from "@tanstack/react-query"
import { useChillQuery, useChillMutation, useSession, ApiError } from "@/lib/client"
import type { SchedulerSettings, SchedulerSettingsChange, SchedulerControllerState, SchedulerObservationPage } from "@/lib/read-types"
import { millisecondsToNanos, nanosToMilliseconds, wireInteger, preciseJSONSupported, parseSchedulerJSON, stringifySchedulerJSON, settingsEqual } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"
import { ReadRegion, SourceJSON, SourceExport } from "@/components/read-region"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

type NumericKey = Exclude<keyof SchedulerSettings,"controls">
const fields: {key:NumericKey;label:string;help:string;advanced?:boolean}[] = [
  {key:"observationWindowNanos",label:"부하를 관찰할 구간",help:"입력 관측에 사용할 시간입니다."},
  {key:"controlIntervalNanos",label:"운영 판단 주기",help:"새 운영 판단 사이의 시간입니다."},
  {key:"horizonNanos",label:"에너지 비교 기간",help:"후보의 미래 에너지를 비교할 시간입니다."},
  {key:"maxObservationAgeNanos",label:"관측을 사용할 최대 경과 시간",help:"제공된 관측을 사용할 최대 나이입니다."},
  {key:"servingMemoryBytes",label:"Serving 메모리 예산",help:"실측 모델 메모리 외에 인스턴스 할당 안에서 확보할 Serving 메모리입니다. 정확한 bytes 값을 입력합니다."},
  {key:"replays",label:"비교 반복 횟수",help:"후보별 반복 비교 횟수입니다.",advanced:true},
  {key:"maxCandidates",label:"후보 수 상한",help:"비교할 후보 수의 상한입니다.",advanced:true},
  {key:"maxReplicas",label:"복제 수 상한",help:"탐색할 서비스 복제 수의 상한입니다.",advanced:true},
  {key:"maxInstances",label:"인스턴스 수 상한",help:"탐색할 인스턴스 수의 상한입니다.",advanced:true},
  {key:"maxReplayRequests",label:"비교 요청 수 상한",help:"반복 비교에 사용할 요청 수의 상한입니다.",advanced:true},
  {key:"maxCPUs",label:"CPU 수 상한",help:"탐색할 CPU 수의 상한입니다.",advanced:true},
  {key:"seed",label:"반복 비교의 시드",help:"같은 조건을 재현하기 위한 signed int64 값입니다.",advanced:true},
]
const controls: {key:keyof SchedulerSettings["controls"];label:string}[] = [
  {key:"enabled",label:"자동 적용"},{key:"placement",label:"배치 위치"},{key:"resources",label:"자원 할당"},
  {key:"replicas",label:"복제 수"},{key:"sharing",label:"인스턴스 공유"},{key:"routing",label:"요청 분배"},
  {key:"batching",label:"배칭"},{key:"power",label:"전력모드"},{key:"membership",label:"실행 그룹 소속"},
]
export function SettingChanges({before,after,stored=false}:{before:SchedulerSettings;after:SchedulerSettings;stored?:boolean}) {
  const rows=[...fields.map(field=>({label:field.label,before:String(before[field.key]),after:String(after[field.key]),unit:field.key.endsWith("Nanos")?"ns":field.key==="servingMemoryBytes"?"bytes":""})),...controls.map(control=>({label:control.label,before:before.controls[control.key]?"사용":"미사용",after:after.controls[control.key]?"사용":"미사용",unit:""}))].filter(row=>row.before!==row.after)
  return rows.length?<div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr><th>항목</th><th>변경 전</th><th>{stored?"변경 후":"작성 값"}</th></tr></thead><tbody>{rows.map(row=><tr key={row.label}><td>{row.label}</td><td className="break-all">{row.before} {row.unit}</td><td className="break-all">{row.after} {row.unit}</td></tr>)}</tbody></table></div>:<p>{stored?"기록의 전후 설정이 같습니다.":"작성 값과 현재 설정이 같습니다."}</p>
}
export function SettingsSummary({settings}:{settings:SchedulerSettings}) {
  return <dl className="grid gap-3">{fields.map(field=><React.Fragment key={field.key}><dt>{field.label}</dt><dd>{field.key.endsWith("Nanos")?`${nanosToMilliseconds(settings[field.key])} ms`:String(settings[field.key])}{field.key==="servingMemoryBytes"?` bytes (약 ${Number(settings.servingMemoryBytes)/1048576} MiB)`:""}</dd></React.Fragment>)}{controls.map(control=><React.Fragment key={control.key}><dt>{control.label}</dt><dd>{settings.controls[control.key]?"사용":"미사용"}</dd></React.Fragment>)}</dl>
}
export function SchedulerSettingsPage() {
  const settings=useChillQuery<SchedulerSettings>("v1/scheduler/settings",{decoder:parseSchedulerJSON,poll:true})
  const [editing,setEditing]=React.useState(false)
  return <div className="page"><h1>운영 설정</h1><p>현재 값에서 수정하고 변경 전후를 검토합니다. 정적 자원 선언은 배포 설정에서 관리합니다.</p><ReadRegion title="현재 운영 설정" query={settings}>{settings.data&&<>{editing?<SettingsEditor initial={settings.data} settings={settings} finish={()=>setEditing(false)}/>:<><SettingsSummary settings={settings.data}/><Button onClick={()=>setEditing(true)} disabled={!preciseJSONSupported()}>수정</Button><SourceJSON value={settings.data}/><SourceExport value={settings.data} filename="chill-scheduler-settings.json"/></>}</>}</ReadRegion><Link href="/changes?scope=scheduler">운영 설정 변경 기록</Link><Link href="/scheduler">운영 판단</Link></div>
}
function SettingsEditor({initial,settings,finish}:{initial:SchedulerSettings;settings:QueryObserverBaseResult<SchedulerSettings,ApiError>;finish:()=>void}) {
  const [baseline,setBaseline]=React.useState(initial)
  const reviewLauncher=React.useRef<HTMLButtonElement|null>(null)
  const [values,setValues]=React.useState(()=>Object.fromEntries(fields.map(field=>[field.key,field.key.endsWith("Nanos")?nanosToMilliseconds(initial[field.key]):String(initial[field.key])])) as Record<NumericKey,string>)
  const [draftControls,setDraftControls]=React.useState({...initial.controls})
  const [validation,setValidation]=React.useState<{field?:NumericKey;message:string}|null>(null)
  const [responseError,setResponseError]=React.useState<string|null>(null)
  const [review,setReview]=React.useState<SchedulerSettings|null>(null),[latest,setLatest]=React.useState<SchedulerSettings|null>(null)
  const [reviewing,setReviewing]=React.useState(false),[result,setResult]=React.useState<SchedulerSettingsChange|null>(null)
  const [submittedAt,setSubmittedAt]=React.useState("")
  const [historyWindow,setHistoryWindow]=React.useState<{from:string;to:string}|null>(null)
  const [recoveryStarted,setRecoveryStarted]=React.useState(0)
  const {config}=useSession(),client=useQueryClient(),mutation=useChillMutation(),saving=React.useRef(false)
  const state=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON})
  const history=useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${new URLSearchParams(historyWindow??{from:submittedAt,to:submittedAt})}&limit=${config.pageSize}`,{decoder:parseSchedulerJSON,enabled:!!historyWindow})
  const unknown=!!responseError||(mutation.error?.unknownOutcome??false)
  const recovered=recoveryStarted>0&&settings.dataUpdatedAt>=recoveryStarted&&!settings.isFetching&&history.isSuccess&&!history.isFetching
  const blocked=mutation.isPending||reviewing||(unknown&&!recovered)||!preciseJSONSupported()||[401,403].includes(settings.error?.status??0)
  const serverError=mutation.error?.data&&typeof mutation.error.data==="object"?mutation.error.data as {field?:string;message?:string;code?:string}:undefined
  let draft:SchedulerSettings|null={...baseline,controls:{...draftControls}}
  let invalid:{field:NumericKey;message:string}|null=null
  for(const field of fields){try {draft[field.key]=field.key.endsWith("Nanos")?millisecondsToNanos(values[field.key]):field.key==="seed"?wireInteger(values[field.key],-9223372036854775808n,9223372036854775807n):field.key==="servingMemoryBytes"?wireInteger(values[field.key],0n,18446744073709551615n):wireInteger(values[field.key],1n,9223372036854775807n)}catch(error){invalid={field:field.key,message:error instanceof Error?error.message:"정수를 확인하세요."};draft=null;break}}
  async function inspect() {
    if(blocked)return
    setValidation(invalid)
    if(!draft)return
    setReviewing(true);setReview(null);setResult(null)
    try {
      const [current]=await Promise.all([settings.refetch(),state.refetch()])
      if(current.error||!current.data){setValidation({message:"현재 설정 조회가 필요합니다. 입력은 유지합니다."});return}
      if(!settingsEqual(baseline,current.data)){setLatest(current.data);return}
      setLatest(null);setReview(draft)
    } finally {setReviewing(false)}
  }
  async function save() {
    if(!review||blocked||saving.current)return
    saving.current=true;setSubmittedAt(new Date().toISOString());setRecoveryStarted(0);setHistoryWindow(null)
    const selected=review;setReview(null);setResult(null);setResponseError(null)
    try {
      const response=await mutation.mutateAsync({path:"v1/scheduler/settings",method:"PUT",body:stringifySchedulerJSON(selected)})
      const applied=parseSchedulerJSON(response.body) as SchedulerSettingsChange
      setResult(applied);setBaseline(applied.after)
      await client.invalidateQueries({predicate:query=>typeof query.queryKey[1]==="string"&&(query.queryKey[1].startsWith("v1/scheduler")||query.queryKey[1].startsWith("v1/observations/scheduler"))})
    } catch(error) {if(!(error instanceof ApiError))setResponseError("적용 응답을 해석하지 못했습니다. 현재 설정과 최근 변경 기록을 조회합니다.")}
    finally {saving.current=false}
  }
  async function recover() {
    const now=Date.now();setRecoveryStarted(now)
    setHistoryWindow({from:new Date(Date.parse(submittedAt)-60000).toISOString(),to:new Date(now).toISOString()})
    await settings.refetch()
  }
  function input(field:typeof fields[number]) {
    const duration=field.key.endsWith("Nanos"),error=validation?.field===field.key?validation.message:undefined
    return <label className="grid gap-2" key={field.key} htmlFor={`setting-${field.key}`}>{field.label} {duration?"(ms)":field.key==="servingMemoryBytes"?"(bytes)":""}<Input id={`setting-${field.key}`} name={field.key} required inputMode={duration?"decimal":"text"} value={values[field.key]} onChange={event => {setValues(current=>({...current,[field.key]:event.target.value}));setValidation(null)}} aria-invalid={!!error} aria-describedby={`help-${field.key}`}/><span id={`help-${field.key}`} className="pl-3 text-sm text-muted-foreground">{field.help} 필수. {duration?"양수, ms 소수 6자리까지, 최대 9223372036854775807 ns":field.key==="seed"?"-9223372036854775808~9223372036854775807":field.key==="servingMemoryBytes"?"0~18446744073709551615 bytes": "1~9223372036854775807"}{field.key==="servingMemoryBytes"&&draft?` / ${Number(draft.servingMemoryBytes)/1048576} MiB`:""}</span>{error&&<span role="alert">{error}</span>}</label>
  }
  return <section className="grid gap-5"><h2>설정 수정</h2>{!preciseJSONSupported()&&<p role="alert">정밀 JSON을 지원하는 최신 브라우저에서 저장합니다. 읽은 정수를 반올림하여 저장하는 경로는 제공하지 않습니다.</p>}{validation&&<p role="alert">{validation.message}</p>}{(mutation.error || responseError) && <section role="alert"><h3>{unknown?"결과 확인 필요":mutation.error?.status===403?"설정 변경 권한 확인 필요":"설정을 적용하지 못했습니다."}</h3><p>{mutation.error?.status===403?"설정 PUT 경로 권한과 지정된 ConfigMap update 권한이 모두 필요합니다. 조회 권한과 별개입니다.":responseError ?? serverError?.message ?? mutation.error?.body}</p>{serverError?.field&&<p>관련 항목: {serverError.field}</p>}<p>작성 값은 유지합니다. 자동으로 다시 보내지 않습니다.</p></section>}{unknown&&<section><Button variant="outline" onClick={()=>void recover()} disabled={settings.isFetching||history.isFetching}>현재 설정과 최근 변경 기록 조회</Button>{historyWindow&&<ReadRegion title="최근 가져온 설정 변경 기록" query={history}>{history.data&&<>{(history.data.events??[]).filter(event=>event.settings).map((event,index)=><p key={index}>{event.at} / {event.settings!.actorUsername} / {event.settings!.appliedAt?`설정 적용 ${event.settings!.appliedAt}`:event.settings!.error??"검증된 변경 의도"}</p>)}<p>현재 페이지의 기록입니다. {history.data.nextCursor?"다음 기록은 변경 기록 화면에서 확인합니다.":""}</p><Link href={`/changes?scope=scheduler&${new URLSearchParams(historyWindow)}`}>이 구간의 변경 기록</Link></>}</ReadRegion>}{recovered&&settings.data&&<><SettingChanges before={settings.data} after={draft??baseline}/><Button onClick={() => {setBaseline(settings.data!);setLatest(null);setResponseError(null);mutation.reset();setHistoryWindow(null)}}>조회한 현재 값을 기준으로 다시 검토</Button></>}</section>}
    {latest&&<section role="alert"><h3>다른 사용자가 설정을 변경했습니다.</h3><p>최신 값과 작성 값을 비교합니다. 입력은 유지합니다.</p><SettingChanges before={latest} after={draft??baseline}/><Button onClick={()=>{setBaseline(latest);setLatest(null)}}>최신 값을 기준으로 다시 검토</Button></section>}
    <form onSubmit={event=>{event.preventDefault();void inspect()}} className="grid gap-5"><fieldset disabled={blocked} className="grid gap-5"><legend>시간과 메모리</legend>{serverError?.field==="settings"&&<p role="alert">서버 설정 검증: {serverError.message}</p>}{fields.filter(field=>!field.advanced).map(input)}</fieldset><fieldset disabled={blocked} className="grid gap-3"><legend>자동 적용과 제어요소</legend><p className="text-sm text-muted-foreground">자동 적용 상태와 각 비교 요소를 설정합니다. 실제 판단과 실행은 스케줄러가 수행합니다.</p>{controls.map(control=><label key={control.key}><input type="checkbox" name={control.key} checked={draftControls[control.key]} onChange={event=>setDraftControls(current=>({...current,[control.key]:event.target.checked}))}/>{control.label}</label>)}</fieldset><details><summary>상세 설정</summary><fieldset disabled={blocked} className="grid gap-5">{fields.filter(field=>field.advanced).map(input)}</fieldset></details><div className="toolbar"><Button ref={reviewLauncher} type="submit" disabled={blocked}>{reviewing?"현재 값 확인 중":"저장 검토"}</Button><Button type="button" variant="outline" onClick={finish} disabled={mutation.isPending}>수정 마치기</Button></div></form>
    <Dialog open={!!review} onOpenChange={open=>{if(!open)setReview(null)}}><DialogContent className="max-h-[85dvh] overflow-y-auto" onCloseAutoFocus={event=>{event.preventDefault();reviewLauncher.current?.focus()}}><DialogHeader><DialogTitle>운영 설정 저장 검토</DialogTitle><DialogDescription>저장 시 전체 운영 설정을 적용합니다. 여러 사용자가 변경하면 마지막 적용값이 현재 설정이 됩니다.</DialogDescription></DialogHeader>{review&&<><SettingChanges before={baseline} after={review}/><h3>마지막 구성 관측의 노드</h3>{state.isSuccess ? (state.data?.snapshot.nodes?.length ? state.data.snapshot.nodes.map(node=><p key={node.node.uid}>{node.node.name} / {node.node.uid}</p>) : <p>제공된 노드 목록 없음</p>) : <p>노드 목록 조회 필요</p>}<p>마지막 snapshot의 목록이며 영향 예측이 아닙니다. 사전 재조회는 동시 변경 방지를 보장하지 않습니다.</p><details><summary>변경 원본</summary><pre className="source-json">{stringifySchedulerJSON(safeSource({before:baseline,after:review}),2)}</pre></details><Button onClick={()=>void save()} disabled={blocked}>전체 설정 적용</Button></>}</DialogContent></Dialog>
    {result&&<section role="status"><h3>{result.appliedAt?"설정 저장과 Runtime 적용 완료":"설정 적용 결과 확인"}</h3><p>검증 {result.validatedAt} / 설정 적용 {result.appliedAt??"제공 없음"}</p><p>행위자 {result.actorUsername}</p><p>적용 응답의 마지막 관측 노드: {result.affected?.map(node=>node.name).join(", ")||"없음"}</p>{result.error&&<p>{result.error}</p>}<Link href="/transitions">실제 물리 변경 진행 확인</Link><SourceJSON value={result}/></section>}
  </section>
}
