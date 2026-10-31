import assert from "node:assert/strict"
import test from "node:test"
import { runFailure, runStatusObject, exactRun, safeMeasurementReturn, significant, evidenceLocations, isProjectionMeasurementKind, measurementHref, measurementListHref, measurementPath, measurementReason, measurementUIDMismatch, occupancyDuration, occupancySupply, occupancyThroughput, operationDuration, operationWallEnergy, operationSteps } from "./measurements.ts"
import { relayPlan } from "./relay.ts"
import { serverConfig } from "./server-config.ts"
import { parseSchedulerJSON } from "./scheduler.ts"
import type { DispatchOccupancyMeasurement, OperationCostMeasurement } from "./read-types.ts"
import { formatMeasurementTime, measurementConditionDiff, measurementReasonSummary } from "./measurements.ts"
import { formatMeasurementNumber, measurementCheckText, measurementRequestName, measurementUncertainty, resultRowsCSV } from "./measurements.ts"
import type { ResultRow } from "./read-types.ts"
import { operationTotals } from "./measurements.ts"

test("measurement quantities use fixed unit precision and preserve valid zero and negative CI bounds",()=>{
  assert.equal(formatMeasurementNumber(0,"W"),"0.0")
  assert.equal(formatMeasurementNumber(12,"ms"),"12.00")
  assert.equal(formatMeasurementNumber(0.00001234,"J/건"),"0.0000")
  assert.equal(formatMeasurementNumber(-0.12345,"J/건"),"-0.1235")
  assert.equal(formatMeasurementNumber(1234.5,"J"),"1,235")
  assert.equal(formatMeasurementNumber(null,"W"),"미제공")
})
test("CSV retains row identity, source references and exact numbers while escaping every cell",()=>{
  const rows=[{id:'row"1',rowKind:"source-reference",batch:1,sourceProfileId:"source",condition:{dataset:"a,b\nc"},p95Milliseconds:0.123456789,incrementalJoulesPerItem:0,confidence:{lower:-1,upper:1,halfWidth:1,level:0.95},reuse:{source:"origin"}}] as unknown as ResultRow[]
  const csv=resultRowsCSV(rows)
  assert.ok(csv.includes('"row""1"'))
  assert.ok(csv.includes('"source-reference"'))
  assert.ok(csv.includes('"0.123456789"'))
  assert.ok(csv.includes('""dataset"":""a,b\\nc""'))
  assert.ok(csv.includes('""lower"":-1'))
  assert.ok(csv.includes('"source"'))
})
test("request identity includes mode, complete path and CPU and only changes suffix for collisions",()=>{
  const condition={model:"model",node:"node",powerMode:"mode",path:"runtime-backend",cpu:"2"}
  const now=new Date(2026,9,30),taken=new Set<string>()
  const name=measurementRequestName(condition,taken,now)
  assert.equal(name,"model-node-mode-runtime-backend-cpu2-20261030")
  taken.add(name)
  assert.equal(measurementRequestName(condition,taken,now),`${name}-2`)
  const pinned=name
  taken.add(`${name}-2`)
  assert.equal(pinned,name)
})
test("CSV export neutralizes formulas without altering numeric precision",()=>{
  const rows=[{id:"=HYPERLINK(test)",batch:1,condition:{token:"private",dataset:"a_b% & # {x}"},incrementalJoulesPerItem:-0.123456789}] as unknown as ResultRow[]
  const csv=resultRowsCSV(rows)
  assert.ok(csv.includes(`"'=HYPERLINK(test)"`))
  assert.ok(csv.includes('"-0.123456789"'))
  assert.ok(!csv.includes("private"))
})

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
  assert.equal(operationDuration(0),"0.00 ms")
  assert.equal(operationWallEnergy(0),"0 J")
  assert.equal(operationDuration(3807875),"3.81 ms")
  const wire = parseSchedulerJSON('{"status":{"phase":"Failed","reason":"MeasurementFailed","steps":[{"name":"idle","key":"exact-key","verdict":"Failed","operationID":"op-uid-2","durationNanos":9223372036854775807,"wallJoules":null,"reason":"IsolationUnconfirmed"}]}}') as Pick<OperationCostMeasurement,"status">
  const rows = operationSteps({...operation,...wire})
  const result = rows[2].results[0]
  assert.equal(result.operationID,"op-uid-2")
  assert.equal(result.reason,"IsolationUnconfirmed")
  assert.equal(result.verdict,"Failed")
  assert.equal(wire.status.phase,"Failed")
  assert.equal(operationDuration(result.durationNanos),"9,223,372,036,854.78 ms")
  assert.equal(operationWallEnergy(result.wallJoules),"수용된 에너지 없음")
  assert.match(operationDuration(-3807875n),/품질 경고/)
  assert.doesNotMatch(operationDuration(-3807875n),/-3|ms/)
  for (const value of [Number.MAX_SAFE_INTEGER + 1,1.5,NaN,Infinity,"100"]) assert.match(operationDuration(value),/품질 경고/)
  for (const value of [-1,NaN,Infinity,"0"]) assert.match(operationWallEnergy(value),/품질 경고/)
  assert.equal(operationWallEnergy(2.75),"2.75 J")
  assert.equal(operationDuration(operationSteps(operation)[2].results[0]?.durationNanos),"실측 시간 기록 없음")
})

