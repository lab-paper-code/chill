import assert from "node:assert/strict"
import test from "node:test"
import { resolveMeasurementPrefill, runAttempt, runGroupKey, serverCompatibility, staticCompatibility } from "./measurement-coverage.ts"
import { safeMeasurementReturn } from "./measurements.ts"
import type { CoverageModel } from "./measurement-coverage.ts"
import type { InventoryClass, InventoryView, MeasurementPlan, ModelCompatibility, ProfilingRun } from "./read-types.ts"

const model:CoverageModel={name:"model & one",uid:"model-uid",resourceVersion:"1",spec:{artifacts:[{name:"onnx",format:"onnx",source:{image:"image",architecture:"arm64",files:["model.onnx"]}}],executionPaths:[{name:"cpu",artifact:"onnx",runtime:{family:"firstfloor",backend:"ort-cpu"}}]}}
const cls:InventoryClass={name:"class",uid:"class-uid",resourceVersion:"1",nodeSelector:{},architecture:"arm64",memory:"8Gi",accelerator:"none",powerModes:[{name:"MAXN",actuator:{kind:"Fixed"}},{name:"15W",actuator:{kind:"Fixed"}}],nodes:[{name:"node",uid:"node-uid",resourceVersion:"1",ready:"True",schedulingDisabled:false}]}
const inventory:InventoryView={readAt:"2026-10-30T00:00:00Z",deviceClassesResourceVersion:"1",nodesResourceVersion:"1",modelSpecsResourceVersion:"1",classes:[cls],models:[model]}
const plan:MeasurementPlan={metadata:{namespace:"ns",name:"195",uid:"plan-uid",resourceVersion:"1",creationTimestamp:"2026-10-29T00:00:00Z"},spec:{state:{candidate:{modelSpec:model.name,deviceClass:cls.name,executionPath:"cpu",powerMode:"MAXN",preprocessor:"pre"},targetNode:"node",cpuRequest:"1"},activeProtocol:{dataset:"data",durationSeconds:10},idleProtocol:{durationSeconds:10},cpuQuota:"1",trialsPerBatch:3,retryLimit:1,batchSizes:[1]},status:{phase:"Complete",result:{store:"s",key:"result"}}}
const run:ProfilingRun={namespace:"ns",name:"195b-active",uid:"run-uid",resourceVersion:"1",generation:2,creationTimestamp:"2026-10-30T00:00:00Z",spec:{candidate:plan.spec.state.candidate,targetNode:"node",cpuRequest:"1",measurement:{batch:1,cpuQuota:"1",cpuAffinity:[1],dataset:"data",workloadMode:"active",durationSeconds:10},metricObservation:{durationSeconds:10}},status:{conditions:[{type:"ExecutionComplete",status:"True",observedGeneration:2},{type:"EvidenceVerified",status:"True",observedGeneration:2}],evidence:{store:"s",key:"run"}}}

