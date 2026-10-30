import assert from "node:assert/strict"
import test from "node:test"
import { conditionLabel, deviceLabel, coverageSelection, coveragePathVisible, coverageModes, coverageVariant, coverageRepresentative, coverageProgress, coverageBatchState, coverageRequestHref, powerModeLabel, resolveMeasurementPrefill, runAttempt, runGroupKey, serverCompatibility, staticCompatibility } from "./measurement-coverage.ts"
import { safeMeasurementReturn } from "./measurements.ts"
import type { CoverageModel } from "./measurement-coverage.ts"
import type { InventoryClass, InventoryView, MeasurementCoverageCell, MeasurementPlan, ModelCompatibility, ProfilingRun, ResultRow } from "./read-types.ts"

test("power mode labels identify measured watts rather than inventing a declared limit",()=>{
  assert.equal(powerModeLabel("MODE-1",[10,12]),"MODE-1 · 실측 10.0–12.0 W")
  assert.equal(powerModeLabel("fixed",[0]),"fixed · 실측 0.0 W")
  assert.equal(powerModeLabel("MAXN",[NaN]),"MAXN · 전력 미확인")
})

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
test("closing a directly linked cell preserves its matrix and filter context",()=>{
  const cell={id:"cell",model:{uid:"model"},deviceClass:{uid:"class"}}
  const open=coverageSelection("batches=1,2&cpu=2&variant=old",cell,true)
  assert.equal(open.get("group"),"model/class")
  assert.equal(open.get("cell"),"cell")
  assert.equal(open.has("variant"),false)
  const closed=coverageSelection("cell=cell&variant=old&batches=1,2&cpu=2",cell,false)
  assert.equal(closed.get("group"),"model/class")
  assert.equal(closed.get("batches"),"1,2")
  assert.equal(closed.get("cpu"),"2")
  assert.equal(closed.has("cell"),false)
  assert.equal(closed.has("variant"),false)
})
test("condition choices name Node CPU and dataset without dumping frozen JSON",()=>{
  assert.equal(conditionLabel({nodeUID:"node-uid",metricSource:{nodeName:"jetsono"},cpu:{limit:"4",affinityCPUs:[2,3]},activeProtocol:{dataset:{name:"images",uid:"dataset-uid"}},runtimeImage:"registry/image@sha256:long"}),"Node jetsono / CPU 4 (코어 2,3) / 데이터셋 images")
  assert.equal(conditionLabel({}),"Node 미지정 / CPU 미지정 / 데이터셋 미지정")
  assert.equal(conditionLabel({state:{targetNode:"genesis2",cpuRequest:"2"},cpuQuota:"4",cpuAffinity:[2,3],activeProtocol:{dataset:"images"}}),"Node genesis2 / CPU 4 (코어 2,3) / 데이터셋 images")
  assert.equal(deviceLabel("jetson-agx-orin-64g"),"AGX Orin 64g")
})

