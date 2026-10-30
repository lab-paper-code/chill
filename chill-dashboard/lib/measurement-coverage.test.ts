import assert from "node:assert/strict"
import test from "node:test"
import { batchStatisticsText, coverageCells, coverageFailureCounts, coverageOverview, coverageReady, coverageCellSummary, coverageRequestHref, currentRunCondition, exactRunTrial, ownedRuns, planVariant, profileForPlan, profileVariant, relatedPlans, resolveMeasurementPrefill, runAttempt, runEvidenceState, runGroupKey, serverCompatibility, staticCompatibility, statisticsMissing } from "./measurement-coverage.ts"
import { safeMeasurementReturn } from "./measurements.ts"
import type { CoverageModel } from "./measurement-coverage.ts"
import type { BatchStatistics, CurvePlanSpec, InventoryClass, InventoryView, MeasurementPlan, ModelCompatibility, ProfilingRun, ProfilesView, SchedulerProfile } from "./read-types.ts"

const model:CoverageModel={name:"model & one",uid:"model-uid",resourceVersion:"1",spec:{artifacts:[{name:"onnx",format:"onnx",source:{image:"image",architecture:"arm64",files:["model.onnx"]}}],executionPaths:[{name:"cpu",artifact:"onnx",runtime:{family:"firstfloor",backend:"ort-cpu"}}]}}
const cls:InventoryClass={name:"class",uid:"class-uid",resourceVersion:"1",nodeSelector:{},architecture:"arm64",memory:"8Gi",accelerator:"none",powerModes:[{name:"MAXN",actuator:{kind:"Fixed"}},{name:"15W",actuator:{kind:"Fixed"}}],nodes:[{name:"node",uid:"node-uid",resourceVersion:"1",ready:"True",schedulingDisabled:false}]}
const inventory:InventoryView={readAt:"2026-10-30T00:00:00Z",deviceClassesResourceVersion:"1",nodesResourceVersion:"1",modelSpecsResourceVersion:"1",classes:[cls],models:[model]}
const plan:MeasurementPlan={metadata:{namespace:"ns",name:"195",uid:"plan-uid",resourceVersion:"1",creationTimestamp:"2026-10-29T00:00:00Z"},spec:{state:{candidate:{modelSpec:model.name,deviceClass:cls.name,executionPath:"cpu",powerMode:"MAXN",preprocessor:"pre"},targetNode:"node",cpuRequest:"1"},activeProtocol:{dataset:"data",durationSeconds:10},idleProtocol:{durationSeconds:10},cpuQuota:"1",trialsPerBatch:3,retryLimit:1,batchSizes:[1]},status:{phase:"Complete",result:{store:"s",key:"result"}}}
const condition:CurvePlanSpec={name:"curve",logicalModel:model.name,deviceClass:cls.name,nodeUID:"node-uid",runtimeFamily:"firstfloor",backend:"ort-cpu",runtimeImage:"runtime",powerMode:"MAXN",cpu:{policy:"p",policyVersion:"1",request:"1",limit:"1",ortIntraOpThreads:1,ortInterOpThreads:1,exclusivityClaimed:false},metricSource:{sourceKind:"prom",nodeName:"node",endpoint:"http://meter",powerMetric:"w"},activeProtocol:{workloadMode:"active",dataset:{name:"data",uid:"data-uid"},warmupIterations:1,settlingDurationSeconds:0,durationSeconds:10,metricIntervalMilliseconds:100,metricTimeoutMilliseconds:50,metricDurationSeconds:10,metricObserverImage:"meter",workloadObserverImage:"observer",workloadSchemaVersion:"1"},idleProtocol:{workloadMode:"idle-model-loaded",warmupIterations:0,settlingDurationSeconds:2,durationSeconds:10,metricIntervalMilliseconds:100,metricTimeoutMilliseconds:50,metricDurationSeconds:10,metricObserverImage:"meter",workloadObserverImage:"observer",workloadSchemaVersion:"1"},conversion:{},artifact:{executionPath:"cpu",name:"onnx",files:["model.onnx"],image:"image"},preprocessor:{name:"pre",image:"preimage",architecture:"arm64",path:"/pre",inputs:[],outputs:[]},plannedBatchSizes:[1]}
const profile:SchedulerProfile={id:"opaque-id-not-a-request",reference:{store:"s",key:"result",size:4},evidence:[],nodeName:"node",condition,batches:[{batch:1,durationNanos:[1],energyPerInputJoules:0.09,memoryBytes:0}],confidenceLevel:0.95,idleBaselineWatts:1,measuredAt:"2026-10-30T00:00:00Z",metricSource:{kind:"prom",endpoint:"http://meter",metric:"w"}}
const catalog:ProfilesView={configVersion:"1",profiles:[profile],insufficiencies:[],preparations:null}
const run:ProfilingRun={namespace:"ns",name:"195b-active",uid:"run-uid",resourceVersion:"1",generation:2,creationTimestamp:"2026-10-30T00:00:00Z",spec:{candidate:plan.spec.state.candidate,targetNode:"node",cpuRequest:"1",measurement:{batch:1,cpuQuota:"1",cpuAffinity:[1],dataset:"data",workloadMode:"active",durationSeconds:10},metricObservation:{durationSeconds:10}},status:{conditions:[{type:"ExecutionComplete",status:"True",observedGeneration:2},{type:"EvidenceVerified",status:"True",observedGeneration:2}],evidence:{store:"s",key:"run"}}}

