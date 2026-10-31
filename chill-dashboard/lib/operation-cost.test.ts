import assert from "node:assert/strict"
import test from "node:test"
import { operationCostDuration, operationCostEnergy, largestOperationStages } from "./operation-cost.ts"
import type { OperationStep } from "./measurements.ts"

test("operation cost display bounds precision without changing missing and invalid values",()=>{
  assert.equal(operationCostDuration(874863796474n),"874.9 s")
  assert.equal(operationCostDuration(0),"0 s")
  assert.equal(operationCostEnergy(6290.123456),"6,290.1 J")
  assert.equal(operationCostEnergy(0),"0 J")
  assert.match(operationCostDuration(-1),/품질 경고/)
  assert.match(operationCostDuration(10n**400n),/품질 경고/)
  assert.match(operationCostEnergy(Infinity),/품질 경고/)
  assert.match(operationCostEnergy(undefined),/없음/)
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