test("operation totals require the complete plan, accepted energy, and nonnegative quantities",()=>{
  const complete:OperationCostMeasurement={...operation,status:{phase:"Complete",steps:operation.conditions.steps.map(step=>({name:step.name,key:step.name,verdict:"Accepted",durationNanos:1_500_000_000n,wallJoules:2}))}}
  assert.deepEqual(operationTotals(complete),{durationNanos:6_000_000_000n,wallJoules:8})
  assert.equal(operationDuration(operationTotals(complete).durationNanos,"s"),"6 s")
  assert.equal(operationDuration(1n,"s"),"0.000000001 s")
  assert.equal(operationWallEnergy(0.0001),"0.0001 J")
  const replace=(patch:Partial<NonNullable<OperationCostMeasurement["status"]["steps"]>[number]>)=>({...complete,status:{...complete.status,steps:complete.status.steps!.map((step,index)=>index?step:{...step,...patch})}})
  assert.deepEqual(operationTotals({...complete,status:{steps:complete.status.steps!.slice(1)}}),{durationNanos:undefined,wallJoules:undefined})
  assert.deepEqual(operationTotals(replace({durationNanos:null,wallJoules:null})),{durationNanos:undefined,wallJoules:undefined})
  assert.deepEqual(operationTotals(replace({durationNanos:-1n,wallJoules:-1})),{durationNanos:null,wallJoules:null})
  assert.equal(operationTotals(replace({durationNanos:NaN})).durationNanos,null)
  assert.equal(operationTotals(replace({wallJoules:Infinity})).wallJoules,null)
  assert.equal(operationTotals(replace({verdict:"Rejected"})).wallJoules,undefined)
  const zero={...complete,status:{steps:complete.status.steps!.map(step=>({...step,durationNanos:0n,wallJoules:0}))}}
  assert.deepEqual(operationTotals(zero),{durationNanos:0n,wallJoules:0})
  assert.deepEqual(operationTotals({...complete,status:{steps:[...complete.status.steps!,complete.status.steps![0]]}}),{durationNanos:null,wallJoules:null})
})

test("occupancy preserves missing and zero d, string throughput, unspecified supply and logical location identity", () => {
  assert.equal(occupancyDuration(occupancy.status.perRequestNanos),"결과 없음")
  assert.equal(occupancyDuration(0),"0.00ms/request")
  assert.equal(occupancyDuration(3807875n),"3.81ms/request")
  assert.equal(occupancyThroughput("0.0000012300"),"0.00000123 req/s")
  assert.equal(occupancyThroughput("0"),"0 req/s")
  for (const value of [undefined,""]) assert.equal(occupancyThroughput(value),"결과 없음")
  assert.equal(occupancySupply(occupancy.conditions.supplyMode),"기록되지 않았어요")
  assert.equal(occupancySupply("closed-loop"),"고정 동시 요청")
  assert.equal(occupancySupply("poisson"),"포아송 공급")
  assert.equal(occupancySupply("new-mode"),"new-mode")
  assert.deepEqual(evidenceLocations([{store:"a",key:"k"},{store:"b",key:"k"},{store:"a",key:"k"}],{store:"b",key:"k"}),[{store:"a",key:"k"},{store:"b",key:"k"}])
  assert.deepEqual(evidenceLocations([]),[])
})


