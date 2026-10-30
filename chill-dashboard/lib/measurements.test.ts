import assert from "node:assert/strict"
import test from "node:test"
import { exactRun, safeMeasurementReturn, significant, evidenceLocations, isProjectionMeasurementKind, measurementHref, measurementListHref, measurementPath, measurementReason, measurementState, measurementUIDMismatch, occupancyDuration, occupancySupply, occupancyThroughput, operationDuration, operationWallEnergy, operationSteps } from "./measurements.ts"
import { relayPlan } from "./relay.ts"
import { serverConfig } from "./server-config.ts"
import { parseSchedulerJSON } from "./scheduler.ts"
import type { DispatchOccupancyMeasurement, OperationCostMeasurement } from "./read-types.ts"

const metadata = {namespace:"research",name:"sample",uid:"uid-1",resourceVersion:"42",creationTimestamp:"2026-10-30T00:00:00Z"}
const references = {cr:{apiVersion:"edge.dacs.io/v1alpha1",kind:"OperationCostMeasurement",namespace:metadata.namespace,name:metadata.name,uid:metadata.uid},evidence:[{store:"evidence",key:"power-key"}]}
const operation:OperationCostMeasurement = {
  kind:"OperationCostMeasurement",metadata,node:"node-1",references,
  conditions:{steps:[{name:"power",kind:"power",powerMode:"low"},{name:"prepare",kind:"prepare",batchEnergyOptimization:"beo"},{name:"idle",kind:"idle",idleDurationSeconds:5},{name:"remove",kind:"remove",batchEnergyOptimization:"beo"}]},
  status:{phase:"Insufficient",reason:"UnknownVerdict",steps:[{name:"remove",key:"remove-key",verdict:"NewVerdict"},{name:"power",key:"power-key",verdict:"Accepted"},{name:"orphan",key:"",verdict:"Rejected"}]},
}
const occupancy:DispatchOccupancyMeasurement = {
  kind:"DispatchOccupancyMeasurement",metadata,node:"node-1",references:{...references,cr:{...references.cr,kind:"DispatchOccupancyMeasurement"},evidence:[]},
  conditions:{workload:"demo",batchEnergyOptimization:"beo",batch:4,warmupSeconds:2,durationSeconds:10,workers:8},status:{phase:"Complete"},
}

test("measurement routes accept only the two projection kinds and keep UI queries outside the upstream request", () => {
  const config = serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill"})
  for (const kind of ["operation-cost-measurements","dispatch-occupancy-measurements"] as const) {
    assert.equal(isProjectionMeasurementKind(kind),true)
    for (const name of [undefined,"sample"]) {
      const path = measurementPath(kind,"research",name)
      assert.deepEqual(relayPlan("GET",path.split("/"),"",config),{path:`/api/${path}`,kind:"query",timeoutMs:config.queryTimeoutMs,bodyAllowed:false})
      for (const search of ["?namespace=other","?uid=uid-1","?workload=uid","?row=key","?limit=10","?offset=0","?q=sample"]) {
        assert.throws(() => relayPlan("GET",path.split("/"),search,config),/DashboardQueryInvalid/)
      }
      for (const method of ["POST","PUT","DELETE"]) assert.throws(() => relayPlan(method,path.split("/"),"",config),/DashboardMethodNotAllowed/)
    }
    assert.equal(measurementListHref(kind,"a b"),`/measurements/${kind}?namespace=a%20b`)
    assert.equal(measurementHref(kind,{namespace:"a b",name:"a:b",uid:"u&1"}),`/measurements/${kind}/a%20b/a%3Ab?uid=u%261`)
  }
  for (const kind of ["unknown","power-mode-transition-costs","profiling-runs"]) assert.equal(isProjectionMeasurementKind(kind),false)
  assert.equal(measurementUIDMismatch(metadata.uid,null),false)
  assert.equal(measurementUIDMismatch(metadata.uid,metadata.uid),false)
  assert.equal(measurementUIDMismatch(metadata.uid,"old-uid"),true)
  assert.equal(relayPlan("GET",["v1","scheduler","capacity"],"",config).path,"/api/v1/scheduler/capacity")
  for (const search of ["?row=key","?workload=uid"]) assert.throws(() => relayPlan("GET",["v1","scheduler","capacity"],search,config),/DashboardQueryInvalid/)
})

test("operation result joins retain condition order, missing results and unmatched status without inferring acceptance", () => {
  const steps = operationSteps(operation)
  assert.deepEqual(steps.map(step => step.name),["power","prepare","idle","remove","orphan"])
  assert.equal(steps[0].results[0].key,"power-key")
  assert.equal(steps[0].results[0].verdict,"Accepted")
  assert.deepEqual(steps[1].results,[])
  assert.deepEqual(steps[2].results,[])
  assert.equal(steps[3].condition?.batchEnergyOptimization,"beo")
  assert.equal(steps[3].results[0].verdict,"NewVerdict")
  assert.equal(steps[4].condition,undefined)
  assert.deepEqual(operationSteps({...operation,status:{phase:"Complete"}}).map(step => step.results),[[],[],[],[]])
})

