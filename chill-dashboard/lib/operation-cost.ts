import type { OperationStep } from "./measurements.ts"

export const operationStageLabel=(step:OperationStep)=>(operationStageLabels[step.condition?.kind??""]??"동작 종류 미기록")
export const operationStageLabels:Record<string,string>={power:"전력 모드 변경",prepare:"모델 준비",idle:"쉬는 상태 유지",remove:"모델 제거"}

export function operationDirection(step:OperationStep):string {
  const directions=[...new Set(step.results.filter(result=>result.fromPowerMode||result.toPowerMode).map(result=>`${result.fromPowerMode||"출발 모드 미기록"} → ${result.toPowerMode||"도착 모드 미기록"}`))]
  return directions.join(" / ")||(step.condition?.kind==="power"?`출발 모드 미기록 → 계획 목표 ${step.condition.powerMode||"미기록"}`:"전환 방향 미기록")
}
export function operationModel(step:OperationStep):string {
  return [...new Set(step.results.map(result=>result.model).filter(Boolean))].join(" / ")||"모델 미기록"
}
export type OperationMetadata={directions:string[];models:string[]}
export function operationMetadata(steps:OperationStep[]):OperationMetadata {
  const directions=steps.filter(step=>step.condition?.kind==="power"||step.results.some(result=>result.fromPowerMode&&result.toPowerMode&&result.fromPowerMode!==result.toPowerMode)).map(operationDirection)
  const models=steps.filter(step=>step.condition?.kind==="prepare"||step.condition?.kind==="remove"||step.results.some(result=>result.model)).map(step=>`${operationStageLabel(step)}: ${operationModel(step)}${step.condition?.batchEnergyOptimization?` (프로파일 참조 ${step.condition.batchEnergyOptimization})`:""}`)
  return {directions:directions.length?directions:["전환 동작 미기록"],models:models.length?models:["모델 대상 미기록"]}
}

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
