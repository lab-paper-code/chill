import type { SchedulerDecision, SchedulerWorkloadState } from "@/lib/read-types"
import { liveReason } from "@/lib/live-display"

const states:Record<string,string>={Controlled:"자율 제어",Fixed:"고정",Disabled:"꺼짐",MeasurementEvidenceMissing:"측정 근거 부족"}
function Reason({reason,compact=false}:{reason?:string;compact?:boolean}){
  if(!reason)return <>미기록</>
  const separator=reason.indexOf(":"),code=separator<0?reason:reason.slice(0,separator),target=separator<0?"":reason.slice(separator+1)
  const label=({UnconfirmedInstance:"인스턴스 미확인",ReservedScope:"예약 범위"} as Record<string,string>)[code]??liveReason(code)
  return <>{label}{!compact&&<> <span className="text-muted-foreground">({code})</span>{target&&<details><summary className="cursor-pointer py-2">대상 보기</summary><span className="break-all">{target}</span></details>}</>}</>
}
export function SchedulerWorkloadStatus({workload,compact=false}:{workload?:SchedulerWorkloadState;compact?:boolean}){
  if(!workload)return <>자율 상태 미기록</>
  return <span className={compact?"live-autonomy":undefined} data-state={workload.state}><strong>{states[workload.state]??"상태 미확인"}{!compact&&` (${workload.state})`}</strong>{compact&&workload.reason&&<small><Reason reason={workload.reason} compact/></small>}</span>
}
export function SchedulerDecisionReasons({reasons}:{reasons?:string[]|null}){
  return reasons?.length?<ul>{reasons.map(reason=><li key={reason}><Reason reason={reason}/></li>)}</ul>:<>미기록</>
}
export function SchedulerDecisionSummary({at,status,reasons}:{at:string;status:string;reasons?:string[]|null}){
  return <div className="overflow-x-auto"><table className="w-full text-sm"><caption className="pb-2 text-left font-semibold">결정 전체</caption><thead><tr><th className="p-2 text-left">시각 KST</th><th className="p-2 text-left">상태</th><th className="p-2 text-left">결정 이유·대상</th></tr></thead><tbody><tr className="border-t"><td className="p-2 align-top">{at}</td><td className="p-2 align-top">{status}</td><td className="p-2 align-top"><SchedulerDecisionReasons reasons={reasons}/></td></tr></tbody></table></div>
}
export function SchedulerWorkloadStates({workloads,names}:{workloads?:SchedulerWorkloadState[]|null;names:ReadonlyMap<string,string>}){
  if(!workloads?.length)return <p>서비스별 자율 상태 미기록</p>
  return <div className="overflow-x-auto"><table className="w-full text-sm"><caption className="pb-2 text-left font-semibold" title="결정에 기록된 서비스별 제어 범위. 자동 운영 설정 ON·전체 Keep 여부와 별개이며, 이유는 각 서비스 기록만 표시합니다.">서비스별 자율 상태</caption><thead><tr>{["서비스","상태","이유·대상"].map(label=><th key={label} className="p-2 text-left">{label}</th>)}</tr></thead><tbody>{workloads.map(row=><tr key={row.uid} className="border-t"><th scope="row" className="p-2 text-left align-top">{names.get(row.uid)??"이름 미기록"}<details className="font-normal"><summary className="cursor-pointer py-2">서비스 식별자</summary><span className="break-all">{row.uid}</span></details></th><td className="p-2 align-top"><SchedulerWorkloadStatus workload={row}/></td><td className="p-2 align-top"><Reason reason={row.reason}/></td></tr>)}</tbody></table></div>
}

export function SchedulerPathFeedback({decision,names}:{decision:Pick<SchedulerDecision,"pathFeedback">;names:ReadonlyMap<string,string>}){
  if(!decision.pathFeedback?.length)return null
  const means=decision.pathFeedback.filter(row=>row.observedMean!==undefined)
  return <section className="space-y-2">
    <div className="overflow-x-auto"><table className="w-full text-sm"><caption className="pb-2 text-left font-semibold">결정의 서비스별 경로 예산</caption><thead><tr>{["서비스","분위수 (%)","계획 reserve TOTAL (ms)","반환 reserve POST (ms)","출처","결합 표본 (개)"].map((label,index)=><th key={label} scope="col" className={index===0||index===4?"p-2 text-left":"p-2 text-right"}>{label}</th>)}</tr></thead>
      <tbody>{decision.pathFeedback.map(row=><tr key={row.workloadUID} className="border-t">
        <th scope="row" className="p-2 text-left align-top">{names.get(row.workloadUID)??row.workloadUID}<small className="block break-all font-normal">{names.has(row.workloadUID)?row.workloadUID:null}</small></th>
        {[row.percentile,row.totalNanos==null?null:Number(row.totalNanos)/1e6,row.postNanos==null?null:Number(row.postNanos)/1e6,row.source,row.sampleCount].map((value,index)=><td key={index} className={index===3?"p-2 align-top":"p-2 text-right align-top tabular-nums"}>{value??"미기록"}</td>)}
      </tr>)}</tbody></table></div>
    <p className="text-sm text-muted-foreground">선택 당시 결정이 사용한 회계 예산입니다. TOTAL은 계획용, POST는 sender 경과 반영 후 반환용 reserve입니다. POST는 외부 클라이언트 양방향 잔여를 포함하며 실측 편도 지연이 아닙니다. configured는 설정값, empirical은 관측 분위수입니다. 결합 표본 수는 유한 reserve 표본 수이며 전체 클라이언트 성공·실패 수와 다릅니다.</p>
    {!!means.length&&<>
      <div className="overflow-x-auto"><table className="w-full text-sm"><caption className="pb-2 text-left font-semibold">결합 표본 평균</caption><thead><tr>{["서비스","클라이언트 E (ms)","서버 대기 Q (ms)","native 전체 N (ms)","비연산 TOTAL (ms)","결합 표본 (개)"].map((label,index)=><th key={label} scope="col" className={index===0?"p-2 text-left":"p-2 text-right"}>{label}</th>)}</tr></thead>
        <tbody>{means.map(row=>{
          const mean=row.observedMean!
          return <tr key={row.workloadUID} className="border-t"><th scope="row" className="break-all p-2 text-left align-top">{names.get(row.workloadUID)??row.workloadUID}<small className="block font-normal">{names.has(row.workloadUID)?row.workloadUID:null}</small></th>{[mean.clientNanos/1e6,mean.servingQueueNanos/1e6,mean.nativeNanos/1e6,mean.nonComputeNanos/1e6,row.sampleCount].map((value,index)=><td key={index} className="p-2 text-right align-top tabular-nums">{value}</td>)}</tr>
        })}</tbody></table></div>
      <p className="text-sm text-muted-foreground">동일한 결합 표본의 산술평균이며 평균 E = 평균 Q + 평균 N + 평균 TOTAL입니다. native N은 내부 대기와 처리를 포함합니다. 전체 클라이언트 성공·실패 모집단을 나타내지 않습니다.</p>
    </>}
  </section>
}