test("operation actual duration and accepted wall energy preserve zero, missing values and exact nanoseconds", () => {
  for (const value of [undefined,null]) {
    assert.equal(operationDuration(value),"실측 시간 기록 없음")
    assert.equal(operationWallEnergy(value),"수용된 에너지 없음")
  }
  assert.equal(operationDuration(0),"0 ms (0 ns)")
  assert.equal(operationWallEnergy(0),"0 J")
  assert.equal(operationDuration(3807875),"3.807875 ms (3807875 ns)")
  const wire = parseSchedulerJSON('{"status":{"phase":"Failed","reason":"MeasurementFailed","steps":[{"name":"idle","key":"exact-key","verdict":"Failed","operationID":"op-uid-2","durationNanos":9223372036854775807,"wallJoules":null,"reason":"IsolationUnconfirmed"}]}}') as Pick<OperationCostMeasurement,"status">
  const rows = operationSteps({...operation,...wire})
  const result = rows[2].results[0]
  assert.equal(result.operationID,"op-uid-2")
  assert.equal(result.reason,"IsolationUnconfirmed")
  assert.equal(result.verdict,"Failed")
  assert.equal(wire.status.phase,"Failed")
  assert.equal(operationDuration(result.durationNanos),"9223372036854.775807 ms (9223372036854775807 ns)")
  assert.equal(operationWallEnergy(result.wallJoules),"수용된 에너지 없음")
  assert.equal(operationDuration(-3807875n),"-3.807875 ms (-3807875 ns) · 품질 경고·사용 확인 필요")
  for (const value of [Number.MAX_SAFE_INTEGER + 1,1.5,NaN,Infinity,"100"]) assert.match(operationDuration(value),/품질 경고/)
  for (const value of [-1,NaN,Infinity,"0"]) assert.match(operationWallEnergy(value),/품질 경고/)
  assert.equal(operationWallEnergy(2.75),"2.75 J")
  assert.equal(operationDuration(operationSteps(operation)[2].results[0]?.durationNanos),"실측 시간 기록 없음")
})

test("occupancy preserves missing and zero d, string throughput, unspecified supply and logical location identity", () => {
  assert.equal(occupancyDuration(occupancy.status.perRequestNanos),"결과 없음")
  assert.equal(occupancyDuration(0),"0ms/request")
  assert.equal(occupancyDuration(3807875n),"3.81ms/request")
  assert.equal(occupancyThroughput("0.0000012300"),"0.0000012300 req/s")
  assert.equal(occupancyThroughput("0"),"0 req/s")
  for (const value of [undefined,""]) assert.equal(occupancyThroughput(value),"결과 없음")
  assert.equal(occupancySupply(occupancy.conditions.supplyMode),"기록되지 않았어요")
  assert.equal(occupancySupply("closed-loop"),"고정 동시 요청")
  assert.equal(occupancySupply("poisson"),"포아송 공급")
  assert.equal(occupancySupply("new-mode"),"new-mode")
  assert.deepEqual(evidenceLocations([{store:"a",key:"k"},{store:"b",key:"k"},{store:"a",key:"k"}],{store:"b",key:"k"}),[{store:"a",key:"k"},{store:"b",key:"k"}])
  assert.deepEqual(evidenceLocations([]),[])
})

test("all measurement terminal and unknown phases carry explicit sentences and non-color-only tone semantics", () => {
  assert.deepEqual(["Measuring","Complete","Insufficient","Failed",undefined,"Unknown"].map(phase => measurementState(phase)),[
    {label:"측정 중이에요",tone:"info"},{label:"측정이 끝났어요",tone:"ok"},{label:"판단할 근거가 부족해요",tone:"warn"},{label:"측정을 끝내지 못했어요",tone:"fail"},{label:"측정 상태를 확인할 수 없어요",tone:"idle"},{label:"측정 상태를 확인할 수 없어요",tone:"idle"},
  ])
})

test("measurement reasons expose the server-provided failure or insufficiency instead of an unknown-code placeholder", () => {
  for (const reason of ["probe request timed out after 10s", "only 2 batches completed; minimum is 5", "UnknownVerdict"]) {
    assert.equal(measurementReason(reason),reason)
  }
  assert.equal(measurementReason("MissingDataset"),"측정에 쓸 데이터셋이 지정되지 않았어요. 서비스 설정에서 데이터셋을 골라 주세요. (MissingDataset)")
})

test("raw occupancy negatives and invalid values warn without replacing zero or precision",()=>{
  assert.equal(occupancyDuration(-3807875n),"-3.81ms/request · 품질 경고·사용 확인 필요")
  for(const value of [NaN,Infinity,-Infinity,"NaN","broken",null]) assert.match(occupancyDuration(value),/품질 경고/)
  for(const value of ["NaN","Infinity","-1","broken"," "]) assert.match(occupancyThroughput(value),/품질 경고/)
  assert.equal(significant(0.09261),"0.09261")
  assert.equal(significant(0.08682),"0.08682")
  for(const value of ["//evil.test","https://evil.test","/settings","/measurements\\evil","\n/measurements"])assert.equal(safeMeasurementReturn(value),"/measurements")
  assert.equal(safeMeasurementReturn("/measurements?model=x&page=2"),"/measurements?model=x&page=2")
})

test("run identity requires exact names and expected UID, not q partial matches",()=>{
  const base={namespace:"n",name:"run",uid:"u",resourceVersion:"1",generation:1,creationTimestamp:"",spec:{},status:{}}
  const rows=[{...base,name:"run-2"},base,{...base,namespace:"other"}]
  assert.equal(exactRun(rows,"n","run","u"),base)
  assert.equal(exactRun(rows,"n","run","old"),undefined)
})
