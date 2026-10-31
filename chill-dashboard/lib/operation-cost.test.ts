import assert from "node:assert/strict"
import test from "node:test"
import { largestOperationStages, operationStageLabel, operationStageLabels } from "./operation-cost.ts"
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
