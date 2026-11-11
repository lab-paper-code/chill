"use client"

import type { UseQueryResult } from "@tanstack/react-query"
import type { ApiError } from "@/lib/client-transport"
import { useChillQuery } from "@/lib/client"
import { evaluationLastProgressAt, evaluationProgressOngoing, evaluationRemaining, type ActiveEvaluations, type EvaluationPhaseSummary, type EvaluationStageProgress, type EvaluationProgressProcedure, type EvaluationProgressEvent, type EvaluationProgressOwner } from "@/lib/evaluation-active"
import { StatusCell } from "@/components/status-cell"
import { STATUS_AXES } from "@/lib/status"

export function EvaluationStatusCell() {
  const query=useChillQuery<{items:EvaluationPhaseSummary[]}>("v1/evaluations",{poll:true})
  const phases=query.data?.items.map(run=>run.phase)??[]
  const label=query.error?(query.error.status===403?"권한 없음":"조회 실패"):query.isPending?"조회 중":phases.includes("Blocked")?"정리 실패":phases.includes("Finishing")?"정리 중":phases.includes("Running")?"실행 중":phases.includes("Pending")?"시작 전":"활성 없음"
  return <StatusCell axis={STATUS_AXES.experiment} label={label} tone={label==="정리 실패"?"fail":query.error?"warn":label==="활성 없음"?"idle":"info"}/>
}

const actions:Record<string,string>={"delete-serving-pod":"서비스 Pod 삭제","cordon-delete":"노드 차단·Pod 삭제",uncordon:"노드 차단 해제"}
const outcomes:Record<string,string>={Executed:"실행됨",Cancelled:"취소됨",Invalid:"무효"}
const progressLabels:Record<EvaluationProgressEvent["phase"],{boundary:string;ongoing?:string}>={
  loadStarted:{boundary:"부하 시작",ongoing:"부하 진행 중"},
  loadEnded:{boundary:"부하 종료"},
  drainStarted:{boundary:"drain 시작",ongoing:"drain 진행 중"},
  drainEnded:{boundary:"drain 종료"},
  flushStarted:{boundary:"flush 시작",ongoing:"flush 진행 중"},
  collectStarted:{boundary:"수집 시작",ongoing:"수집 진행 중"},
  collectEnded:{boundary:"수집 종료"},
  judgeStarted:{boundary:"판정 시작",ongoing:"판정 진행 중"},
  judgeEnded:{boundary:"판정 종료"},
}
const progressTime=(value:string)=>new Date(value).toLocaleTimeString("ko-KR",{timeZone:"Asia/Seoul",hour12:false,hour:"2-digit",minute:"2-digit",second:"2-digit"})