test("measurement reasons expose the server-provided failure or insufficiency instead of an unknown-code placeholder", () => {
  for (const reason of ["probe request timed out after 10s", "only 2 batches completed; minimum is 5", "UnknownVerdict"]) {
    assert.equal(measurementReason(reason),reason)
  }
  assert.equal(measurementReason("MissingDataset"),"측정에 쓸 데이터셋이 지정되지 않았어요. 서비스 설정에서 데이터셋을 골라 주세요. (MissingDataset)")
})

test("raw occupancy negatives and invalid values warn without replacing zero or precision",()=>{
  for(const value of [-3807875n,-5720000,-1n]) { assert.match(occupancyDuration(value),/품질 경고.*음수/); assert.doesNotMatch(occupancyDuration(value),/-5|-3|ms\/request/) }
  for(const value of [NaN,Infinity,-Infinity,"NaN","broken",null]) assert.match(occupancyDuration(value),/품질 경고/)
  for(const value of ["NaN","Infinity","-1","broken"," "]) assert.match(occupancyThroughput(value),/품질 경고/)
  assert.equal(significant(0.09261),"0.0926")
  assert.equal(significant(0.08682),"0.0868")
  for(const value of ["//evil.test","https://evil.test","/settings","/measurements\\evil","\n/measurements"])assert.equal(safeMeasurementReturn(value),"/measurements")
  assert.equal(safeMeasurementReturn("/measurements?model=x&page=2"),"/measurements?model=x&page=2")
})

test("run identity requires exact names and expected UID, not q partial matches",()=>{
  const base={namespace:"n",name:"run",uid:"u",resourceVersion:"1",generation:1,creationTimestamp:"",spec:{},status:{}}
  const rows=[{...base,name:"run-2"},base,{...base,namespace:"other"}]
  assert.equal(exactRun(rows,"n","run","u"),base)
  assert.equal(exactRun(rows,"n","run","old"),undefined)
})

test("run status decodes JSON fields while failure summary uses only server classification",()=>{
  assert.deepEqual(runStatusObject('{"exitCode":3,"logTail":"error"}'),{exitCode:3,logTail:"error"})
  for(const value of ["broken","null","[]",null,42]) assert.equal(runStatusObject(value),undefined)
  const run={namespace:"n",name:"r",uid:"u",resourceVersion:"1",generation:1,creationTimestamp:"",spec:{},status:{runtimeTermination:JSON.stringify({exitCode:3,logTail:"\u001b[31mFailed to allocate memory for requested buffer\u001b[0m"})},assessment:{failureSummary:[{code:"GPUMemoryExhausted",label:"GPU 메모리 부족",message:"GPU 메모리가 부족해 측정을 완료하지 못했습니다.",exitCode:3}]}} as unknown as Parameters<typeof runFailure>[0]
  const failure=runFailure(run)
  assert.equal(failure.title,"GPU 메모리 부족")
  assert.equal(failure.exitCode,3)
  assert.equal(failure.excerpt,"GPU 메모리가 부족해 측정을 완료하지 못했습니다.")
  assert.match(failure.next,/메모리/)
  assert.doesNotMatch(failure.log,/\u001b/)
  assert.equal(runFailure({...run,assessment:undefined}).title,"실패 분류 미제공")
  assert.doesNotMatch(runFailure({...run,assessment:undefined}).excerpt,/Failed to allocate/)
})

test("measurement timestamps retain the explicit timezone",()=>{
  assert.match(formatMeasurementTime("2026-10-30T00:00:00Z"),/9:00:00.*KST/)
})

