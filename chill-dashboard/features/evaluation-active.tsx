"use client"

import type { UseQueryResult } from "@tanstack/react-query"
import type { ApiError } from "@/lib/client"
import { useChillQuery } from "@/lib/client"
import { evaluationRemaining, type ActiveEvaluations, type EvaluationPhaseSummary } from "@/lib/evaluation-active"
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

export function EvaluationActive({now,name,uid}:{now:number;name?:string;uid?:string}) {
  const active=useChillQuery<ActiveEvaluations>("v1/evaluations/active",{poll:true})
  return <EvaluationActiveDetails active={active} now={now} name={name} uid={uid}/>
}

export function EvaluationActiveDetails({active,now,name,uid}:{active:Pick<UseQueryResult<ActiveEvaluations,ApiError>,"data"|"error"|"isPending"|"refetch">;now:number;name?:string;uid?:string}) {
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
      return <details key={run.uid} className="live-evaluation" open>
        <summary>{afterLoad?"부하 종료 · 후처리 중":"부하 진행"}</summary>
        <div className="live-evaluation-details space-y-3">
          <dl className="flex flex-wrap gap-x-6 gap-y-2 text-sm tabular-nums">
            <div><dt className="text-muted-foreground">현재 단계</dt><dd>{run.stage?.id??"미기록"} <span className="text-muted-foreground">({run.stageIndex}/{run.stagesTotal})</span></dd></div>
            <div><dt className="text-muted-foreground">GO 이후 경과 (초)</dt><dd>{elapsed??"미기록"}</dd></div>
            <div><dt className="text-muted-foreground">계획상 남은 시간 (초)</dt><dd>{remaining??"미기록"}</dd></div>
          </dl>
          {!afterLoad&&elapsed!==null&&typeof planned==="number"&&Number.isFinite(planned)&&planned>0&&<progress className="h-2 w-full accent-primary" aria-label={`${run.stage?.id} 부하 진행`} max={planned} value={Math.min(planned,elapsed)}/>}
          {!!run.stage?.injections.length&&<details><summary className="min-h-11 cursor-pointer py-3 text-sm">주입 계획·결과</summary><div className="live-table-wrap"><table><thead><tr><th>주입</th><th>동작</th><th>시작 후 초</th><th>노드</th><th>파드</th><th>결과</th></tr></thead><tbody>{run.stage.injections.map(injection=><tr key={injection.id}><th scope="row">{injection.id}</th><td title={injection.action}>{actions[injection.action]??"미기록"}</td><td>{injection.offsetSeconds}</td><td>{injection.nodeName??"미기록"}</td><td>{injection.podName??"미기록"}</td><td title={injection.outcome}>{injection.outcome?outcomes[injection.outcome]??"미기록":"대기"}</td></tr>)}</tbody></table></div></details>}
        </div>
      </details>
    })}
  </div>
}