const cell:MeasurementCoverageCell={id:"cell",model:{name:model.name,uid:model.uid},deviceClass:{name:cls.name,uid:cls.uid},executionPath:"cpu",powerMode:"MAXN",support:{verdict:"Unknown",node:{name:"node",uid:"node-uid"},reasons:[{code:"RuntimeDeclarationMissing",message:"선언 없음"}]},assessment:{status:"unmeasured",displayStatus:"unmeasured",label:"미측정",reasons:[],execution:null,evidence:{available:false,accepted:false,reasons:[]},checklist:{criteriaMet:null,version:"result-checklist.v1",checks:[]},scope:{schedulerUsed:null,reused:false,facts:[]}},variantCount:0,variants:[],batchProgress:{basis:"explicit",target:[1,2,4,8],measured:[1,2],criteriaMet:[1],missing:[4,8],inProgress:[4],unknown:[]},representative:null,failureSummary:[],history:[]}
test("coverage hides architecture conflicts only, never declarations or measured unsupported paths",()=>{
  assert.equal(coveragePathVisible(cell),true)
  assert.equal(coveragePathVisible({...cell,support:{...cell.support,verdict:"Incompatible",reasons:[{code:"RuntimeUnsupported",message:"runtime 미지원"}]}}),true)
  assert.equal(coveragePathVisible({...cell,support:{...cell.support,reasons:[{code:"ArtifactArchitectureMismatch",message:"아키텍처 불일치"}]}}),false)
  assert.equal(coveragePathVisible(cell,inventory),true)
  assert.equal(coveragePathVisible(cell,{...inventory,classes:[{...cls,architecture:"amd64"}]}),false)
})
test("batch slots preserve separate evidence, running, missing and unknown states",()=>{
  assert.deepEqual([1,2,4,8,16].map(batch=>coverageBatchState(cell.batchProgress,batch)),["complete","measured","running","missing","outside"])
  assert.equal(coverageBatchState(null,1),"unknown")
  const first={id:"first",condition:{},assessment:{...cell.assessment,displayStatus:"usable" as const},batchProgress:cell.batchProgress!,resultRows:[],requestIds:[],runIds:[],profileIds:[]}
  const second={...first,id:"second",assessment:cell.assessment,batchProgress:{...first.batchProgress,measured:[8],missing:[1,2,4]}}
  const multiple={...cell,batchProgress:null,variants:[first,second],representative:{variantId:"first",batch:1,incrementalJoulesPerItem:1,p95Milliseconds:2}}
  assert.equal(coverageProgress(multiple),second.batchProgress)
  assert.equal(coverageVariant(multiple,"first"),first)
})
test("coverage requests work for unmeasured cells without exact variants and preserve return filters",()=>{
  const href=new URL(coverageRequestHref(cell,"ns","/measurements?cell=cell&status=unmeasured"),"http://local")
  assert.equal(href.searchParams.get("model"),model.name)
  assert.equal(href.searchParams.get("node"),"node")
  assert.equal(href.searchParams.get("batches"),"4,8")
  assert.equal(href.searchParams.has("variantId"),false)
  assert.equal(href.searchParams.get("returnTo"),"/measurements?cell=cell&status=unmeasured")
  assert.equal(new URL(coverageRequestHref({...cell,support:{...cell.support,node:null}},"ns","/measurements"),"http://local").searchParams.has("node"),false)
  assert.equal(new URL(coverageRequestHref({...cell,batchProgress:null},"ns","/measurements",undefined,[2,8]),"http://local").searchParams.get("batches"),"2,8")
  const variant={id:"variant",condition:{state:{targetNode:"actual-node"}},assessment:cell.assessment,batchProgress:cell.batchProgress!,resultRows:[],requestIds:[],runIds:[],profileIds:[]}
  assert.equal(new URL(coverageRequestHref({...cell,variants:[variant]},"ns","/measurements","variant"),"http://local").searchParams.get("node"),"actual-node")
})
function resultRow(watts:number):ResultRow {
  return {id:"row",rowKind:"direct",profileId:"profile",sourceProfileId:null,batch:1,condition:{},p95Milliseconds:2,latencySampleCount:10,idleWatts:0,activeWatts:watts,totalJoulesPerItem:1,incrementalJoulesPerItem:1,confidence:null,activeTrialCount:3,idleTrialCount:3,completedItems:10,completedCalls:10,powerSource:null,assessment:cell.assessment,sources:[],missingReasons:[],reuse:null}
}
test("power modes put measured zero watts first and unknown watts last",()=>{
  const measured=(mode:string,watts:number):MeasurementCoverageCell=>({...cell,powerMode:mode,variants:[{id:mode,condition:{},assessment:cell.assessment,batchProgress:cell.batchProgress!,resultRows:[resultRow(watts)],requestIds:[],runIds:[],profileIds:[]}]})
  assert.deepEqual(coverageModes([measured("high",30),{...cell,powerMode:"unknown"},measured("idle",0),measured("low",12)]),["idle","low","high","unknown"])
  assert.deepEqual(coverageRepresentative(measured("low",12)),{variantId:"low",batch:1,incrementalJoulesPerItem:1,p95Milliseconds:2})
})
