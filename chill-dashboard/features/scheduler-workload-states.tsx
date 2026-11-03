import type { SchedulerWorkloadState } from "@/lib/read-types"
import { liveReason } from "@/lib/live-display"

const states:Record<string,string>={Controlled:"자율 제어",Fixed:"고정",Disabled:"꺼짐",MeasurementEvidenceMissing:"측정 근거 부족"}
function Reason({reason}:{reason?:string}){
  if(!reason)return <>미기록</>
  const separator=reason.indexOf(":"),code=separator<0?reason:reason.slice(0,separator),target=separator<0?"":reason.slice(separator+1)
  const label=({UnconfirmedInstance:"인스턴스 미확인",ReservedScope:"예약 범위"} as Record<string,string>)[code]??liveReason(code)
  return <>{label} <span className="text-muted-foreground">({code})</span>{target&&<details><summary className="cursor-pointer py-2">대상 보기</summary><span className="break-all">{target}</span></details>}</>
}
export function SchedulerDecisionReasons({reasons}:{reasons?:string[]|null}){
  return reasons?.length?<ul>{reasons.map(reason=><li key={reason}><Reason reason={reason}/></li>)}</ul>:<>미기록</>
}
export function SchedulerDecisionSummary({at,status,reasons}:{at:string;status:string;reasons?:string[]|null}){
  return <div className="overflow-x-auto"><table className="w-full text-sm"><caption className="pb-2 text-left font-semibold">결정 전체</caption><thead><tr><th className="p-2 text-left">시각 KST</th><th className="p-2 text-left">상태</th><th className="p-2 text-left">결정 이유·대상</th></tr></thead><tbody><tr className="border-t"><td className="p-2 align-top">{at}</td><td className="p-2 align-top">{status}</td><td className="p-2 align-top"><SchedulerDecisionReasons reasons={reasons}/></td></tr></tbody></table></div>
}
export function SchedulerWorkloadStates({workloads,names}:{workloads?:SchedulerWorkloadState[]|null;names:ReadonlyMap<string,string>}){
  if(!workloads?.length)return <p>서비스별 자율 상태 미기록</p>
  return <div className="overflow-x-auto"><table className="w-full text-sm"><caption className="pb-2 text-left font-semibold" title="결정에 기록된 서비스별 제어 범위. 자동 운영 설정 ON·전체 Keep 여부와 별개이며, 이유는 각 서비스 기록만 표시합니다.">서비스별 자율 상태</caption><thead><tr>{["서비스","상태","이유·대상"].map(label=><th key={label} className="p-2 text-left">{label}</th>)}</tr></thead><tbody>{workloads.map(row=><tr key={row.uid} className="border-t"><th scope="row" className="p-2 text-left align-top">{names.get(row.uid)??"이름 미기록"}<details className="font-normal"><summary className="cursor-pointer py-2">서비스 식별자</summary><span className="break-all">{row.uid}</span></details></th><td className="p-2 align-top">{states[row.state]??"상태 미확인"} ({row.state})</td><td className="p-2 align-top"><Reason reason={row.reason}/></td></tr>)}</tbody></table></div>
}
