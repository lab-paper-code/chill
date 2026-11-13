import assert from "node:assert/strict"
import test from "node:test"
import { runFailure, runStatusObject, exactRun, safeMeasurementReturn, significant, measurementPath, occupancyDuration, occupancyThroughput, occupancyReason, operationSteps } from "./measurements.ts"
import { relayPlan } from "./relay.ts"
import { serverConfig } from "./server-config.ts"
import type { DispatchOccupancyMeasurement, OperationCostMeasurement } from "./read-types.ts"
import { executionPathLabel, measurementReasonSummary } from "./measurements.ts"
import { formatMeasurementNumber, measurementRequestName } from "./measurements.ts"

test("measurement quantities use fixed unit precision and preserve valid zero and negative CI bounds",()=>{
  assert.equal(formatMeasurementNumber(0,"W"),"0.0")
  assert.equal(formatMeasurementNumber(12,"ms"),"12.00")
  assert.equal(formatMeasurementNumber(0.00001234,"J/건"),"0.0000")
  assert.equal(formatMeasurementNumber(-0.12345,"J/건"),"-0.1235")
  assert.equal(formatMeasurementNumber(1234.5,"J"),"1,235")
  assert.equal(formatMeasurementNumber(null,"W"),"미제공")
})
test("request identity includes mode and complete path without CPU reservation and only changes suffix for collisions",()=>{
  const condition={model:"model",node:"node",powerMode:"mode",path:"runtime-backend"}
  const now=new Date(2026,9,30),taken=new Set<string>()
  const name=measurementRequestName(condition,taken,now)
  assert.equal(name,"model-node-mode-runtime-backend-20261030")
  taken.add(name)
  assert.equal(measurementRequestName(condition,taken,now),`${name}-2`)
  const pinned=name
  taken.add(`${name}-2`)
  assert.equal(pinned,name)
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
  phase:"Complete",d:null,
  conditions:{workload:"demo",batchEnergyOptimization:"beo",batch:4,warmupSeconds:2,durationSeconds:10,workers:8},status:{phase:"Complete"},
}

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

test("raw occupancy negatives and invalid values warn without replacing zero or precision",()=>{
  for(const value of [-3807875n,-5720000,-1n]) {assert.equal(occupancyDuration(value),"—");assert.equal(occupancyReason({...occupancy,status:{perRequestNanos:value}}),"점유시간 음수")}
  for(const value of [NaN,Infinity,-Infinity,"NaN","broken",null]) assert.equal(occupancyDuration(value),"—")
  for(const value of ["NaN","Infinity","-1","broken"," "]) {assert.equal(occupancyThroughput(value),"—");assert.equal(occupancyReason({...occupancy,status:{throughput:value}}),"처리량 오류")}
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
  assert.match(failure.actions.flat().join(" "),/메모리/)
  assert.doesNotMatch(failure.log,/\u001b/)
  assert.equal(runFailure({...run,assessment:undefined}).title,"실패 분류 미제공")
  assert.doesNotMatch(runFailure({...run,assessment:undefined}).excerpt,/Failed to allocate/)
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

test("failure actions name the measured node model path and smallest-batch limit",()=>{
  const run={spec:{targetNode:"genesis2",candidate:{modelSpec:"smolvlm2",executionPath:"cuda"},measurement:{batch:1}},status:{},assessment:{failureSummary:[{code:"GPUMemoryExhausted"}]}} as unknown as Parameters<typeof runFailure>[0]
  assert.deepEqual(runFailure(run).target,[["노드","genesis2"],["모델","smolvlm2"],["경로","cuda"]])
  assert.match(runFailure(run).actions.flat().join(" "),/CPU·int8/)
  assert.match(runFailure({...run,spec:{...run.spec,measurement:{batch:8}}}).actions.flat().join(" "),/작은 배치/)
  for(const code of ["HostOOM","PowerObservationTimeout","ImagePullFailed","RuntimeFailed","Other"]) {
    const failure=runFailure({...run,assessment:{...run.assessment!,failureSummary:[{...run.assessment!.failureSummary![0],code}]}})
    assert.deepEqual(failure.target,runFailure(run).target)
  }
})

test("R13: occupancy quantity cells are unitless while invalid-value reasons remain separate",()=>{
  assert.equal(occupancyDuration(3807875n),"3.81")
  assert.equal(occupancyDuration(-1n),"—")
  assert.equal(occupancyDuration(undefined),"—")
  assert.equal(occupancyThroughput("87.1"),"87.1")
  assert.equal(occupancyThroughput("-1"),"—")
  assert.equal(occupancyThroughput(undefined),"—")
})

test("R13: occupancy issues retain short reasons even when the checklist has no checks",()=>{
  assert.equal(occupancyReason({...occupancy,status:{perRequestNanos:0,throughput:"0",reason:"Stored occupancy does not match this request and status"}}),"요청·점유시간 근거 불일치")
  assert.equal(occupancyReason({...occupancy,status:{reason:"Signed negative d"}}),"점유시간 음수")
  assert.equal(occupancyReason({...occupancy,status:{reason:"UnknownReason"}}),"UnknownReason")
  assert.equal(occupancyReason({...occupancy,status:{reason:"TooFewBatches 12; NotSaturated 0.123456; FutureReason 7"}}),"배치 표본 부족 · 부하 미포화 · FutureReason 7")
  assert.equal(occupancyReason({...occupancy,status:{reason:"UnknownPhysicalContinuity"}}),"실행 연속성 미확인")
  assert.equal(occupancyReason({...occupancy,status:{},assessment:{...occupancy.assessment!,reasons:[{code:"OccupancyEvidenceMismatch",message:"",label:"",sourceIds:[]}]}}),"요청·점유시간 근거 불일치")
})

test("R14: occupancy evidence and interruption codes use server-grounded Korean statuses",()=>{
  const labels={OccupancyEvidenceMissing:"점유시간 근거 파일 없음",OccupancyEvidenceUnavailable:"점유시간 근거 읽기 실패",OccupancyEvidenceInvalid:"점유시간 근거 해석 실패",OccupancyThroughputInvalid:"처리량 오류",ChillServerRestart:"서버 재시작으로 측정 중단",Failed:"측정 실패",Insufficient:"채택 근거 부족"}
  for(const [reason,label] of Object.entries(labels))assert.equal(occupancyReason({...occupancy,status:{reason}}),label)
  assert.equal(occupancyReason({...occupancy,status:{reason:"FutureOccupancyCode"}}),"FutureOccupancyCode")
})

test("measurement labels distinguish backend variants while translating workload protocols",()=>{
  assert.equal(executionPathLabel("firstfloor-ort-cpu-arm64"),"CPU · ARM 64비트 (firstfloor-ort-cpu-arm64)")
  const paths=["firstfloor-ort-cuda-arm64","firstfloor-rnn-ort-cuda-arm64","firstfloor-vlm-ort-cuda-arm64"]
  const labels=paths.map(path=>executionPathLabel(path))
  assert.equal(new Set(labels).size,paths.length)
  for(const [index,path] of paths.entries())assert.equal(labels[index],`CUDA · ARM 64비트 (${path})`)
  assert.equal(executionPathLabel("new-private-backend",2),"등록 실행 경로 3")
  assert.equal(measurementReasonSummary("No verified catalog profile for current condition"),"현재 조건의 검증된 프로파일 없음")
})

test("measurement routes accept only the two projection kinds and keep UI queries outside the upstream request", () => {
  const config = serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"chill"})
  for (const kind of ["operation-cost-measurements","dispatch-occupancy-measurements"] as const) {
    for (const name of [undefined,"sample"]) {
      const path = measurementPath(kind,"research",name)
      assert.deepEqual(relayPlan("GET",path.split("/"),"",config),{path:`/api/${path}`,kind:"query",timeoutMs:config.queryTimeoutMs,bodyAllowed:false})
      for (const search of ["?namespace=other","?uid=uid-1","?workload=uid","?row=key","?limit=10","?offset=0","?q=sample"]) {
        assert.throws(() => relayPlan("GET",path.split("/"),search,config),/DashboardQueryInvalid/)
      }
      for (const method of ["POST","PUT","DELETE"]) {
        if(kind==="dispatch-occupancy-measurements"&&!name&&method==="POST") {
          assert.equal(relayPlan(method,path.split("/"),"",config).kind,"mutation")
        } else if(kind==="dispatch-occupancy-measurements"&&name&&method==="DELETE") {
          assert.equal(relayPlan(method,path.split("/"),"?uid=uid-1",config).kind,"mutation")
        } else assert.throws(() => relayPlan(method,path.split("/"),"",config),/DashboardMethodNotAllowed/)
      }
    }
  }
  assert.equal(relayPlan("GET",["v1","scheduler","capacity"],"",config).path,"/api/v1/scheduler/capacity")
  for (const search of ["?row=key","?workload=uid"]) assert.throws(() => relayPlan("GET",["v1","scheduler","capacity"],search,config),/DashboardQueryInvalid/)
})

test("occupancy preserves missing and zero d, string throughput and logical location identity", () => {
  assert.equal(occupancyDuration(occupancy.status.perRequestNanos),"—")
  assert.equal(occupancyDuration(0),"0.00")
  assert.equal(occupancyDuration(3807875n),"3.81")
  assert.equal(occupancyThroughput("0.0000012300"),"0.00000123")
  assert.equal(occupancyThroughput("0"),"0")
  for (const value of [undefined,""]) assert.equal(occupancyThroughput(value),"—")
})