test("static declarations prove mismatches but never promise runtime support",()=>{
  const path=model.spec.executionPaths[0]
  assert.equal(staticCompatibility(model,path,cls).state,"candidate")
  assert.equal(staticCompatibility(model,path,{...cls,architecture:"amd64"}).state,"unsupported")
  assert.equal(staticCompatibility(model,{...path,runtime:{family:"firstfloor",backend:"ort-cuda"}},cls).state,"candidate")
  assert.equal(staticCompatibility(model,{...path,runtime:{family:"firstfloor",backend:"ort-cuda"}},{...cls,accelerator:"future-cuda-device"}).state,"candidate")
  assert.equal(staticCompatibility(model,{...path,artifact:"missing"},cls).state,"unknown")
})
test("incomplete or failed ranges are never unmeasured and Complete does not imply catalog quality",()=>{
  const base={inventory,plans:[],catalog:{...catalog,profiles:[]},complete:true,runsComplete:true}
  assert.equal(coverageCells(base)[0].state,"unmeasured")
  assert.equal(coverageCells({...base,complete:false})[0].state,"unknown")
  assert.equal(coverageCells({...base,runsComplete:false})[0].state,"unknown")
  assert.equal(coverageCells({...base,plans:[plan]})[0].state,"warning")
  assert.equal(coverageCells({...base,plans:[plan],catalog})[0].state,"complete")
  assert.equal(coverageCells({...base,inventory:{...inventory,classes:[{...cls,architecture:"amd64"}]},catalog})[0].state,"warning")
})

test("opening a cell never changes its neighbours or the overview counts",()=>{
  const base={inventory,plans:[],catalog:{...catalog,profiles:[]},complete:true,runsComplete:true}
  const before=coverageCells(base)
  const selectedSources={...base,compatibility:[{node:"node",selection:{modelSpec:model.name,executionPath:"cpu",deviceClass:cls.name,node:"node"},error:"HTTP 404"}]}
  const after=coverageCells(selectedSources)
  assert.deepEqual(after,before)
})

test("overview counts only eligible cells, preserves detail keys and groups model by device",()=>{
  const cells=coverageCells({inventory:{...inventory,classes:[cls,{...cls,name:"other",uid:"other",architecture:"amd64"}]},plans:[plan],catalog,complete:true,runsComplete:true})
  const groups=coverageOverview(cells)
  assert.equal(groups.length,2)
  assert.deepEqual(groups.map(group=>[group.complete,group.eligible,group.failed]),[[1,2,0],[0,0,0]])
  assert.deepEqual(groups.flatMap(group=>group.cells.map(cell=>cell.key)),cells.map(cell=>cell.key))
  assert.match(coverageCellSummary(cells[0]),/0.09 J\/건/)
  assert.equal(coverageCellSummary(cells[1]),"")
  const warned=coverageCells({inventory,plans:[plan],catalog,complete:true,runsComplete:true,evidenceIssues:{[cells[0].key]:"보존 근거 없음"}})
  assert.equal(warned[0].state,"warning")
  assert.equal(warned[1].state,"unmeasured")
})