test("static declarations prove mismatches but never promise runtime support",()=>{
  const path=model.spec.executionPaths[0]
  assert.equal(staticCompatibility(model,path,cls).state,"candidate")
  assert.equal(staticCompatibility(model,path,{...cls,architecture:"amd64"}).state,"unsupported")
  assert.equal(staticCompatibility(model,{...path,runtime:{family:"firstfloor",backend:"ort-cuda"}},cls).state,"candidate")
  assert.equal(staticCompatibility(model,{...path,runtime:{family:"firstfloor",backend:"ort-cuda"}},{...cls,accelerator:"future-cuda-device"}).state,"candidate")
  assert.equal(staticCompatibility(model,{...path,artifact:"missing"},cls).state,"unknown")
})
test("run groups retain mode, preprocessing, dataset, CPU affinity and protocol without invented attempts",()=>{
  assert.notEqual(runGroupKey(run),runGroupKey({...run,spec:{...run.spec,candidate:{...plan.spec.state.candidate,powerMode:"15W"}}}))
  for(const measurement of [{dataset:"other"},{cpuQuota:"2"},{cpuAffinity:[2]},{durationSeconds:11}]) assert.notEqual(runGroupKey(run),runGroupKey({...run,spec:{...run.spec,measurement:{...(run.spec.measurement as object),...measurement}}}))
  assert.equal(runGroupKey(run),runGroupKey({...run,spec:{...run.spec,measurement:{...(run.spec.measurement as object),batch:8}}}))
})
test("prefill decodes exactly once, rejects duplicate/deleted references, and does not invent multi-Node selection",()=>{
  const href=`/measurements/new?${new URLSearchParams({namespace:"ns",model:model.name,deviceClass:cls.name,node:"node",path:"cpu",powerMode:"MAXN",returnTo:"/measurements/coverage?model=a%26b"})}`
  const params=new URL(href,"http://local").searchParams,resolved=resolveMeasurementPrefill(params,"ns",inventory)
  assert.equal(resolved.values.model,model.name);assert.deepEqual(resolved.errors,[])
  params.append("model","other");assert.match(resolveMeasurementPrefill(params,"ns",inventory).errors.join(" "),/중복/)
  params.delete("model");params.set("model","deleted");assert.match(resolveMeasurementPrefill(params,"ns",inventory).errors.join(" "),/모델/)
  const valid=new URL(href,"http://local").searchParams;valid.delete("node")
  assert.equal(resolveMeasurementPrefill(valid,"ns",inventory).node,"node")
  assert.equal(resolveMeasurementPrefill(valid,"ns",{...inventory,classes:[{...cls,nodes:[...cls.nodes,{...cls.nodes[0],name:"node2",uid:"node2"}]}]}).node,"")
  valid.set("from","195");assert.match(resolveMeasurementPrefill(valid,"ns",inventory).errors.join(" "),/함께/)
  valid.delete("from");valid.set("node","alien");assert.match(resolveMeasurementPrefill(valid,"ns",inventory).errors.join(" "),/Node/)
})
test("server support is Node-scoped, independent of evidence and does not invent admission",()=>{
  const selection={modelSpec:model.name,executionPath:"cpu",deviceClass:cls.name,node:"node"}
  const data:ModelCompatibility={readAt:"2026-10-30T00:00:00Z",selection,inputs:{modelSpec:{name:model.name,uid:model.uid,resourceVersion:"1"},deviceClass:{name:cls.name,uid:cls.uid,resourceVersion:"1"},node:{name:"node",uid:"node-uid",resourceVersion:"1"},runtimeDeclaration:null},verdict:"Compatible",reasons:[]}
  const fallback=staticCompatibility(model,model.spec.executionPaths[0],cls)
  assert.match(serverCompatibility(fallback,[{node:"node",data}]).reason,/admission 보장 아님/)
  assert.equal(serverCompatibility(fallback,[{node:"node",data:{...data,verdict:"Unknown",reasons:[{kind:"Unknown",code:"RuntimeDeclarationMissing",message:"선언 없음"}]}}]).state,"unknown")
  assert.equal(serverCompatibility(fallback,[{node:"node",error:"HTTP 403"}]).state,"unknown")
  assert.match(serverCompatibility(fallback,[{node:"node",error:"HTTP 503"}]).reason,/503/)
  assert.equal(serverCompatibility(fallback,[{node:"node",data},{node:"node2",data:{...data,verdict:"Incompatible"}}]).state,"unknown")
})
test("attempts are not invented and return links stay same-origin",()=>{
  assert.equal(runAttempt(run),"회차 기록 없음")
  assert.equal(runAttempt({...run,labels:{"edge.dacs.io/attempt":"3"}}),"3회차")
  for(const value of ["", "0", "-1", "1.5", "1e2"])assert.equal(runAttempt({...run,labels:{"edge.dacs.io/attempt":value}}),"회차 기록 없음")
  for(const value of ["//evil.test","https://evil.test","/configuration"])assert.equal(safeMeasurementReturn(value),"/measurements")
})
