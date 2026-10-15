"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { ObservationResult, RequestEvent, WireInteger } from "@/lib/read-types"
import { QueryGate, SourceJSON, SourceExport, StatusBadge, statusLabel } from "@/components/read-region"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { ChartAreaInteractive } from "@/components/chart-area-interactive"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { parseSchedulerJSON, nanosToMilliseconds } from "@/lib/scheduler"
import { localDateTime, utcDateTime } from "@/lib/date-time"
import { when, actorName } from "@/features/scheduler-history"

type UIDChoice={desired:{metadata:{name:string;uid:string}}}
const outcomeLabels:Record<string,string>={completed:"완료",error:"오류",rejected:"접수 거부",timeout:"시간 초과",canceled:"취소",incomplete:"미완료",delivery_failed:"전달 실패"}
const outcomes=Object.keys(outcomeLabels)
const presets:[key:string,label:string,hours:number][]=[["15m","최근 15분",.25],["1h","최근 1시간",1],["24h","최근 24시간",24]]
function span(from:string,to:string) {
  const minutes=Math.round((Date.parse(to)-Date.parse(from))/60000)
  return minutes%60?`${minutes}분`:`${minutes/60}시간`
}
const ms=(value?:WireInteger)=>value===undefined?"미제공":`${nanosToMilliseconds(value)} ms`

export function ObservationsPage({changes=false}:{changes?:boolean}) {
  const uid=useSearchParams().get("uid")
  return uid?<ServiceRecords uid={uid} changes={changes}/>:<PickService changes={changes}/>
}

// Step 1: choose whose records to read.
function PickService({changes}:{changes:boolean}) {
  const router=useRouter(),path=changes?"/changes":"/observations"
  const workloads=useChillQuery<UIDChoice[]>("v1/workloads")
  const [uid,setUID]=React.useState("")
  return <Screen back={["/records","기록"]} title={changes?"어떤 서비스의 변경 이력을 볼까요?":"어떤 서비스의 요청 기록을 볼까요?"} lede="서비스를 고르면 최근 1시간 기록부터 보여드려요.">
    <QueryGate query={workloads}>{workloads.data?.length?<Rows label="서비스">{workloads.data.map(row=><Row key={row.desired.metadata.uid} href={`${path}?uid=${encodeURIComponent(row.desired.metadata.uid)}`} label={row.desired.metadata.name}/>)}</Rows>:<Empty title="아직 등록된 서비스가 없어요"/>}</QueryGate>
    <Rows>
      <RowDialog label="이전 등록 서비스 찾기" hint="삭제된 서비스의 기록을 볼 때" title="이전 등록 서비스 찾기" description="삭제된 서비스의 원본 참조를 알고 있을 때 사용하세요. 이름이 같아도 새로 등록한 서비스와 기록이 달라요.">
        <form className="grid gap-3" onSubmit={event=>{event.preventDefault();if(uid.trim())router.push(`${path}?uid=${encodeURIComponent(uid.trim())}`)}}><label className="grid gap-1.5">이전 등록 UID<Input value={uid} onChange={event=>setUID(event.target.value)} required/></label><Button type="submit">기록 찾기</Button></form>
      </RowDialog>
      {changes&&<Row href="/changes?scope=scheduler" label="운영 설정 변경 이력" hint="누가 언제 운영 설정을 바꿨는지"/>}
    </Rows>
  </Screen>
}