export function EvaluationActiveDetails({active,now,name,uid,stageProgress,parentRun,procedures}:{active:Pick<UseQueryResult<ActiveEvaluations,ApiError>,"data"|"error"|"isPending"|"refetch">;now:number;name?:string;uid?:string;stageProgress?:EvaluationStageProgress;parentRun?:EvaluationProgressOwner;procedures?:EvaluationProgressProcedure[]|null}) {
  const runs=active.data?.items.filter(run=>(!name||run.name===name)&&(!uid||run.uid===uid))??[]
  return <div className="live-evaluations" aria-label="진행 중 실험">
    {active.isPending&&<span role="status">실험 조회 중</span>}
    {active.error&&<span role="status">실험 조회 실패 <button type="button" onClick={()=>void active.refetch()}>다시 조회</button></span>}
    {!active.error&&active.data&&name&&!runs.length&&<p className="text-sm">활성 단계 미기록</p>}
    {!active.error&&runs.map(run=>{
      const remaining=now?evaluationRemaining(run.stage,now):null
      const goAt=run.stage?.goAt?Date.parse(run.stage.goAt):NaN
      const elapsed=now&&Number.isFinite(goAt)?Math.max(0,Math.floor((now-goAt)/1000)):null
      const planned=run.stage?.plannedSeconds
      const afterLoad=elapsed!==null&&typeof planned==="number"&&Number.isFinite(planned)&&elapsed>=planned
      const stage=stageProgress?.id===run.stage?.id?stageProgress:undefined
      const progress=stage?.progress
      const lastBoundary=progress?.[progress.length-1]
      const progressLabel=lastBoundary
        ?evaluationProgressOngoing(stage,parentRun)?`현재 구간: ${progressLabels[lastBoundary.phase].ongoing}`:`마지막 관측: ${progressLabels[lastBoundary.phase].boundary} ${progressTime(lastBoundary.at)}${lastBoundary.phase.endsWith("Started")?" · 종료 미기록":""}`
        :"구간 경계 미기록"
      const lastProgressAt=evaluationLastProgressAt(stage,procedures)
      const stageProcedures=stage?.procedures
      const loadIndex=stageProcedures?.findIndex(procedure=>procedure.step==="stage")??-1
      const postLoad=[...(stageProcedures??[]).filter((procedure,index)=>index>loadIndex&&loadIndex>=0||["judge","cleanup-jobs"].includes(procedure.step)),...(procedures??[]).filter(procedure=>["finish","publish"].includes(procedure.step))].map(procedure=>({procedure,at:Math.max(...[procedure.startedAt,procedure.finishedAt].map(value=>value?Date.parse(value):NaN).filter(Number.isFinite))})).filter(record=>Number.isFinite(record.at)).sort((a,b)=>b.at-a.at)[0]?.procedure
      const procedureLabels:Record<string,string>={judge:"측정 판정","cleanup-jobs":"작업 정리",finish:"정리",publish:"게시"}
      const postLoadTimes=postLoad?[["시작",postLoad.startedAt],["종료",postLoad.finishedAt]].filter(([label,value])=>label==="시작"||value).map(([label,value])=>`${label} ${value?progressTime(value):"미기록"}`).join(" · "):""
      const postLoadLabel=postLoad?`종료 처리: ${procedureLabels[postLoad.step]??postLoad.step} ${postLoad.finishedAt?"종료 기록":"중"} (${postLoadTimes})`:stageProcedures==null?"종료 처리 단계 미기록":"종료 처리 신호 미수신"
      const silentSeconds=now&&lastProgressAt!==null?Math.max(0,Math.floor((now-lastProgressAt)/1000)):null
      return <details key={run.uid} className="live-evaluation" open>
        <summary>{progress?progressLabel:afterLoad?`계획 시간 경과 · ${postLoadLabel}`:"부하 계획 시간"}</summary>
        <div className="live-evaluation-details space-y-3">
          <dl className="flex flex-wrap gap-x-6 gap-y-2 text-sm tabular-nums">
            <div><dt className="text-muted-foreground">현재 단계</dt><dd>{run.stage?.id??"미기록"} <span className="text-muted-foreground">({run.stageIndex}/{run.stagesTotal})</span></dd></div>
            {!progress&&<><div><dt className="text-muted-foreground">GO 이후 경과 (초)</dt><dd>{elapsed??"미기록"}</dd></div>
            <div><dt className="text-muted-foreground">{afterLoad?"계획 종료 후 경과 (초)":"계획상 남은 시간 (초)"}</dt><dd>{afterLoad?Math.floor(elapsed!-planned!):remaining??"미기록"}</dd></div></>}
          </dl>
          <p className="text-sm text-muted-foreground">{silentSeconds===null?"마지막 진행 신호 미기록":`진행 신호 없음 ${silentSeconds}초`}</p>
          {progress&&<div className="overflow-x-auto"><table className="w-full text-left text-sm tabular-nums" aria-label="기록된 구간 경계"><caption className="py-2 text-left">부하 → drain → flush → 수집 → 판정 · 관측 순서</caption><thead><tr><th className="p-2">경계</th><th className="p-2">시각 (KST)</th><th className="p-2">기록된 근거</th></tr></thead><tbody>{progress.map((event,index)=><tr key={index} className="border-t"><th scope="row" className="whitespace-nowrap p-2 font-normal">{progressLabels[event.phase].boundary}</th><td className="whitespace-nowrap p-2"><time dateTime={event.at} title={event.at}>{progressTime(event.at)}</time></td><td className="p-2">{event.phase==="drainEnded"&&event.producerRowsMissing!=null?<>drain 종료 시 생성기 결과 행 부족: {Object.entries(event.producerRowsMissing).map(([workload,count])=>`${workload}: ${count.toLocaleString("ko-KR")}`).join(", ")} (진행 중 요청 수 아님)</>:event.phase==="collectEnded"&&event.bytes!=null?`저장된 원시 자료: ${event.bytes.toLocaleString("ko-KR")} bytes`:"—"}</td></tr>)}</tbody></table></div>}
          {!progress&&!afterLoad&&elapsed!==null&&typeof planned==="number"&&Number.isFinite(planned)&&planned>0&&<progress className="h-2 w-full accent-primary" aria-label={`${run.stage?.id} 계획 시간 경과`} max={planned} value={Math.min(planned,elapsed)}/>}
          {!!run.stage?.injections.length&&<details><summary className="min-h-11 cursor-pointer py-3 text-sm">주입 계획·결과</summary><div className="live-table-wrap"><table><thead><tr><th>주입</th><th>동작</th><th>시작 후 초</th><th>노드</th><th>파드</th><th>결과</th></tr></thead><tbody>{run.stage.injections.map(injection=><tr key={injection.id}><th scope="row">{injection.id}</th><td title={injection.action}>{actions[injection.action]??injection.action}</td><td>{injection.offsetSeconds}</td><td>{injection.nodeName??"미기록"}</td><td>{injection.podName??"미기록"}</td><td title={injection.outcome}>{injection.outcome?outcomes[injection.outcome]??injection.outcome:"미기록"}</td></tr>)}</tbody></table></div></details>}
        </div>
      </details>
    })}
  </div>
}
