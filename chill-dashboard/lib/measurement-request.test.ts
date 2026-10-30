import assert from "node:assert/strict"
import test from "node:test"
import { requestBatches, requestDefaults } from "./measurement-request.ts"
import type { MeasurementPlan, ProfilesView } from "./read-types.ts"

const condition={model:"model & one",deviceClass:"device",node:"node",path:"cuda",powerMode:"MAXN"}
const plan:MeasurementPlan={metadata:{name:"source",namespace:"ns",uid:"uid",resourceVersion:"1",creationTimestamp:"2026-10-30T00:00:00Z"},spec:{state:{candidate:{modelSpec:condition.model,deviceClass:condition.deviceClass,preprocessor:"pre",executionPath:condition.path,powerMode:condition.powerMode},targetNode:condition.node,cpuRequest:"2"},activeProtocol:{warmupIterations:5,durationSeconds:10,dataset:"data",metricObservation:{intervalMilliseconds:1000,timeoutMilliseconds:500,durationSeconds:11}},idleProtocol:{settlingDurationSeconds:2,durationSeconds:10,metricObservation:{intervalMilliseconds:1000,timeoutMilliseconds:500,durationSeconds:11}},trialsPerBatch:3,retryLimit:1}}

test("request query batches decode, deduplicate and sort without accepting malformed values",()=>{
  const query=new URLSearchParams({...condition,batches:"8,1,4,1"})
  assert.equal(query.get("model"),condition.model)
  assert.deepEqual(requestBatches(query),{values:[1,4,8],error:""})
  for(const raw of ["", "0", "-1", "1.5", "1e2", "1,", "2147483648", "1&batches=2"]){const result=requestBatches(new URLSearchParams(`batches=${raw}`));assert.notEqual(result.error,"");assert.deepEqual(result.values,[])}
  assert.deepEqual(requestBatches(new URLSearchParams()),{values:[],error:""})
})

test("default protocol comes from newest server request with the same condition, never another mode or Node",()=>{
  const newer={...plan,metadata:{...plan.metadata,name:"newer",creationTimestamp:"2026-10-31T00:00:00Z"},spec:{...plan.spec,retryLimit:2}}
  const selected=requestDefaults(condition,[plan,newer],[])
  assert.deepEqual(selected?.spec,{...newer.spec,cpuAffinity:undefined})
  assert.match(selected?.source??"",/newer/)
  for(const patch of [{model:"other"},{deviceClass:"other"},{node:"other"},{path:"cpu"},{powerMode:"15W"}])assert.equal(requestDefaults({...condition,...patch},[plan],[]),null)
  assert.equal(requestDefaults({...condition,node:""},[plan],[])?.spec.state.targetNode,"node")
  assert.equal(plan.spec.activeProtocol.dataset,"data")
})

test("catalog fallback retains measured protocol and identifies unprovided retry policy",()=>{
  const profiles=[{id:"profile",nodeName:"node",measuredAt:"2026-10-30T00:00:00Z",condition:{logicalModel:condition.model,deviceClass:condition.deviceClass,powerMode:condition.powerMode,artifact:{executionPath:condition.path},preprocessor:{name:"pre"},cpu:{request:"2",limit:"2",affinityCPUs:[1]},activeProtocol:{warmupIterations:5,durationSeconds:10,dataset:{name:"data"},metricIntervalMilliseconds:1000,metricTimeoutMilliseconds:500,metricDurationSeconds:11},idleProtocol:{settlingDurationSeconds:2,durationSeconds:10,metricIntervalMilliseconds:1000,metricTimeoutMilliseconds:500,metricDurationSeconds:11}}}] as unknown as ProfilesView["profiles"]
  const selected=requestDefaults(condition,[],profiles)
  assert.deepEqual(selected?.spec.activeProtocol,plan.spec.activeProtocol)
  assert.deepEqual(selected?.spec.idleProtocol,plan.spec.idleProtocol)
  assert.equal(Number.isNaN(selected?.spec.retryLimit),true)
  assert.match(selected?.source??"",/profile/)
  assert.equal(requestDefaults({...condition,path:"cpu"},[],profiles),null)
})

test("unmeasured targets reuse successful protocol without copying another device's CPU, batches or affinity",()=>{
  const success={...plan,status:{phase:"Complete"},spec:{...plan.spec,cpuQuota:"8",cpuAffinity:[5,6],batchSizes:[16]}}
  const otherTarget={...condition,deviceClass:"arm-board",node:"arm-node",path:"arm-cpu",powerMode:"15W"}
  const defaults=requestDefaults(otherTarget,[success],[])
  assert.equal(defaults?.exact,false)
  assert.match(defaults?.source??"",/source.*device.*같은 모델/)
  assert.deepEqual(defaults?.spec.activeProtocol,plan.spec.activeProtocol)
  assert.equal(defaults?.spec.cpuQuota,undefined)
  assert.equal(defaults?.spec.cpuAffinity,undefined)
  assert.equal(defaults?.spec.state.cpuRequest,undefined)
  assert.equal(defaults?.spec.batchSizes,undefined)
  assert.equal(defaults?.spec.state.targetNode,"")
  const pathOnly=requestDefaults({...condition,model:"new-model"},[success],[])
  assert.match(pathOnly?.source??"",/같은 실행 경로/)
  assert.equal(requestDefaults({...otherTarget,model:"new-model"},[success],[]),null)
  assert.equal(requestDefaults(otherTarget,[{...success,status:{phase:"Failed"}}],[]),null)
})