test("coverage denominator uses matrix runtime verdicts, not architecture-only candidates",()=>{
  const selection={modelSpec:model.name,executionPath:"cpu",deviceClass:cls.name,node:"node"}
  const row:ModelCompatibility={readAt:inventory.readAt,selection,inputs:{modelSpec:{name:model.name,uid:model.uid,resourceVersion:"1"},deviceClass:{name:cls.name,uid:cls.uid,resourceVersion:"1"},node:{name:"node",uid:"node-uid",resourceVersion:"1"},runtimeDeclaration:null},verdict:"Incompatible",reasons:[{kind:"Unsupported",code:"BackendUnsupported",message:"NPU backend unsupported"}]}
  const source={inventory,plans:[],catalog:{...catalog,profiles:[]},complete:true,runsComplete:true}
  const excluded=coverageCells({...source,matrix:[row]})
  assert.equal(excluded[0].compatibility.state,"unsupported")
  assert.equal(coverageOverview(excluded)[0].eligible,0)
  const supported=coverageCells({...source,matrix:[{...row,verdict:"Compatible",reasons:[]}]})
  assert.equal(coverageOverview(supported)[0].eligible,2)
  const unknown=coverageCells({...source,matrix:[{...row,inputs:{...row.inputs,modelSpec:{...row.inputs.modelSpec,uid:"old"}}}]})
  assert.equal(unknown[0].compatibility.state,"unknown")
  assert.equal(coverageOverview(unknown)[0].eligible,0)
  const declaredMismatch=coverageCells({...source,inventory:{...inventory,classes:[{...cls,architecture:"amd64"}]},matrix:[]})
  assert.equal(declaredMismatch[0].state,"unsupported")
  assert.equal(coverageOverview(declaredMismatch)[0].eligible,0)
})

test("missing requested batches and other Nodes are partial coverage, not numeric warnings",()=>{
  const cells=coverageCells({inventory,plans:[{...plan,spec:{...plan.spec,batchSizes:[1,2]}}],catalog,complete:true,runsComplete:true})
  assert.equal(cells[0].state,"partial")
  assert.match(cells[0].reason,/배치/)
  assert.match(coverageCellSummary(cells[0]),/최저 @배치 1/)
  assert.match(coverageCellSummary(cells[0]),/반복 3회/)
  assert.match(coverageCellSummary(cells[0]),/CPU 1/)
})