// Step 2: one service, one window, one list.
function ServiceRecords({uid,changes}:{uid:string;changes:boolean}) {
  const params=useSearchParams(),router=useRouter(),{config}=useSession()
  const path=changes?"/changes":"/observations",from=params.get("from"),to=params.get("to")
  const [poll,setPoll]=React.useState(false)
  React.useEffect(()=>{
    if(from&&to&&params.has("limit"))return
    const next=new URLSearchParams(params.toString()),end=new Date()
    if(!from)next.set("from",new Date(end.getTime()-3600000).toISOString());if(!to)next.set("to",end.toISOString());if(!next.has("limit"))next.set("limit",String(config.pageSize))
    router.replace(`${path}?${next}`)
  },[from,to,params,path,router,config.pageSize])
  const queryText=new URLSearchParams()
  for(const key of ["from","to","limit","cursor","definition","outcome"])params.getAll(key).forEach(value=>queryText.append(key,value))
  const query=useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(uid)}?${queryText}`,{enabled:!!from&&!!to,poll,decoder:changes?undefined:parseSchedulerJSON})
  const workloads=useChillQuery<UIDChoice[]>("v1/workloads")
  const name=workloads.data?workloads.data.find(row=>row.desired.metadata.uid===uid)?.desired.metadata.name??"이전 등록 서비스":"서비스"
  function page(cursor?:string) {
    // A page belongs to the exact submitted window, never the unsaved form.
    const next=new URLSearchParams(params.toString());if(cursor)next.set("cursor",cursor);else next.delete("cursor");router.replace(`${path}?${next}`)
  }
  const data=query.data,definitions=data?.definitions??[],period=from&&to?span(from,to):""
  const definitionLabel=(identity:string)=>{const index=definitions.findIndex(item=>item.identity===identity);return index<0?"이전 적용 설정":`적용 설정 ${index+1}`}
  const more=!!data?.nextCursor||params.has("cursor"),count=data?.management?.length??0
  const title=!data?`${name} ${changes?"변경 이력":"요청 기록"}`:changes?(count?`${period} 동안 변경 ${count}건${more?" 이상":""}이 있었어요`:`${period} 동안 변경이 없었어요`):data.total?`${period} 동안 요청 ${data.total}건을 받았어요`:`${period} 동안 받은 요청이 없어요`
  return <Screen back={[path,"서비스 목록"]} status={data?.incompleteWindows?.length?<StatusBadge tone="warn">일부 기간 기록이 빠졌어요</StatusBadge>:undefined} title={title} lede={from&&to?`${name} · ${when(from)} – ${when(to)}`:name}>
    <Rows label="조회 조건">
      <Conditions changes={changes} path={path} definitions={definitions} poll={poll} setPoll={setPoll}/>
      {data&&!changes&&<RowDialog label="구간 통계" hint="결과별 건수와 응답 시간" title="구간 통계" description="고른 기간 전체를 집계했어요. 아래 요청 목록과 따로 계산해요.">
        <Facts items={[["전체 요청",`${data.total}건`],["조건에 맞는 요청",`${data.selected}건`],...Object.entries(data.outcomes).map(([key,value]):[string,string]=>[outcomeLabels[key]??"기타 결과",`${value}건`])]}/>
        {data.cohorts?.length?<><h3 className="font-semibold">적용 설정별 응답 시간</h3><Facts items={data.cohorts.map((row):[string,string]=>[definitionLabel(row.definition),`표본 ${row.sampleCount}건 · ${row.quantileNanos==null?"값 없음":`${row.percentile===undefined?"백분위 미제공":`${row.percentile}% 기준`} ${Number(row.quantileNanos)/1e6} ms`} · 목표 ${row.targetLatencyNanos==null?"없음":`${nanosToMilliseconds(row.targetLatencyNanos)} ms`}`])}/>
          <ChartAreaInteractive data={data.cohorts.map(row=>({definition:row.definition,quantileNanos:row.quantileNanos===null?null:Number(row.quantileNanos),label:definitionLabel(row.definition)}))}/></>:<p>응답 시간 표본이 없어요.</p>}
        <SourceJSON inline title="적용 설정 원본 보기" value={definitions}/>
      </RowDialog>}
      {data&&<RowDialog label="자료 내보내기" title="자료 내보내기" description="지금 보고 있는 조건과 결과를 파일로 받아요.">
        {!!data.incompleteWindows?.length&&<p role="alert">일부 기간의 기록이 불완전해요. 원본 자료에서 빠진 구간을 확인하세요.</p>}
        <SourceExport value={{conditions:{uid,query:Object.fromEntries(params.entries()),definition:params.getAll("definition"),outcome:params.getAll("outcome")},source:data}} filename={`chill-${changes?"changes":"observations"}-${data.uid}.json`}/>
        <SourceJSON inline title="조회 자료 원본 보기" value={{conditions:{uid,from,to,definition:params.getAll("definition"),outcome:params.getAll("outcome"),cursor:params.get("cursor"),limit:params.get("limit")},source:data}}/>
      </RowDialog>}
      <Row href={`${changes?"/observations":"/changes"}?${params}`} label={changes?"같은 기간 요청 기록":"같은 기간 변경 이력"}/>
    </Rows>
    <QueryGate query={query}>{data&&<>
      {changes?(data.management?.length?<Rows label="변경">{data.management.map((row,index)=><RowDialog key={index} label={statusLabel(row.kind)} hint={`${when(row.at)} · ${actorName(row.actorUsername)}`} value={statusLabel(row.phase)} title="변경 자세히" description="변경 전후와 처리 결과예요.">
        <Facts items={[["시각",when(row.at)],["변경",statusLabel(row.kind)],["상태",statusLabel(row.phase)],["담당자",actorName(row.actorUsername)]]}/>{row.error&&<p role="alert">{row.error}</p>}<SourceJSON inline title="변경 전후 원본 보기" value={row}/>
      </RowDialog>)}</Rows>:<Empty title="이 기간에는 변경이 없어요"/>)
      :data.requests?.length?<Rows label="요청">{data.requests.map((row,index)=><RowDialog key={index} label={row.outcome?outcomeLabels[row.outcome]??"기타 결과":statusLabel(row.kind)} hint={`${when(row.at)}${params.get("requestID")===row.requestID?" · 선택한 요청":""}`} value={ms(row.elapsedNanos)} title="요청 자세히" description="CHILL 배정과 실행 진행을 확인해요."><RequestDetails row={row}/></RowDialog>)}</Rows>:<Empty title="이 조건에 맞는 요청이 없어요"/>}
      {more&&<div className="flex gap-2"><Button variant="outline" disabled={!params.has("cursor")} onClick={()=>page()}>처음으로</Button><Button variant="outline" disabled={!data.nextCursor} onClick={()=>page(data.nextCursor)}>다음 기록 보기</Button></div>}
    </>}</QueryGate>
  </Screen>
}

function Conditions({changes,path,definitions,poll,setPoll}:{changes:boolean;path:string;definitions:ObservationResult["definitions"];poll:boolean;setPoll:(value:boolean)=>void}) {
  const params=useSearchParams(),[open,setOpen]=React.useState(false)
  const from=params.get("from")??"",to=params.get("to")??"",filters=params.getAll("outcome").length+params.getAll("definition").length
  return <RowDialog open={open} onOpenChange={setOpen} label="조회 조건" value={`${from&&to?span(from,to):""}${filters?` · 조건 ${filters}개`:""}${poll?" · 자동 새로고침":""}`} title="조회 조건" description="기간과 보고 싶은 결과를 골라요.">
    <ConditionsForm changes={changes} path={path} definitions={definitions} poll={poll} setPoll={setPoll} done={()=>setOpen(false)}/>
  </RowDialog>
}

// Mounted only while the dialog is open, so every open starts from the submitted URL.
function ConditionsForm({changes,path,definitions,poll,setPoll,done}:{changes:boolean;path:string;definitions:ObservationResult["definitions"];poll:boolean;setPoll:(value:boolean)=>void;done:()=>void}) {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),uid=params.get("uid")??""
  const [from,setFrom]=React.useState(params.get("from")??""),[to,setTo]=React.useState(params.get("to")??"")
  const [preset,setPreset]=React.useState(()=>{
    const minutes=Math.round((Date.parse(to)-Date.parse(from))/60000)
    return Math.abs(Date.now()-Date.parse(to))<120000&&presets.find(([,,hours])=>hours*60===minutes)?.[0]||"custom"
  })
  const [definition,setDefinition]=React.useState(params.getAll("definition")),[outcome,setOutcome]=React.useState(params.getAll("outcome"))
  const [limit,setLimit]=React.useState(Number(params.get("limit")??config.pageSize)),[validation,setValidation]=React.useState("")
  const toggle=(set:React.Dispatch<React.SetStateAction<string[]>>,value:string,on:boolean)=>set(current=>on?[...current,value]:current.filter(item=>item!==value))
  function submit() {
    const end=preset==="custom"?to:new Date().toISOString()
    const hours=presets.find(([key])=>key===preset)?.[2]??1
    const start=preset==="custom"?from:new Date(Date.parse(end)-hours*3600000).toISOString()
    const startTime=Date.parse(start),endTime=Date.parse(end)
    if(!uid||!Number.isFinite(startTime)||!Number.isFinite(endTime)||startTime>=endTime||endTime-startTime>config.maxWindowHours*3600000||!Number.isInteger(limit)||limit<1||limit>config.maxPageSize){setValidation(`올바른 기간을 골라 주세요. 최대 ${config.maxWindowHours}시간, 한 번에 1–${config.maxPageSize}건까지 볼 수 있어요.`);return}
    const next=new URLSearchParams({uid,from:start,to:end,limit:String(limit)})
    definition.forEach(value=>next.append("definition",value));outcome.forEach(value=>next.append("outcome",value))
    if(params.get("requestID"))next.set("requestID",params.get("requestID")!)
    router.replace(`${path}?${next}`);done()
  }
  return <form noValidate className="grid gap-5" onSubmit={event=>{event.preventDefault();submit()}}>
    <fieldset className="grid gap-2"><legend className="mb-1">기간</legend>
      {[...presets,["custom","직접 고르기",0] as const].map(([key,label])=><label key={key} className="flex items-center gap-2"><input type="radio" name="period" checked={preset===key} onChange={()=>setPreset(key)}/>{label}</label>)}
      {preset==="custom"&&<><label className="grid gap-1.5">시작<Input type="datetime-local" value={localDateTime(from)} onChange={event=>setFrom(utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:""))}/></label><label className="grid gap-1.5">종료<Input type="datetime-local" value={localDateTime(to)} onChange={event=>setTo(utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:""))}/></label><p className="text-sm text-muted-foreground">이 기기의 시간대 기준이에요. 최대 {config.maxWindowHours}시간까지 볼 수 있어요.</p></>}
    </fieldset>
    {!changes&&<fieldset className="grid gap-2"><legend className="mb-1">처리 결과</legend>
      {outcomes.map(value=><label key={value} className="flex items-center gap-2"><input type="checkbox" checked={outcome.includes(value)} onChange={event=>toggle(setOutcome,value,event.target.checked)}/>{outcomeLabels[value]}</label>)}
      {outcome.filter(value=>!outcomes.includes(value)).map(value=><label key={value} className="flex items-center gap-2"><input type="checkbox" checked onChange={()=>toggle(setOutcome,value,false)}/>이전 상세 조건</label>)}
    </fieldset>}
    {!changes&&<fieldset className="grid gap-2"><legend className="mb-1">적용 설정</legend>
      {definitions.length?definitions.map((item,index)=><div key={item.identity} className="flex flex-wrap items-center gap-2"><label className="flex items-center gap-2"><input type="checkbox" checked={definition.includes(item.identity)} onChange={event=>toggle(setDefinition,item.identity,event.target.checked)}/>적용 설정 {index+1}</label><SourceJSON title="설정 자세히" value={item}/></div>):<p className="text-sm text-muted-foreground">기록을 한 번 불러오면 실제 적용 설정을 고를 수 있어요.</p>}
      {definition.filter(value=>!definitions.some(item=>item.identity===value)).map(value=><label key={value} className="flex items-center gap-2"><input type="checkbox" checked onChange={()=>toggle(setDefinition,value,false)}/>이전 적용 설정</label>)}
    </fieldset>}
    <label className="grid gap-1.5">한 번에 볼 기록 수<Input type="number" min={1} max={config.maxPageSize} value={Number.isFinite(limit)?limit:""} onChange={event=>setLimit(Number(event.target.value))}/></label>
    <label className="flex items-center gap-2"><input type="checkbox" checked={poll} onChange={event=>setPoll(event.target.checked)}/>자동 새로고침</label>
    {validation&&<p role="alert">{validation}</p>}
    <Button type="submit">이 조건으로 보기</Button>
  </form>
}

function RequestDetails({row}:{row:RequestEvent}) {
  const assignment=row.assignment,native=row.nativeProgress
  return <div className="space-y-4 [&_dd]:break-all">
    <Facts items={[["요청",row.requestID],["서비스",`${row.workloadName} / ${row.workloadUID??"UID 미제공"}`],["적용 설정",row.definition??"미제공"],["단계와 시각",`${statusLabel(row.kind)} / ${row.at}`],["수신 시각",row.receivedAt],["입력 경로",row.inputPath??"미제공"],["요청 유효 시간",ms(row.lifetimeNanos)],["클라이언트 관측 시간",ms(row.clientSpanNanos)],["실행 완료 관측",row.executionComplete?"완료 확인":"완료 확인 대기"],["응답 목표",row.slo?`${row.slo.percentile}% 기준 / ${ms(row.slo.targetLatencyNanos)}`:"미제공"]]}/>
    <h3 className="font-semibold">CHILL 배정</h3>
    {assignment?<Facts items={[["실행 그룹과 노드",`${assignment.pool} / ${assignment.node.name} / ${assignment.node.uid}`],["인스턴스와 배정 시각",`${assignment.instance} / ${assignment.dispatchedAt}`],["실행 경로와 모델 자료",`${assignment.executionPath} / ${assignment.artifact}`],["프로파일과 신뢰 수준",`${assignment.profile} / ${assignment.profileConfidenceLevel}`]]}/>:<p>이 단계에는 배정 관측이 없어요.</p>}
    <h3 className="font-semibold">Native 실행 진행</h3>
    {native?<Facts items={[["실행 인스턴스",row.nativeInstance??"미제공"],["단계와 시각",`${statusLabel(native.phase)} / ${native.at}`],["실행 대상",`${native.requestID} / ${native.workloadUID} / ${native.definition}`],["배치 ID와 크기",`${native.batchID===undefined?"미제공":String(native.batchID)} / ${native.batchSize??"미제공"}`],["프로파일과 판단 ID",`${native.profileID??"미제공"} / ${native.decisionID??"미제공"}`],["큐 대기 / 서비스 실행 / Backend / Native 경과",`${native.queueMS} / ${native.serviceMS} / ${native.backendMS} / ${native.nativeElapsedMS} ms`],["실행 완료 관측",native.executionComplete?"완료 확인":"완료 확인 대기"],["처리 결과",native.outcome?outcomeLabels[native.outcome]??native.outcome:"미제공"],...(native.error?[["실행 오류",native.error] as [string,string]]:[])]}/>:<p>실행 인스턴스 {row.nativeInstance??"미제공"} · 이 단계에는 Native 실행 관측이 없어요.</p>}
    {row.error&&<p role="alert">{row.error}</p>}
    <SourceJSON inline value={row}/>
  </div>
}
