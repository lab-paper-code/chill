import { operationDuration, operationWallEnergy, type OperationStep } from "./measurements.ts"

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

export type OperationCostRow={step:OperationStep;result:OperationStep["results"][number]|undefined}
// Keep individual measured attempts: scenario totals are not a one-transition cost.
export function operationTransitionCosts(steps:OperationStep[]):OperationCostRow[] {
  return steps.flatMap(step=>{
    if(step.condition?.kind==="idle")return []
    if(!["power","prepare","remove"].includes(step.condition?.kind??"")&&!step.results.some(result=>result.fromPowerMode&&result.toPowerMode&&result.fromPowerMode!==result.toPowerMode))return []
    return (step.results.length?step.results:[undefined]).map(result=>({step,result}))
  })
}

export function operationCostTime(value:unknown) {
  const text=operationDuration(value,"s",2)
  return text.endsWith(" s")?(text.includes(".")?text.slice(0,-2):`${text.slice(0,-2)}.0`):text
}
export const operationCostEnergy=(value:unknown)=>operationWallEnergy(value,0).replace(/ J$/,"")

// Compare attempts of the same operation within one device's measurement, not scenario totals.
// Three samples and a positive median are required; the marker is descriptive, not a verdict.
export function operationCostOutliers(rows:OperationCostRow[]) {
  const values=rows.map(({result})=>{
    const time=result?.durationNanos,energy=result?.wallJoules
    return {
      duration:(typeof time==="bigint"||typeof time==="number"&&Number.isSafeInteger(time))&&time>=0&&Number.isFinite(Number(time))?Number(time):undefined,
      energy:result?.verdict==="Accepted"&&typeof energy==="number"&&Number.isFinite(energy)&&energy>=0?energy:undefined,
    }
  })
  const groups=rows.map(({step,result})=>{
    const kind=step.condition?.kind
    if(kind==="power"||!kind&&result?.fromPowerMode&&result.toPowerMode)return result?.fromPowerMode&&result.toPowerMode?JSON.stringify(["power",result.fromPowerMode,result.toPowerMode]):undefined
    if(kind==="prepare"||kind==="remove")return result?.model||step.condition?.batchEnergyOptimization?JSON.stringify([kind,result?.model??"",step.condition?.batchEnergyOptimization??""]):undefined
    return undefined
  })
  const medians=new Map<string,{duration?:number;energy?:number}>()
  for(const group of groups) {
    if(!group||medians.has(group))continue
    const median:{duration?:number;energy?:number}={}
    for(const metric of ["duration","energy"] as const) {
      const samples=values.flatMap((value,index)=>groups[index]===group&&value[metric]!==undefined?[value[metric]!]:[]).sort((a,b)=>a-b)
      if(samples.length>=3) {
        const middle=Math.floor(samples.length/2)
        median[metric]=samples.length%2?samples[middle]:samples[middle-1]/2+samples[middle]/2
      }
    }
    medians.set(group,median)
  }
  return rows.map((_,index)=>{
    const outlier:{duration?:number;energy?:number}={},median=medians.get(groups[index]??"")
    for(const metric of ["duration","energy"] as const) {
      const baseline=median?.[metric],value=values[index][metric]
      if(baseline!==undefined&&baseline>0&&value!==undefined&&value/baseline>=3)outlier[metric]=value/baseline
    }
    return outlier
  })
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