test("loading is complete only after every entire range has settled successfully",()=>{
  const done={data:{},error:null,isFetching:false}
  assert.equal(coverageReady([done,done,done,done]),true)
  for(const pending of [{...done,data:undefined},{...done,error:new Error("403")},{...done,isFetching:true}]) assert.equal(coverageReady([done,done,done,pending]),false)
})
test("a newer failure retains usable evidence with warning; earlier same-request failure is history",()=>{
  const failed={...plan,metadata:{...plan.metadata,name:"failed",uid:"failed-uid",creationTimestamp:"2026-10-30T01:00:00Z"},status:{phase:"Failed"}}
  const cells=coverageCells({inventory,plans:[plan,failed],catalog,complete:true,runsComplete:true})
  assert.equal(cells[0].state,"warning");assert.equal(cells[0].profiles.length,1)
  assert.equal(cells[1].state,"unmeasured")
  const prior={...failed,metadata:{...failed.metadata,creationTimestamp:"2026-10-28T00:00:00Z"}}
  assert.equal(coverageCells({inventory,plans:[plan,prior],catalog,complete:true,runsComplete:true})[0].state,"complete")
  assert.equal(coverageCells({inventory,plans:[plan,{...prior,spec:{...prior.spec,cpuQuota:"2"}}],catalog,complete:true,runsComplete:true})[0].state,"warning")
})
test("Node, mode and frozen conditions stay separate and exact source references do not parse IDs",()=>{
  const cells=coverageCells({inventory:{...inventory,classes:[{...cls,nodes:[...cls.nodes,{...cls.nodes[0],name:"node2",uid:"node2-uid"}]}]},plans:[plan],catalog,complete:true,runsComplete:true})
  assert.equal(cells[0].state,"partial");assert.equal(cells[0].completeNodes,1);assert.equal(cells[0].nodes,2)
  assert.equal(cells[1].profiles.length,0)
  assert.equal(profileForPlan(plan,[profile]).length,1)
  assert.equal(profileForPlan({...plan,status:{phase:"Complete",result:{store:"s",key:"other"}}},[profile]).length,0)
  assert.equal(profileVariant({...profile,condition:{...condition,name:"renamed",plannedBatchSizes:[2]}}),profileVariant(profile))
  assert.notEqual(profileVariant({...profile,condition:{...condition,runtimeImage:"new-image"}}),profileVariant(profile))
  assert.notEqual(planVariant({...plan.spec,cpuAffinity:[2]}),planVariant(plan.spec))
})
test("run groups retain mode, preprocessing, dataset, CPU affinity and protocol without invented attempts",()=>{
  assert.notEqual(runGroupKey(run),runGroupKey({...run,spec:{...run.spec,candidate:{...plan.spec.state.candidate,powerMode:"15W"}}}))
  for(const measurement of [{dataset:"other"},{cpuQuota:"2"},{cpuAffinity:[2]},{durationSeconds:11}]) assert.notEqual(runGroupKey(run),runGroupKey({...run,spec:{...run.spec,measurement:{...(run.spec.measurement as object),...measurement}}}))
  assert.equal(runGroupKey(run),runGroupKey({...run,spec:{...run.spec,measurement:{...(run.spec.measurement as object),batch:8}}}))
  const result={completedTrials:[{run:{namespace:"ns",name:"195-active",uid:"different"},attempt:7}],excludedTrials:[{run:{namespace:run.namespace,name:run.name,uid:run.uid},attempt:4}]}
  assert.equal(exactRunTrial(run,result)?.trial.attempt,4);assert.equal(exactRunTrial(run,result)?.excluded,true)
  assert.equal(exactRunTrial({...run,name:"195-active"},result),undefined)
  assert.equal(exactRunTrial(run,null),undefined)
})
test("current-generation Run evidence and real numeric anomalies update coverage",()=>{
  assert.equal(runEvidenceState(run),"complete")
  const stale={...run,generation:3}
  assert.equal(currentRunCondition(stale,"ExecutionComplete"),undefined)
  assert.equal(runEvidenceState(stale),"unknown")
  assert.equal(runEvidenceState({...run,status:{...run.status,evidence:undefined}}),"warning")
  assert.equal(coverageCells({inventory,plans:[plan],catalog:{...catalog,profiles:[{...profile,batches:[{...profile.batches![0],energyPerInputJoules:NaN}]}]},complete:true,runsComplete:true})[0].state,"warning")
})
test("prefill decodes exactly once, rejects duplicate/deleted references, and does not invent multi-Node selection",()=>{
  const cell=coverageCells({inventory,plans:[],catalog,complete:true,runsComplete:true})[0]
  const href=coverageRequestHref(cell,"ns","node","/measurements/coverage?model=a%26b")
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
test("return links cannot leave allowed same-origin areas and remeasurement does not claim frozen identity",()=>{
  for(const url of ["//evil.test","https://evil.test","/\\evil","/measurements\n/evil","/configuration","/measurements/%2e%2e/configuration"]) assert.equal(safeMeasurementReturn(url),"/measurements")
  assert.equal(safeMeasurementReturn("/measurements/coverage?model=a%20b"),"/measurements/coverage?model=a%20b")
  const related=relatedPlans(plan,[{...plan,metadata:{...plan.metadata,uid:"next",name:"195b"},status:{phase:"Complete",result:{store:"s",key:"next"}}}])[0]
  assert.equal(related.sameRequestedCondition,true);assert.equal(related.frozenIdentityConfirmed,false)
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
  assert.equal(serverCompatibility(fallback,[]),fallback)
  const base={inventory,plans:[],catalog:{...catalog,profiles:[]},complete:true,runsComplete:true,compatibility:[{node:"node",data}]}
  assert.equal(coverageCells(base)[0].state,"unmeasured")
  assert.equal(coverageCells({...base,plans:[{...plan,status:{phase:"Failed"}}]})[0].state,"failed")
  const multi={...inventory,classes:[{...cls,nodes:[...cls.nodes,{...cls.nodes[0],name:"node2",uid:"node2"}]}]}
  assert.equal(coverageCells({...base,inventory:multi})[0].compatibility.reason,fallback.reason)
})

test("owner matching requires namespace and controller API kind UID, not labels or names",()=>{
  const owner={apiVersion:"edge.dacs.io/v1alpha1",kind:"BatchEnergyOptimization",name:"same-name",uid:plan.metadata.uid,controller:true}
  const owned={...run,ownerReferences:[owner],labels:{"edge.dacs.io/attempt":"3"}}
  assert.deepEqual(ownedRuns([owned],plan.metadata),[owned])
  for(const patch of [{uid:"recreated"},{controller:false},{apiVersion:"edge.dacs.io/v2"},{kind:"Other"}]) assert.deepEqual(ownedRuns([{...owned,ownerReferences:[{...owner,...patch}]}],plan.metadata),[])
  assert.deepEqual(ownedRuns([{...owned,namespace:"other"},{...run,labels:{"edge.dacs.io/batch-energy-optimization":plan.metadata.name}}],plan.metadata),[])
  assert.equal(runAttempt(owned),"3회차")
  for(const value of ["", "0", "-1", "1.5", "1e2", "9007199254740993"]) assert.equal(runAttempt({...run,labels:{"edge.dacs.io/attempt":value}}),"회차 기록 없음")
})

test("statistics preserve zero-width and negative CI and distinguish pooled call samples",()=>{
  const row:BatchStatistics={batch:8,incrementalWorkJoulesPerItem:0.1,activeTrialCount:3,idleTrialCount:2,confidence:{estimatorVersion:"v",method:"Student-t",level:0.9,varianceOfMean:0.01,halfWidth:0.2},latencySampleCount:101,latencyP95Milliseconds:0,sources:[]}
  assert.match(batchStatisticsText(row).ci,/90% CI \[-0.1, 0.3\]/)
  assert.match(batchStatisticsText(row).p95,/0 ms\/호출 · 호출 표본 N 101/)
  assert.match(batchStatisticsText(row).trials,/active 3 \/ 공유 idle 2 Run/)
  assert.match(batchStatisticsText({...row,confidence:{...row.confidence,varianceOfMean:0,halfWidth:0}}).ci,/\[0.1, 0.1\]/)
  assert.match(batchStatisticsText({...row,confidence:{...row.confidence,varianceOfMean:undefined,halfWidth:0}}).ci,/\[0.1, 0.1\]/)
  assert.match(batchStatisticsText({...row,latencySampleCount:0}).p95,/품질 경고/)
  assert.match(batchStatisticsText({...row,latencyP95Milliseconds:-1}).p95,/품질 경고/)
  assert.match(batchStatisticsText({...row,confidence:{...row.confidence,halfWidth:-1}}).ci,/품질 경고/)
  assert.equal(statisticsMissing("RetainedEvidenceMissing"),"통계 원근거 기록 없음")
  assert.equal(statisticsMissing("StatisticsNotRecorded"),"CI·통합 p95 기록 없음")
  assert.equal(statisticsMissing("NoPublishedResult"),"게시된 결과 없음")
  assert.equal(statisticsMissing(undefined),"CI 미제공 · p95 미제공")
})

test("failed cells and Failed request history keep different explicit units",()=>{
  const failed={...plan,metadata:{...plan.metadata,uid:"earlier",creationTimestamp:"2026-10-28T00:00:00Z"},status:{phase:"Failed"}}
  const other={...failed,metadata:{...failed.metadata,uid:"outside"},spec:{...failed.spec,state:{...failed.spec.state,candidate:{...failed.spec.state.candidate,powerMode:"15W"}}}}
  const cells=coverageCells({inventory,plans:[plan,failed,other],catalog,complete:true,runsComplete:true})
  assert.deepEqual(coverageFailureCounts(cells,[plan,failed,other]),{cells:1,requests:2,totalRequests:2})
  assert.deepEqual(coverageFailureCounts(cells.filter(cell=>cell.mode==="MAXN"),[plan,failed,other]),{cells:0,requests:1,totalRequests:2})
})
