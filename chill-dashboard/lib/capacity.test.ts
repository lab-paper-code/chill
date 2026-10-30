import assert from "node:assert/strict"
import test from "node:test"
import { capacityComparison, capacityPath, capacityPercentile, capacityPolling, capacityRate, capacityReason, capacityRows, capacityStatus, selectedCapacityRow, type CapacityInput, type CapacityRow } from "./capacity.ts"
import { parseSchedulerJSON, stringifySchedulerJSON } from "./scheduler.ts"

const input:CapacityInput={
  workloadUID:"workload-uid",slo:{percentile:99,targetLatencyNanos:100000000},node:{name:"node-a",uid:"node-uid"},powerMode:"mode-a",profileID:"profile-id",
  allocation:{cpus:null,memoryBytes:9007199254740993n,threads:2,cpuCount:2,cpuReserved:false},maxBatch:4,
  completionMarginMS:2.5,capacityWindowNanos:9007199254740993n,capacityWindows:5,capacityPassingWindows:4,capacityMinimumRequests:100,
  capacityGridSteps:10,capacityBisectionWidth:0.01,capacityTailSamples:20,
  dispatchOccupancy:{node:{name:"node-a",uid:"node-uid"},powerMode:"mode-a",runtimeImage:"image@sha256:abc",model:"model",perRequestNanos:0,source:"measurement identity: kept verbatim"},
}
const built:CapacityRow={key:"input-key",input,status:"Built",mu:100,kappa:0,builtAt:"2026-10-30T01:00:00Z",reason:"BatchServiceExceedsSLO",minimumServiceNanos:100000000,elapsedNanos:10}

test("capacity selection preserves exact row key and local workload filter, never falling back",()=>{
  const next={...built,key:"new-input-key",input:{...input,capacityGridSteps:20}}
  assert.equal(selectedCapacityRow([next],built.key),undefined)
  assert.deepEqual(capacityRows([built,next],"absent-uid"),[])
  assert.equal(selectedCapacityRow(capacityRows([built],"other-workload"),built.key),undefined)
  assert.equal(selectedCapacityRow([built,next],built.key),built)
  assert.deepEqual(capacityRows([built],""),[built])
  const path=capacityPath("uid with / space","key+&")
  assert.equal(path.split("?")[0],"/scheduler/capacity")
  assert.deepEqual(Object.fromEntries(new URLSearchParams(path.split("?")[1])),{workload:"uid with / space",row:"key+&"})
  assert.equal(capacityPath(),"/scheduler/capacity")
  assert.equal(new URLSearchParams(capacityPath(input.workloadUID).split("?")[1]).has("row"),false)
})

test("capacity missing kappa is not zero, pending and previous alone need polling",()=>{
  assert.equal(capacityRate(undefined),"용량 값이 없어요")
  assert.equal(capacityRate(0),"0 req/s")
  assert.equal(capacityStatus("Built"),"용량 계산이 끝났어요")
  assert.equal(capacityStatus("CapacityPending"),"용량을 계산하고 있어요")
  assert.equal(capacityStatus("CapacityRejected"),"이 조건의 용량을 사용할 수 없어요")
  assert.equal(capacityStatus("Unknown"),"용량 계산 상태를 확인할 수 없어요")
  assert.equal(capacityPolling([]),false)
  assert.equal(capacityPolling([built]),false)
  assert.equal(capacityPolling([{...built,previous:true}]),true)
  assert.equal(capacityPolling([{...built,status:"CapacityPending",kappa:undefined,builtAt:"0001-01-01T00:00:00Z"}]),true)
  assert.equal(capacityPolling([{...built,status:"CapacityRejected",reason:"NonMonotone",kappa:undefined}]),false)
  assert.equal(capacityReason("MissingDispatchOccupancy"),"분배 점유시간 근거가 없어요")
  assert.equal(capacityReason("NonMonotone"),"요청률을 높였는데 다시 통과한 구간이 있어 용량을 정하지 않았어요")
  assert.equal(capacityReason(built.reason),"가장 짧은 배치 실행에도 완료 여유를 더하면 목표 시간을 넘어요")
})

test("comparison uses eight applied top-level settings with exact wire integers",()=>{
  const comparison=capacityComparison(input,{...input,capacityWindowNanos:9007199254740992n,capacityGridSteps:11,capacityBisectionWidth:0.02,capacityTailSamples:21})
  assert.equal(comparison.length,8)
  assert.deepEqual(comparison.filter(field=>!field.same).map(field=>field.key),["capacityWindowNanos","capacityGridSteps","capacityBisectionWidth","capacityTailSamples"])
  assert.equal(comparison.find(field=>field.key==="capacityWindowNanos")?.used,9007199254740993n)
  assert.equal(capacityComparison(input,{...input,capacityWindows:5n}).find(field=>field.key==="capacityWindows")?.same,true)
})

test("wire fixture retains grid execution order, early termination and missing latency sentinel",()=>{
  const response=parseSchedulerJSON(stringifySchedulerJSON({rows:[{...built,grid:[
    {rate:100,requests:500,passed:1,met:false,percentileMS:[-1,12.5]},
    {rate:50,requests:250,passed:4,met:true,percentileMS:[2,3,4,5]},
  ]}]})) as {rows:CapacityRow[]}
  const row=response.rows[0]
  assert.equal(row.input.capacityWindowNanos,9007199254740993n)
  assert.deepEqual(row.grid?.map(judgment=>judgment.rate),[100,50])
  assert.equal(row.grid?.[0].requests,500)
  assert.equal(row.grid?.[0].percentileMS?.length,2)
  assert.equal(capacityPercentile(-1),"이 창의 지연값 없음(미충족)")
  assert.equal(capacityPercentile(0),"0 ms")
  assert.equal(capacityPercentile(12.5),"12.5 ms")
  assert.equal(row.bisection,undefined)
  assert.equal(built.grid,undefined)
})
