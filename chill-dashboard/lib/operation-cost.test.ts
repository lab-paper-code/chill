import assert from "node:assert/strict"
import test from "node:test"
import { largestOperationStages, operationStageLabel, operationStageLabels, operationDirection, operationModel, operationMetadata, operationTransitionCosts, operationCostOutliers, operationCostTime, operationCostEnergy } from "./operation-cost.ts"
import { operationDuration, operationWallEnergy, type OperationStep } from "./measurements.ts"

test("operation cost display bounds precision without changing missing and invalid values",()=>{
  assert.equal(operationDuration(874863796474n,"s",1),"874.9 s")
  assert.equal(operationDuration(0,"s",1),"0 s")
  assert.equal(operationWallEnergy(6290.123456,1),"6,290.1 J")
  assert.equal(operationWallEnergy(0,1),"0 J")
  assert.match(operationDuration(-1,"s",1),/품질 경고/)
  assert.match(operationDuration(10n**400n,"s",1),/품질 경고/)
  assert.match(operationWallEnergy(Infinity,1),/품질 경고/)
  assert.match(operationWallEnergy(undefined,1),/없음/)
})
test("largest stages distinguish time and accepted energy, retain ties and exclude ambiguous records",()=>{
  const step=(name:string,durationNanos:number,wallJoules:number,verdict="Accepted"):OperationStep=>({name,condition:undefined,results:[{name,key:name,durationNanos,wallJoules,verdict}]})
  const steps=[step("prepare",30,20),step("idle",10,40),step("power",30,90,"Rejected"),step("invalid",-1,NaN)]
  const duplicate=step("duplicate",100,100);duplicate.results.push(duplicate.results[0])
  const largest=largestOperationStages([...steps,duplicate])
  assert.deepEqual(largest.duration.map(step=>step.name),["prepare","power"])
  assert.deepEqual(largest.energy.map(step=>step.name),["idle"])
  assert.deepEqual(largestOperationStages([]),{duration:[],energy:[]})
})

test("stage summaries and planned conditions share the same operation labels",()=>{
  for(const kind of ["power","prepare","idle","remove"]) {
    assert.equal(operationStageLabel({name:kind,condition:{name:kind,kind},results:[]} as OperationStep),operationStageLabels[kind])
  }
})

test("R9: cost columns use recorded transitions and frozen models, never infer them from record names or current catalog",()=>{
  const step:OperationStep={name:"MAXN-to-15W-mobilenet",condition:{name:"power",kind:"power",powerMode:"planned"},results:[{name:"power",key:"saved",verdict:"Accepted",fromPowerMode:"MAXN",toPowerMode:"MODE_15W",model:"frozen-model"}]}
  assert.equal(operationDirection(step),"MAXN → MODE_15W")
  assert.equal(operationModel(step),"frozen-model")
  assert.equal(operationDirection({...step,results:[]}),"출발 모드 미기록 → 계획 목표 planned")
  assert.equal(operationModel({...step,results:[]}),"모델 미기록")
  assert.equal(operationDirection({...step,condition:undefined,results:[{...step.results[0],fromPowerMode:undefined}]}),"출발 모드 미기록 → MODE_15W")
  assert.equal(operationDirection({...step,condition:undefined,results:[]}),"전환 방향 미기록")
  assert.equal(operationModel({...step,results:[step.results[0],{...step.results[0],model:"another"}]}),"frozen-model / another")
  assert.equal(operationDirection({...step,results:[step.results[0],{...step.results[0],fromPowerMode:"MODE_30W"}]}),"MAXN → MODE_15W / MODE_30W → MODE_15W")
})

test("R9: list metadata preserves recorded unknown-step targets and keeps profile references distinct from model names",()=>{
  const steps:OperationStep[]=[
    {name:"power",condition:{name:"power",kind:"power",powerMode:"MAXN"},results:[]},
    {name:"prepare",condition:{name:"prepare",kind:"prepare",batchEnergyOptimization:"profile-ref"},results:[]},
    {name:"unmatched",condition:undefined,results:[{name:"unmatched",key:"saved",verdict:"Insufficient",fromPowerMode:"MAXN",toPowerMode:"MODE_15W",model:"historical"}]},
  ]
  assert.deepEqual(operationMetadata(steps),{directions:["출발 모드 미기록 → 계획 목표 MAXN","MAXN → MODE_15W"],models:["모델 준비: 모델 미기록 (프로파일 참조 profile-ref)","동작 종류 미기록: historical"]})
  assert.deepEqual(operationMetadata([]),{directions:["전환 동작 미기록"],models:["모델 대상 미기록"]})
})

