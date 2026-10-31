"use client"

import * as React from "react"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { evaluationPoolBlocked, evaluationRemaining, evaluationStopPath, type ActiveEvaluation, type ActiveEvaluations, type EvaluationPhaseSummary } from "@/lib/evaluation-active"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { StatusCell } from "@/components/status-cell"
import { STATUS_AXES } from "@/lib/status"

export function EvaluationStatusCell() {
  const query=useChillQuery<{items:EvaluationPhaseSummary[]}>("v1/evaluations",{poll:true})
  const phases=query.data?.items.map(run=>run.phase)??[]
  const label=query.error?(query.error.status===403?"권한 없음":"조회 실패"):query.isPending?"조회 중":phases.includes("Blocked")?"정리 실패":phases.includes("Finishing")?"정리 중":phases.includes("Running")?"실행 중":phases.includes("Pending")?"시작 전":"활성 없음"
  return <StatusCell axis={STATUS_AXES.experiment} label={label} tone={label==="정리 실패"?"fail":query.error?"warn":label==="활성 없음"?"idle":"info"}/>
}

const modes:Record<string,string>={autonomous:"자율",fixed:"고정","controls-disabled":"제어요소 끔"}
const actions:Record<string,string>={"delete-serving-pod":"서비스 Pod 삭제","cordon-delete":"노드 차단·Pod 삭제",uncordon:"노드 차단 해제"}
const outcomes:Record<string,string>={Executed:"실행됨",Cancelled:"취소됨",Invalid:"무효"}

export function EvaluationActive({now,name,uid}:{now:number;name?:string;uid?:string}) {
  const active=useChillQuery<ActiveEvaluations>("v1/evaluations/active",{poll:true})
  const evaluations=useChillQuery<{items:EvaluationPhaseSummary[]}>("v1/evaluations",{poll:true})
  const stop=useChillMutation()
  const [selected,setSelected]=React.useState<ActiveEvaluation|null>(null)
  const [uncertain,setUncertain]=React.useState(false)
  const [submitted,setSubmitted]=React.useState<string[]>([])
  const [unknownUIDs,setUnknownUIDs]=React.useState<string[]>([])
  const current=useChillQuery<{name:string;uid:string;phase:string}>(`v1/evaluations/${encodeURIComponent(selected?.name??"")}`,{enabled:!!selected&&uncertain})
  const submit=()=>{
    if(!selected||uid&&selected.uid!==uid||name&&selected.name!==name)return
    stop.mutate({path:evaluationStopPath(selected),method:"DELETE"},{
      onSuccess:()=>{setSubmitted(previous=>[...previous,selected.uid]);setSelected(null);void active.refetch()},
      onError:error=>{if(error.unknownOutcome||error.status===0){setUnknownUIDs(previous=>[...previous,selected.uid]);setUncertain(true);void active.refetch()}},
    })
  }
  return <div className="live-evaluations" aria-label="진행 중 실험">
    {active.isPending&&<span role="status">실험 조회 중</span>}
    {active.error&&<span role="status">실험 조회 실패 <button type="button" onClick={()=>void active.refetch()}>다시 조회</button></span>}
    {evaluations.error&&<span role="status">실험 상태 관측 실패 <button type="button" onClick={()=>void evaluations.refetch()}>다시 조회</button></span>}
    {!active.error&&active.data?.items.filter(run=>(!name||run.name===name)&&(!uid||run.uid===uid)).map(run=>{const remaining=now?evaluationRemaining(run.stage,now):null,phase=evaluations.data?.items.find(item=>item.uid===run.uid)?.phase;return <details key={run.uid} className="live-evaluation">
      <summary><strong>{phase==="Blocked"?"정리 실패":phase==="Finishing"?"정리 중":phase==="Pending"?"시작 전":phase==="Running"?"실험 실행 중":"미확인"}</strong> {run.name} / 단계 {run.stageIndex}/{run.stagesTotal} / {run.stage?modes[run.stage.mode]??"미기록":"시작 전"} / {remaining===null?"시작 전":`남은 ${remaining}초`} {!evaluations.error&&evaluationPoolBlocked(run.uid,evaluations.data?.items??[])&&<strong className="live-injection-tag">풀 묶임</strong>}</summary>
      <div className="live-evaluation-details"><strong>{run.stage?.id??"시작 전"}</strong>
        {!!run.stage?.injections.length&&<div className="live-table-wrap"><table><caption>주입 계획·결과</caption><thead><tr><th>주입</th><th>동작</th><th>시작 후 초</th><th>노드</th><th>파드</th><th>결과</th></tr></thead><tbody>{run.stage.injections.map(injection=><tr key={injection.id}><th scope="row">{injection.id}</th><td title={injection.action}>{actions[injection.action]??"미기록"}</td><td>{injection.offsetSeconds}</td><td>{injection.nodeName??"미기록"}</td><td>{injection.podName??"미기록"}</td><td title={injection.outcome}>{injection.outcome?outcomes[injection.outcome]??"미기록":"대기"}</td></tr>)}</tbody></table></div>}
        <button type="button" disabled={submitted.includes(run.uid)} onClick={()=>{stop.reset();setUncertain(unknownUIDs.includes(run.uid));setSelected(run)}}>{submitted.includes(run.uid)?"중지 요청됨":unknownUIDs.includes(run.uid)?"중지 결과 확인":"실험 중지"}</button>
      </div>
    </details>})}
    <Dialog open={!!selected} onOpenChange={open=>{if(!open&&!stop.isPending)setSelected(null)}}><DialogContent><DialogHeader><DialogTitle>실험 중지 확인</DialogTitle><DialogDescription title="중지 요청 후 끝 정리와 풀 반환이 완료될 때까지 실험이 목록에 남을 수 있습니다.">{selected?.name} / 중지 요청</DialogDescription></DialogHeader>
      {stop.error&&<span role="alert">{uncertain?"중지 결과 미확인":stop.error.status===403?"권한 없음(실험 운영자)":stop.error.status===401?"인증 필요":"중지 실패"}</span>}
      {uncertain&&<><span role="status">{current.isFetching?"상태 확인 중":current.error?.status===404?"실험 삭제 확인":current.error?"상태 조회 실패":current.data?.uid!==selected?.uid?"실험 교체 확인":current.data?.phase==="Done"?"실험 완료":"중지 결과 미확인"}</span><button type="button" onClick={()=>{void current.refetch();void active.refetch()}}>상태 다시 확인</button></>}
      <button type="button" disabled={stop.isPending} onClick={()=>setSelected(null)}>닫기</button><button type="button" disabled={stop.isPending||uncertain||!!uid&&selected?.uid!==uid||!!name&&selected?.name!==name} onClick={submit}>{stop.isPending?"중지 요청 중":"중지 요청"}</button>
    </DialogContent></Dialog>
  </div>
}
