import { operationDuration, operationWallEnergy, type OperationStep } from "./measurements.ts"

export function operationCostDuration(value?:unknown) {
  if((typeof value!=="number"&&typeof value!=="bigint")||(typeof value==="number"&&!Number.isSafeInteger(value))||value<0)return operationDuration(value,"s")
  const seconds=Number(value)/1e9
  return Number.isFinite(seconds)?`${seconds.toLocaleString("ko-KR",{maximumFractionDigits:1})} s`:"품질 경고: 실측 시간이 올바르지 않아요"
}
export function operationCostEnergy(value?:unknown) {
  return typeof value==="number"&&Number.isFinite(value)&&value>=0?`${value.toLocaleString("ko-KR",{maximumFractionDigits:1})} J`:operationWallEnergy(value)
}
export const operationStageLabel=(step:OperationStep)=>(stageLabels[step.condition?.kind??""]??"동작 종류 미기록")
const stageLabels:Record<string,string>={power:"전력 모드 변경",prepare:"모델 준비",idle:"쉬는 상태 유지",remove:"모델 제거"}

export function largestOperationStages(steps:OperationStep[]) {
  const duration:OperationStep[]=[],energy:OperationStep[]=[]
  let longest=-1n,highest=-1
  for(const step of steps) {
    if(step.results.length!==1)continue
    const result=step.results[0],time=result.durationNanos,joules=result.wallJoules
    if((typeof time==="bigint"||typeof time==="number"&&Number.isSafeInteger(time))&&time>=0) {
      const value=BigInt(time)
      if(value>longest){longest=value;duration.length=0}
      if(value===longest)duration.push(step)
    }
    if(result.verdict==="Accepted"&&typeof joules==="number"&&Number.isFinite(joules)&&joules>=0) {
      if(joules>highest){highest=joules;energy.length=0}
      if(joules===highest)energy.push(step)
    }
  }
  return {duration,energy}
}