test("retry condition diff includes CPU and protocol changes while summaries retain original reasons separately",()=>{
  const spec={state:{candidate:{modelSpec:"m",preprocessor:"p",deviceClass:"d",executionPath:"cpu",powerMode:"MAXN"},targetNode:"n",cpuRequest:"1"},cpuQuota:"1",trialsPerBatch:3,batchSizes:[1,2],retryLimit:1,activeProtocol:{durationSeconds:10,dataset:"set"},idleProtocol:{durationSeconds:10}}
  assert.deepEqual(measurementConditionDiff(spec,spec),[])
  assert.deepEqual(measurementConditionDiff(spec,{...spec,cpuQuota:"2",activeProtocol:{...spec.activeProtocol,durationSeconds:20}}),["CPU: 1 → 2","부하 durationSeconds: 10 → 20"])
  assert.match(measurementReasonSummary("context deadline exceeded"),/시간.*초과/)
  assert.match(measurementReasonSummary("cost requires exactly one proven primitive state effect"),/단일 상태 변경/)
})

test("R10: projection reasons summarize repeated state-effect errors and preserve useful absence language",()=>{
  assert.match(measurementReasonSummary("MeasurementFailed"),/실패/)
  assert.match(measurementReasonSummary("IsolationUnconfirmed"),/단독/)
  assert.match(measurementReasonSummary(""),/사유.*미제공/)
  assert.match(measurementReasonSummary("accepted evidence"),/원문/)
  assert.doesNotMatch(measurementReasonSummary("accepted evidence"),/채택되지/)
  assert.match(measurementReasonSummary('native preparation Pod failed; json: unknown field "energyBelowResolution"'),/설정.*지원/)
  assert.match(measurementReasonSummary("native preparation Pod failed"),/모델 준비/)
  assert.match(measurementReasonSummary("Stored occupancy does not match this request and status"),/요청.*일치/)
})


test("energy uncertainty reports relative half width without inventing an acceptance threshold",()=>{
  const row={incrementalJoulesPerItem:0.1,confidence:{level:0.95,halfWidth:0.02}} as ResultRow
  assert.match(measurementUncertainty(row),/95.*±20\.00%/)
  assert.match(measurementUncertainty({...row,incrementalJoulesPerItem:-0.1}),/±20\.00%/)
  assert.match(measurementUncertainty({...row,incrementalJoulesPerItem:0}),/±0\.0200 J\/건/)
  assert.doesNotMatch(measurementUncertainty({...row,incrementalJoulesPerItem:0}),/Infinity|NaN/)
  assert.equal(measurementUncertainty({...row,confidence:{...row.confidence!,halfWidth:NaN}}),"오차 미제공")
})

test("failure actions name the measured node model path and smallest-batch limit",()=>{
  const run={spec:{targetNode:"genesis2",candidate:{modelSpec:"smolvlm2",executionPath:"cuda"},measurement:{batch:1}},status:{},assessment:{failureSummary:[{code:"GPUMemoryExhausted"}]}} as unknown as Parameters<typeof runFailure>[0]
  assert.match(runFailure(run).next,/genesis2.*smolvlm2/)
  assert.match(runFailure(run).next,/배치 1.*cpu|배치 1.*int8/)
  assert.match(runFailure({...run,spec:{...run.spec,measurement:{batch:8}}}).next,/배치 8.*작은 배치/)
  for(const code of ["HostOOM","PowerObservationTimeout","ImagePullFailed","RuntimeFailed","Other"]) {
    const next=runFailure({...run,assessment:{...run.assessment!,failureSummary:[{...run.assessment!.failureSummary![0],code}]}}).next
    assert.match(next,/genesis2.*smolvlm2/)
  }
})

test("power quality language identifies the checks operators still need to confirm",()=>{
  assert.equal(measurementCheckText({code:"FrozenPowerWindowPolicy",message:"모든 활성·유휴 창의 동결 표본·gap·실패 정책"}),"전력 기록의 표본 수·누락 구간·실패 처리 기준")
  assert.equal(measurementCheckText({code:"DatasetInputContract",message:"입력 image 크기 불일치"}),"입력 image 크기 불일치")
})
