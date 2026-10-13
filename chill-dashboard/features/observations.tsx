"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { ObservationResult, ManagementEvent, RequestEvent, ObservationCohort } from "@/lib/read-types"
import { ReadRegion, SourceJSON, SourceExport, DetailsDialog, statusLabel } from "@/components/read-region"
import { DataTable } from "@/components/data-table"
import { ChartAreaInteractive } from "@/components/chart-area-interactive"
import { Button } from "@/components/ui/button"
import { parseSchedulerJSON, stringifySchedulerJSON, nanosToMilliseconds } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"

type UIDChoice={desired:{metadata:{name:string;uid:string}}}
const outcomes=["completed","error","rejected","timeout","canceled","incomplete","delivery_failed"]
const outcomeLabels:Record<string,string>={completed:"완료",error:"오류",rejected:"접수 거부",timeout:"시간 초과",canceled:"취소",incomplete:"미완료",delivery_failed:"전달 실패"}
function localDateTime(value:string) {
  const date=new Date(value)
  return Number.isFinite(date.getTime())?new Date(date.getTime()-date.getTimezoneOffset()*60000).toISOString().slice(0,16):""
}
export function ObservationsPage({changes=false}:{changes?:boolean}) {
  const params=useSearchParams(),router=useRouter();const {config}=useSession()
  const [uid,setUID]=React.useState(params.get("uid")??"")
  const [from,setFrom]=React.useState(()=>params.get("from")??new Date(Date.now()-3600000).toISOString())
  const [to,setTo]=React.useState(()=>params.get("to")??new Date().toISOString())
  const [preset,setPreset]=React.useState(params.has("from")||params.has("to")?"custom":"1h")
  const [definition,setDefinition]=React.useState(params.getAll("definition"))
  const [outcome,setOutcome]=React.useState(params.getAll("outcome"))
  const [limit,setLimit]=React.useState(Number(params.get("limit")??config.pageSize))
  const [poll,setPoll]=React.useState(false),[validation,setValidation]=React.useState("")
  const selectedUID=params.get("uid")??""
  const selectedWindow=!!selectedUID&&params.has("from")&&params.has("to")
  const queryText=new URLSearchParams()
  for(const key of ["from","to","limit","cursor","definition","outcome"])params.getAll(key).forEach(value=>queryText.append(key,value))
  const query=useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(selectedUID)}?${queryText}`,{enabled:selectedWindow,poll,decoder:changes?undefined:parseSchedulerJSON})
  const workloads=useChillQuery<UIDChoice[]>("v1/workloads")
  const path=changes?"/changes":"/observations"
  function selectWindow() {
    const end=preset==="custom"?to:new Date().toISOString()
    const hours=preset==="15m"?.25:preset==="24h"?24:1
    const start=preset==="custom"?from:new Date(Date.parse(end)-hours*3600000).toISOString()
    const startTime=Date.parse(start),endTime=Date.parse(end)
    if(!uid||!Number.isFinite(startTime)||!Number.isFinite(endTime)||startTime>=endTime||endTime-startTime>config.maxWindowHours*3600000||!Number.isInteger(limit)||limit<1||limit>config.maxPageSize){setValidation(`서비스와 올바른 조회 기간을 선택하세요. 최대 ${config.maxWindowHours}시간, 표시 개수는 1–${config.maxPageSize}입니다.`);return}
    const next=new URLSearchParams({uid,from:start,to:end,limit:String(limit)})
    definition.forEach(value=>next.append("definition",value));outcome.forEach(value=>next.append("outcome",value))
    if(params.get("requestID"))next.set("requestID",params.get("requestID")!)
    setValidation("");router.replace(`${path}?${next}`)
  }
  function page(cursor?:string) {
    // A page belongs to the exact submitted window, never the unsaved form.
    const next=new URLSearchParams(params.toString());if(cursor)next.set("cursor",cursor);else next.delete("cursor");router.replace(`${path}?${next}`)
  }
  const data=query.data
  const definitions=data?.definitions??[]
  const selectedName=workloads.data?.find(row=>row.desired.metadata.uid===selectedUID)?.desired.metadata.name??"이전 등록 서비스"
  function definitionLabel(identity:string) {
    const index=definitions.findIndex(item=>item.identity===identity)
    return index<0?"이전 적용 설정":`적용 설정 ${index+1}`
  }
  return <div className="page"><h1>{changes?"서비스 변경 이력":"요청 기록"}</h1>{changes&&<nav className="subnav" aria-label="변경 이력 종류"><Link href="/changes" aria-current="page">서비스 변경</Link><Link href={`/changes?scope=scheduler&${new URLSearchParams({...(params.get("from")?{from:params.get("from")!}:{}),...(params.get("to")?{to:params.get("to")!}:{})})}`}>운영 설정 변경</Link></nav>}<form className="toolbar" onSubmit={event=>{event.preventDefault();selectWindow()}}>
    <label>서비스<select aria-label="서비스" value={uid} onChange={event=>setUID(event.target.value)} required><option value="">서비스 선택</option>{uid&&!workloads.data?.some(row=>row.desired.metadata.uid===uid)&&<option value={uid}>이전 등록 서비스</option>}{workloads.data?.map(row=><option key={row.desired.metadata.uid} value={row.desired.metadata.uid}>{row.desired.metadata.name}</option>)}</select></label>
    <label>조회 기간<select value={preset} onChange={event=>setPreset(event.target.value)}><option value="15m">최근 15분</option><option value="1h">최근 1시간</option><option value="24h">최근 24시간</option><option value="custom">직접 선택</option></select></label>
    {preset==="custom"&&<DetailsDialog label="기간 선택" title="조회 기간"><p>현재 기기의 시간대 기준입니다. 최대 {config.maxWindowHours}시간까지 조회할 수 있습니다.</p><label>시작<input type="datetime-local" value={localDateTime(from)} onChange={event=>{if(event.target.value)setFrom(new Date(event.target.value).toISOString())}}/></label><label>종료<input type="datetime-local" value={localDateTime(to)} onChange={event=>{if(event.target.value)setTo(new Date(event.target.value).toISOString())}}/></label></DetailsDialog>}
    {!changes&&<DetailsDialog label="결과 필터" title="조회 조건"><fieldset><legend>처리 결과</legend>{outcomes.map(value=><label key={value}><input type="checkbox" checked={outcome.includes(value)} onChange={event=>setOutcome(current=>event.target.checked?[...current,value]:current.filter(item=>item!==value))}/>{outcomeLabels[value]}</label>)}{outcome.filter(value=>!outcomes.includes(value)).map(value=><label key={value}><input type="checkbox" checked onChange={()=>setOutcome(current=>current.filter(item=>item!==value))}/>이전 상세 조건</label>)}</fieldset><fieldset><legend>적용 설정</legend>{definitions.length?definitions.map((item,index)=><label key={item.identity}><input type="checkbox" checked={definition.includes(item.identity)} onChange={event=>setDefinition(current=>event.target.checked?[...current,item.identity]:current.filter(value=>value!==item.identity))}/>적용 설정 {index+1}<SourceJSON title="설정 자세히" value={item}/></label>):<p>서비스의 결과를 먼저 조회하면 실제 적용 설정을 선택할 수 있습니다.</p>}{definition.filter(value=>!definitions.some(item=>item.identity===value)).map(value=><label key={value}><input type="checkbox" checked onChange={()=>setDefinition(current=>current.filter(item=>item!==value))}/>이전 적용 설정</label>)}</fieldset></DetailsDialog>}
    <DetailsDialog label="조회 설정" title="조회 설정"><label>한 번에 볼 기록 수<input type="number" min="1" max={config.maxPageSize} value={limit} onChange={event=>setLimit(Number(event.target.value))}/></label><label><input type="checkbox" checked={poll} onChange={event=>setPoll(event.target.checked)}/> 자동 새로고침</label></DetailsDialog><Button type="submit">조회</Button></form>
    {(workloads.isPending || workloads.error || workloads.data?.length === 0) && <ReadRegion title="서비스 선택" query={workloads}>{workloads.data&&!workloads.data.length&&<p>선택할 서비스가 없습니다.</p>}</ReadRegion>}{validation&&<p role="alert">{validation}</p>}
    {!selectedWindow&&<p>서비스와 기간을 선택한 뒤 조회하세요.</p>}
    {selectedWindow&&<ReadRegion title={changes?`${selectedName} 변경 이력`:`${selectedName} 요청 기록`} query={query}>{data&&<>
      <p>{new Date(data.from).toLocaleString()} – {new Date(data.to).toLocaleString()} · {Intl.DateTimeFormat().resolvedOptions().timeZone}</p>
      {changes?<><DataTable<ManagementEvent> data={data.management??[]} columns={[
        {key:"at",label:"시각",value:row=>new Date(row.at).toLocaleString(),sortValue:row=>row.at},
        {key:"kind",label:"변경",value:row=>statusLabel(row.kind)},
        {key:"phase",label:"상태",value:row=>statusLabel(row.phase)},
        {key:"actor",label:"담당자",value:row=>row.actorUsername?.startsWith("system:serviceaccount:")?row.actorUsername.split(":").at(-1):row.actorUsername??"기록 없음"},
        {key:"detail",label:"상세",value:row=><DetailsDialog label="변경 자세히" title="변경 전후와 결과"><SourceJSON inline value={row}/></DetailsDialog>},
      ]}/><div className="toolbar"><Button variant="outline" disabled={!params.has("cursor")} onClick={()=>page()}>처음</Button><Button variant="outline" disabled={!data.nextCursor} onClick={()=>page(data.nextCursor)}>다음</Button></div></>:<>
        <h2 className="text-xl font-semibold">전체 조회 기간 결과</h2><p>전체 {data.total}건 · 선택 조건 {data.selected}건</p><div className="toolbar">{Object.entries(data.outcomes).map(([key,value])=><span key={key}>{outcomeLabels[key]??"기타 결과"} {value}건</span>)}</div>
        <DataTable<ObservationCohort> data={data.cohorts??[]} columns={[
          {key:"definition",label:"적용 설정",value:row=><DetailsDialog label={definitionLabel(row.definition)} title="적용 설정"><details><summary>원본</summary><pre>{stringifySchedulerJSON(safeSource(definitions.find(item=>item.identity===row.definition)??row),2)}</pre></details></DetailsDialog>},
          {key:"sampleCount",label:"지연 표본",value:row=>`${row.sampleCount}건`,sortValue:row=>row.sampleCount},
          {key:"latency",label:"지연 시간",value:row=>row.quantileNanos==null?"값 없음":`${row.percentile===undefined?"백분위 미제공":`${row.percentile}% 기준`} / ${Number(row.quantileNanos)/1e6} ms`},
          {key:"target",label:"목표 시간",value:row=>row.targetLatencyNanos==null?"값 없음":`${nanosToMilliseconds(row.targetLatencyNanos)} ms`},
        ]}/><ChartAreaInteractive data={(data.cohorts??[]).map(row=>({definition:row.definition,quantileNanos:row.quantileNanos===null?null:Number(row.quantileNanos),label:definitionLabel(row.definition)}))}/>
        <h2 className="text-xl font-semibold">개별 요청 기록</h2><p>전체 기간 통계와 별개로, 현재 가져온 기록을 표시합니다.</p><DataTable<RequestEvent> data={data.requests??[]} columns={[
          {key:"at",label:"시각",value:row=>new Date(row.at).toLocaleString(),sortValue:row=>row.at},
          {key:"outcome",label:"결과",value:row=>row.outcome?outcomeLabels[row.outcome]??row.outcome:"이 단계에서 결과 없음"},
          {key:"elapsed",label:"단계 소요 시간",value:row=>`${nanosToMilliseconds(row.elapsedNanos)} ms`},
          {key:"detail",label:"상세",value:row=><><span>{params.get("requestID")===row.requestID?"선택한 요청":""}</span><DetailsDialog label="요청 자세히" title="요청 배정과 실행 관측">
            <dl className="grid gap-2"><dt>요청</dt><dd>{row.requestID}</dd><dt>서비스</dt><dd>{row.workloadName} / {row.workloadUID??"UID 미제공"}</dd><dt>적용 설정</dt><dd>{row.definition??"미제공"}</dd><dt>사건 단계와 시각</dt><dd>{statusLabel(row.kind)} / {row.at}</dd><dt>수신 시각</dt><dd>{row.receivedAt}</dd><dt>입력 경로</dt><dd>{row.inputPath??"미제공"}</dd><dt>요청 유효 시간</dt><dd>{row.lifetimeNanos===undefined?"미제공":`${nanosToMilliseconds(row.lifetimeNanos)} ms`}</dd><dt>클라이언트 관측 시간</dt><dd>{row.clientSpanNanos===undefined?"미제공":`${nanosToMilliseconds(row.clientSpanNanos)} ms`}</dd><dt>실행 완료 관측</dt><dd>{row.executionComplete?"완료 확인":"완료 확인 대기"}</dd><dt>응답 목표</dt><dd>{row.slo?`${row.slo.percentile}% 기준 / ${nanosToMilliseconds(row.slo.targetLatencyNanos)} ms`:"미제공"}</dd></dl>
            <h3 className="font-semibold">CHILL 배정</h3>{row.assignment?<dl className="grid gap-2"><dt>실행 그룹과 노드</dt><dd>{row.assignment.pool} / {row.assignment.node.name} / {row.assignment.node.uid}</dd><dt>인스턴스와 배정 시각</dt><dd>{row.assignment.instance} / {row.assignment.dispatchedAt}</dd><dt>실행 경로와 모델 자료</dt><dd>{row.assignment.executionPath} / {row.assignment.artifact}</dd><dt>프로파일과 신뢰 수준</dt><dd>{row.assignment.profile} / {row.assignment.profileConfidenceLevel}</dd></dl>:<p>이 사건에 배정 관측이 없습니다.</p>}
            <h3 className="font-semibold">Native 실행 진행</h3><p>실행 인스턴스: {row.nativeInstance??"미제공"}</p>{row.nativeProgress?<dl className="grid gap-2"><dt>단계와 시각</dt><dd>{statusLabel(row.nativeProgress.phase)} / {row.nativeProgress.at}</dd><dt>실행 대상</dt><dd>{row.nativeProgress.requestID} / {row.nativeProgress.workloadUID} / {row.nativeProgress.definition}</dd><dt>배치 ID와 크기</dt><dd>{row.nativeProgress.batchID===undefined?"미제공":String(row.nativeProgress.batchID)} / {row.nativeProgress.batchSize??"미제공"}</dd><dt>프로파일과 판단 ID</dt><dd>{row.nativeProgress.profileID??"미제공"} / {row.nativeProgress.decisionID??"미제공"}</dd><dt>큐 대기 / 서비스 실행 / Backend / Native 경과 시간</dt><dd>{row.nativeProgress.queueMS} / {row.nativeProgress.serviceMS} / {row.nativeProgress.backendMS} / {row.nativeProgress.nativeElapsedMS} ms</dd><dt>실행 완료 관측</dt><dd>{row.nativeProgress.executionComplete?"완료 확인":"완료 확인 대기"}</dd><dt>처리 결과</dt><dd>{row.nativeProgress.outcome?outcomeLabels[row.nativeProgress.outcome]??row.nativeProgress.outcome:"미제공"}</dd>{row.nativeProgress.error&&<><dt>실행 오류</dt><dd>{row.nativeProgress.error}</dd></>}</dl>:<p>이 사건에 Native 실행 관측이 없습니다.</p>}
            {row.error&&<p role="alert">{row.error}</p>}<details><summary>원본</summary><pre>{stringifySchedulerJSON(safeSource(row),2)}</pre></details>
          </DetailsDialog></>},
        ]}/><div className="toolbar"><Button variant="outline" disabled={!params.has("cursor")} onClick={()=>page()}>처음</Button><Button variant="outline" disabled={!data.nextCursor} onClick={()=>page(data.nextCursor)}>다음</Button></div>
      </>}
      {!!data.incompleteWindows?.length&&<p role="alert">일부 기간의 기록이 불완전합니다. 자세한 내용은 조회 자료에서 확인하세요.</p>}
      <SourceJSON title="조회 자료 자세히" value={{conditions:{uid:selectedUID,from:params.get("from"),to:params.get("to"),definition:params.getAll("definition"),outcome:params.getAll("outcome"),cursor:params.get("cursor"),limit:params.get("limit")},source:data}}/>
      <SourceExport value={{conditions:{uid:selectedUID,query:Object.fromEntries(params.entries()),definition:params.getAll("definition"),outcome:params.getAll("outcome")},source:data}} filename={`chill-${changes?"changes":"observations"}-${data.uid}.json`}/>
    </>}</ReadRegion>}
    <DetailsDialog label="이전 등록 서비스 찾기" title="이전 등록 자료 조회"><p>삭제된 서비스의 원본 참조를 알고 있을 때 사용하세요. 이름이 같아도 새 등록 서비스와 기록은 다릅니다.</p><label>이전 등록 UID<input value={uid} onChange={event=>setUID(event.target.value)}/></label><p>대상을 입력한 뒤 기간을 선택하고 조회하세요.</p></DetailsDialog>
    {selectedWindow&&<Link href={`${changes?"/observations":"/changes"}?${params}`}>{changes?"같은 기간의 요청 기록":"같은 기간의 변경 이력"}</Link>}
  </div>
}