test("R10: one-transition costs retain each recorded non-idle step without summing or inventing missing costs",()=>{
  const step=(kind:string,results:OperationStep["results"]):OperationStep=>({name:kind,condition:{name:kind,kind},results})
  const result={name:"power",key:"saved",verdict:"Accepted",durationNanos:123456789n,wallJoules:2.75}
  const rows=operationTransitionCosts([step("power",[result,{...result,key:"second",durationNanos:0n,wallJoules:0}]),step("idle",[{...result,wallJoules:999}]),step("prepare",[]),step("remove",[{...result,verdict:"Insufficient",wallJoules:null}])])
  assert.equal(rows.length,4)
  assert.deepEqual(rows.map(row=>row.result?.durationNanos),[123456789n,0n,undefined,123456789n])
  assert.deepEqual(rows.map(row=>row.result?.wallJoules),[2.75,0,undefined,null])
  assert.deepEqual(rows.map(row=>row.step.name),["power","power","prepare","remove"])
  assert.deepEqual(operationTransitionCosts([{name:"unknown",condition:undefined,results:[result]}]),[])
  assert.equal(operationTransitionCosts([{name:"unknown",condition:undefined,results:[{...result,fromPowerMode:"A",toPowerMode:"B"}]}]).length,1)
})

test("R12: compact costs and median outliers preserve per-attempt targets and exclude invalid energy",()=>{
  const step=(kind:string,seconds:number,energy:number,verdict="Accepted"):OperationStep=>({name:kind,condition:{name:kind,kind,batchEnergyOptimization:"profile"},results:[{name:kind,key:String(seconds),verdict,durationNanos:BigInt(seconds)*1000000000n,wallJoules:energy,model:"same-model"}]})
  const rows=operationTransitionCosts([step("prepare",7,400),step("prepare",8,500),step("prepare",9,600),step("prepare",268,36400),step("remove",999,99999),step("prepare",-1,Infinity,"Rejected")])
  const outliers=operationCostOutliers(rows)
  assert.equal(operationCostTime(268058690569n),"268.06")
  assert.equal(operationCostTime(0n),"0.0")
  assert.equal(operationCostEnergy(36400.75),"36,401")
  assert.match(operationCostTime(-1),/품질 경고/)
  assert.match(operationCostEnergy(undefined),/없음/)
  assert.deepEqual(outliers.slice(0,3),[{},{},{}])
  assert.equal(outliers[3].duration,268/8.5)
  assert.equal(outliers[3].energy,36400/550)
  assert.deepEqual(outliers.slice(4),[{},{}])
  assert.equal(rows[3].result?.model,"same-model")
  assert.deepEqual(operationCostOutliers(operationTransitionCosts([step("prepare",0,0),step("prepare",0,0),step("prepare",10,10)])),[{},{},{}])
})

test("outlier cohorts isolate frozen models profiles and power transition directions",()=>{
  const rows=[7,8,9,268].map((seconds,index)=>({step:{name:"prepare",condition:{name:"prepare",kind:"prepare",batchEnergyOptimization:"profile"},results:[]},result:{name:"prepare",key:String(index),verdict:"Accepted",durationNanos:seconds*1e9,wallJoules:seconds,model:"same"}}))
  assert.ok(operationCostOutliers(rows)[3].duration)
  for(const isolated of [
    rows.map((row,index)=>({...row,result:{...row.result,model:`model-${index}`}})),
    rows.map((row,index)=>({...row,step:{...row.step,condition:{...row.step.condition,batchEnergyOptimization:`profile-${index}`}}})),
    rows.map((row,index)=>({...row,step:{name:"power",condition:{name:"power",kind:"power"},results:[]},result:{...row.result,fromPowerMode:index<2?"A":"B",toPowerMode:index<2?"B":"A"}})),
  ])assert.deepEqual(operationCostOutliers(isolated),[{},{},{},{}])
})
