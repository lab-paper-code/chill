import { schedulerLabel } from "./scheduler-selection.ts"
import type { FeedbackCapacity, SchedulerFeedbackRate, SchedulerPlanSearch } from "./read-types.ts"

// GET /scheduler/capacity contains only the current decision's tested rates.
export type CapacityView = {tests:SchedulerFeedbackRate[]|null}
export const capacityPath=(workload="")=>{
  const query=new URLSearchParams({evidence:"capacity"})
  if(workload)query.set("workload",workload)
  return `/?${query}`
}
export const capacityTests=(tests:SchedulerFeedbackRate[],workload:string)=>workload?tests.filter(test=>test.input.workloadUID===workload):tests
export const capacityWitness=(capacity?:FeedbackCapacity)=>{
  if(!capacity)return "미기록"
  if(!capacity.tests?.length)return "미검사 · 통과 근거 없음"
  return capacity.tests.some(test=>test.met)?"검사한 요청률에서 통과 근거 있음":"검사한 요청률 모두 미충족 · 통과 근거 없음"
}
export const capacityKappaHat=(capacity?:FeedbackCapacity)=>capacity?`${capacity.kappaHat} req/s (${capacityWitness(capacity)})`:"미기록"

const searchTerminationReasons:Record<string,string>={noImprovement:"개선 없음",evaluationBudget:"평가 수 예산",deadline:"시간 마감",budgetCut:"시간 마감"}

export function planSearchFacts(search:SchedulerPlanSearch):[string,string][] {
  const rows:[string,string][]=[["탐색 이웃",String(search.neighborhood)],["평가 수",String(search.evaluated)],["탐색 시간",`${search.durationNanos} ns`]]
  const fields=[
    ["strategy","탐색 전략"],["surrogateEvaluated","증분 평가 수"],["finalists","최종 후보 수"],
    ["exactEvaluated","정확 평가 수"],["fallback","대체 탐색 사유"],["start","출발점"],
    ["surrogateGapJoules","증분·정확 에너지 차이 (J)"],["postFilterReverts","후처리 복귀 수"],
  ] as const
  for(const [key,label] of fields)if(search[key]!==undefined)rows.push([label,key==="strategy"?schedulerLabel(search.strategy):String(search[key])])
  for(const [reason,count] of Object.entries(search.pruned??{}))rows.push([`가지치기 · ${reason}`,String(count)])
  if(search.terminationReason!==undefined)rows.push(["탐색 종료 사유",searchTerminationReasons[search.terminationReason]??search.terminationReason])
  if(search.budgetCut!==undefined)rows.push(["시간 마감 종료",search.budgetCut?"예":"아니요"])
  return rows
}
