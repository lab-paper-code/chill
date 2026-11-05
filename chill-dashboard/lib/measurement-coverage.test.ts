import assert from "node:assert/strict"
import test from "node:test"
import { coverageQuality, coverageQualityReasons, coverageMeasurementStatus, coverageFailureHref, coverageProfileQuery, coverageFilterVisible, measurementAge, conditionLabel, deviceLabel, coverageSelection, coveragePathVisible, coverageModes, coverageVariant, coverageRepresentative, coverageProgress, coverageBatchState, coverageRequestHref, powerModeLabel, resolveMeasurementPrefill, runAttempt, runGroupKey, serverCompatibility, staticCompatibility } from "./measurement-coverage.ts"
import { safeMeasurementReturn } from "./measurements.ts"
import type { CoverageModel } from "./measurement-coverage.ts"
import type { InventoryClass, InventoryView, MeasurementCoverageCell, MeasurementPlan, ModelCompatibility, ProfilingRun, ResultRow } from "./read-types.ts"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { runInNewContext } from "node:vm"
import { createElement, type ComponentType, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as coverageHelpers from "./measurement-coverage.ts"
import * as assessmentHelpers from "./measurement-assessment.ts"
import * as measurementHelpers from "./measurements.ts"
import * as measurementSummary from "./measurement-summary.ts"
import * as occupancyHelpers from "./dispatch-occupancy.ts"

test("power mode labels identify measured watts rather than inventing a declared limit",()=>{
  assert.equal(powerModeLabel("MODE-1",[10,12]),"MODE-1 · 실측 10.0–12.0 W")
  assert.equal(powerModeLabel("fixed",[0]),"fixed · 실측 0.0 W")
  assert.equal(powerModeLabel("MAXN",[NaN]),"MAXN · 전력 미확인")
})

const model:CoverageModel={name:"model & one",uid:"model-uid",resourceVersion:"1",spec:{artifacts:[{name:"onnx",format:"onnx",source:{image:"image",architecture:"arm64",files:["model.onnx"]}}],executionPaths:[{name:"cpu",artifact:"onnx",runtime:{family:"firstfloor",backend:"ort-cpu"}}]}}
const cls:InventoryClass={name:"class",uid:"class-uid",resourceVersion:"1",nodeSelector:{},architecture:"arm64",memory:"8Gi",accelerator:"none",powerModes:[{name:"MAXN",actuator:{kind:"Fixed"}},{name:"15W",actuator:{kind:"Fixed"}}],nodes:[{name:"node",uid:"node-uid",resourceVersion:"1",ready:"True",schedulingDisabled:false}]}
const inventory:InventoryView={readAt:"2026-10-30T00:00:00Z",deviceClassesResourceVersion:"1",nodesResourceVersion:"1",modelSpecsResourceVersion:"1",classes:[cls],models:[model]}
const plan:MeasurementPlan={metadata:{namespace:"ns",name:"195",uid:"plan-uid",resourceVersion:"1",creationTimestamp:"2026-10-29T00:00:00Z"},spec:{state:{candidate:{modelSpec:model.name,deviceClass:cls.name,executionPath:"cpu",powerMode:"MAXN",preprocessor:"pre"},targetNode:"node"},activeProtocol:{dataset:"data",durationSeconds:10},idleProtocol:{durationSeconds:10},trialsPerBatch:3},status:{phase:"Complete",result:{store:"s",key:"result"}}}
const run:ProfilingRun={namespace:"ns",name:"195b-active",uid:"run-uid",resourceVersion:"1",generation:2,creationTimestamp:"2026-10-30T00:00:00Z",spec:{candidate:plan.spec.state.candidate,targetNode:"node",measurement:{batch:1,dataset:"data",workloadMode:"active",durationSeconds:10},metricObservation:{durationSeconds:10}},status:{conditions:[{type:"ExecutionComplete",status:"True",observedGeneration:2},{type:"EvidenceVerified",status:"True",observedGeneration:2}],evidence:{store:"s",key:"run"}}}

test("static declarations prove mismatches but never promise runtime support",()=>{
  const path=model.spec.executionPaths[0]
  assert.equal(staticCompatibility(model,path,cls).state,"candidate")
  assert.equal(staticCompatibility(model,path,{...cls,architecture:"amd64"}).state,"unsupported")
  assert.equal(staticCompatibility(model,{...path,runtime:{family:"firstfloor",backend:"ort-cuda"}},cls).state,"candidate")
  assert.equal(staticCompatibility(model,{...path,runtime:{family:"firstfloor",backend:"ort-cuda"}},{...cls,accelerator:"future-cuda-device"}).state,"candidate")
  assert.equal(staticCompatibility(model,{...path,artifact:"missing"},cls).state,"unknown")
})
test("run groups retain mode, preprocessing, dataset and protocol without invented attempts",()=>{
  assert.notEqual(runGroupKey(run),runGroupKey({...run,spec:{...run.spec,candidate:{...plan.spec.state.candidate,powerMode:"15W"}}}))
  for(const measurement of [{dataset:"other"},{durationSeconds:11}]) assert.notEqual(runGroupKey(run),runGroupKey({...run,spec:{...run.spec,measurement:{...(run.spec.measurement as object),...measurement}}}))
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
  const data:ModelCompatibility={readAt:"2026-10-30T00:00:00Z",selection,inputs:{modelSpec:{name:model.name,uid:model.uid,resourceVersion:"1"},deviceClass:{name:cls.name,uid:cls.uid,resourceVersion:"1"},node:{name:"node",uid:"node-uid",resourceVersion:"1"},runtimeDeclaration:null},verdict:"Compatible",reasons:[],diagnostics:[]}
  const fallback=staticCompatibility(model,model.spec.executionPaths[0],cls)
  assert.match(serverCompatibility(fallback,[{node:"node",data}]).reason,/admission 보장 아님/)
  assert.equal(serverCompatibility(fallback,[{node:"node",data:{...data,verdict:"Unknown",reasons:[{kind:"Unknown",code:"RuntimeDeclarationMissing",message:"선언 없음"}]}}]).state,"unknown")
  assert.equal(serverCompatibility(fallback,[{node:"node",error:"HTTP 403"}]).state,"unknown")
  assert.match(serverCompatibility(fallback,[{node:"node",error:"HTTP 503"}]).reason,/503/)
  assert.equal(serverCompatibility(fallback,[{node:"node",data},{node:"node2",data:{...data,verdict:"Incompatible"}}]).state,"unknown")
  assert.equal(serverCompatibility({state:"unsupported",reason:"architecture 불일치"},[{node:"node",data:{...data,verdict:"Unknown"}}]).state,"unsupported")
})
test("attempts are not invented and return links stay same-origin",()=>{
  assert.equal(runAttempt(run),"회차 기록 없음")
  assert.equal(runAttempt({...run,labels:{"edge.dacs.io/attempt":"3"}}),"3회차")
  for(const value of ["", "0", "-1", "1.5", "1e2"])assert.equal(runAttempt({...run,labels:{"edge.dacs.io/attempt":value}}),"회차 기록 없음")
  for(const value of ["//evil.test","https://evil.test","/configuration"])assert.equal(safeMeasurementReturn(value),"/measurements")
})
test("closing a directly linked cell preserves its matrix and filter context",()=>{
  const cell={id:"cell",model:{uid:"model"},deviceClass:{uid:"class"}}
  const open=coverageSelection("batches=1,2&node=node&variant=old",cell,true)
  assert.equal(open.get("group"),"model/class")
  assert.equal(open.get("cell"),"cell")
  assert.equal(open.has("variant"),false)
  const closed=coverageSelection("cell=cell&variant=old&batches=1,2&node=node",cell,false)
  assert.equal(closed.get("group"),"model/class")
  assert.equal(closed.get("batches"),"1,2")
  assert.equal(closed.get("node"),"node")
  assert.equal(closed.has("cell"),false)
  assert.equal(closed.has("variant"),false)
})
test("condition choices name Node and dataset without dumping frozen JSON",()=>{
  assert.equal(conditionLabel({nodeUID:"node-uid",metricSource:{nodeName:"jetsono"},activeProtocol:{dataset:{name:"images",uid:"dataset-uid"}},runtimeImage:"registry/image@sha256:long"}),"Node jetsono / 데이터셋 images")
  assert.equal(conditionLabel({}),"Node 미지정 / 데이터셋 미지정")
  assert.equal(conditionLabel({state:{targetNode:"genesis2"},activeProtocol:{dataset:"images"}}),"Node genesis2 / 데이터셋 images")
  assert.equal(deviceLabel("jetson-agx-orin-64g"),"AGX Orin 64g")
})

const cell:MeasurementCoverageCell={id:"cell",model:{name:model.name,uid:model.uid},deviceClass:{name:cls.name,uid:cls.uid},executionPath:"cpu",powerMode:"MAXN",currentMeasurement:null,support:{verdict:"Unknown",node:{name:"node",uid:"node-uid"},reasons:[{code:"RuntimeDeclarationMissing",message:"선언 없음"}],diagnostics:[]},assessment:{status:"unmeasured",displayStatus:"unmeasured",label:"미측정",reasons:[],execution:null,evidence:{available:false,accepted:false,reasons:[]},checklist:{criteriaMet:null,version:"result-checklist.v1",checks:[]},scope:{schedulerUsed:null,reused:false,facts:[]}},variantCount:0,variants:[],batchProgress:{basis:"explicit",target:[1,2,4,8],measured:[1,2],criteriaMet:[1],missing:[4,8],inProgress:[4],unknown:[]},representative:null,failureSummary:[],history:[]}
test("coverage hides proven unsupported paths but retains truly unknown declarations",()=>{
  assert.equal(coveragePathVisible(cell),true)
  assert.equal(coveragePathVisible({...cell,support:{...cell.support,verdict:"Incompatible",reasons:[{code:"RuntimeUnsupported",message:"runtime 미지원"}]}}),false)
  assert.equal(coveragePathVisible({...cell,support:{...cell.support,reasons:[{code:"ArtifactArchitectureMismatch",message:"아키텍처 불일치"}]}}),false)
  assert.equal(coveragePathVisible(cell,inventory),true)
  assert.equal(coveragePathVisible(cell,{...inventory,classes:[{...cls,architecture:"amd64"}]}),false)
})
test("batch slots preserve separate evidence, running, missing and unknown states",()=>{
  assert.deepEqual([1,2,4,8,16].map(batch=>coverageBatchState(cell.batchProgress,batch)),["measured","measured","running","missing","outside"])
  assert.equal(coverageBatchState(null,1),"unknown")
  const first={id:"first",condition:{},assessment:{...cell.assessment,displayStatus:"usable" as const},batchProgress:cell.batchProgress!,resultRows:[],requestIds:[],runIds:[],profileIds:[]}
  const second={...first,id:"second",assessment:cell.assessment,batchProgress:{...first.batchProgress,measured:[8],missing:[1,2,4]}}
  const multiple={...cell,batchProgress:null,variants:[first,second],representative:{variantId:"first",batch:1,incrementalJoulesPerItem:1,p95Milliseconds:2}}
  assert.equal(coverageProgress(multiple),second.batchProgress)
  assert.equal(coverageVariant(multiple,"first"),first)
})

test("current condition variants use status, representative and first fallback without hiding incomplete progress",()=>{
  const complete={id:"complete",condition:{},assessment:{...cell.assessment,displayStatus:"usable" as const},batchProgress:{...cell.batchProgress!,measured:cell.batchProgress!.target},resultRows:[],requestIds:[],runIds:[],profileIds:[]}
  const incomplete={...complete,id:"incomplete",batchProgress:{...complete.batchProgress,measured:[1]}}
  const mixed={...cell,assessment:complete.assessment,batchProgress:null,variants:[incomplete,complete]}
  assert.equal(coverageVariant(mixed),incomplete)
  assert.equal(coverageProgress(mixed),incomplete.batchProgress)
  assert.equal(coverageMeasurementStatus(mixed),"insufficient")
  assert.equal(coverageVariant(mixed,"complete"),complete)
  assert.equal(coverageVariant({...mixed,assessment:cell.assessment,representative:{variantId:"complete",batch:1,incrementalJoulesPerItem:1,p95Milliseconds:2}}),complete)
  assert.equal(coverageVariant({...mixed,assessment:cell.assessment}),incomplete)
  assert.equal(coverageProgress({...mixed,variants:[incomplete]}),incomplete.batchProgress)
  assert.equal(coverageMeasurementStatus({...mixed,assessment:{...cell.assessment,displayStatus:"problem"}}),"problem")
})
test("coverage requests work for unmeasured cells without exact variants and preserve return filters",()=>{
  const href=new URL(coverageRequestHref(cell,"ns","/measurements?cell=cell&status=unmeasured"),"http://local")
  assert.equal(href.searchParams.get("model"),model.name)
  assert.equal(href.searchParams.get("node"),"node")
  assert.equal(href.searchParams.has("batches"),false)
  assert.equal(href.searchParams.has("variantId"),false)
  assert.equal(href.searchParams.get("returnTo"),"/measurements?cell=cell&status=unmeasured")
  assert.equal(new URL(coverageRequestHref({...cell,support:{...cell.support,node:null}},"ns","/measurements"),"http://local").searchParams.has("node"),false)
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

test("RKNN on Jetson is statically unsupported, not an unknown runtime declaration",()=>{
  const path={name:"rknn",artifact:"rknn",runtime:{family:"firstfloor",backend:"rknnlite"}}
  const rknn={...model,spec:{artifacts:[{name:"rknn",format:"rknn",source:{architecture:"arm64",image:"image",files:[]},derivation:{sourceArtifact:"onnx",targetPlatform:"rk3588",declaredBatchSizes:[1],conversion:{tool:"tool",version:"1"}}}],executionPaths:[path]}}
  assert.equal(staticCompatibility(rknn,path,{...cls,accelerator:"nvidia-jetson-agx-orin"}).state,"unsupported")
  assert.equal(staticCompatibility(rknn,path,{...cls,accelerator:"rk3588-npu"}).state,"candidate")
  assert.equal(coveragePathVisible({...cell,executionPath:"rknn"}, {...inventory,models:[rknn],classes:[{...cls,accelerator:"nvidia-jetson-agx-orin"}]}),false)
})


test("measurement age is based on recorded timestamp and never invents missing or future dates",()=>{
  const now=Date.parse("2026-10-31T12:00:00Z")
  assert.equal(measurementAge("2026-10-27T12:00:00Z",now),"4일 전")
  assert.equal(measurementAge("",now),"시각 미기록")
  assert.equal(measurementAge("2026-11-01T12:00:00Z",now),"시각 확인 필요")
})

test("request-only failures open the exact request rather than an empty run filter",()=>{
  const failure={requestId:"ns/request name/owner-uid",runId:"",runNamespace:"",runName:"",batch:null,code:"Other",exitCode:null,message:"실행 전 실패",relation:"unresolved"}
  const href=new URL(coverageFailureHref(failure,"fallback","/measurements?cell=one"),"http://local")
  assert.equal(href.pathname,"/measurements/requests/request%20name")
  assert.equal(href.searchParams.get("namespace"),"ns")
  assert.equal(href.searchParams.get("uid"),"owner-uid")
  assert.equal(href.searchParams.has("status"),false)
  const runHref=new URL(coverageFailureHref({...failure,runId:"ns/run/run-uid",runNamespace:"ns",runName:"run"},"fallback","/measurements"),"http://local")
  assert.equal(runHref.pathname,"/measurements/requests")
  assert.equal(runHref.searchParams.get("run"),"run")
  assert.equal(runHref.searchParams.get("uid"),"run-uid")
})
test("profile time lookup uses the server's fifty-row page contract",()=>{
  const query=coverageProfileQuery(cell)
  assert.equal(query.get("limit"),"50")
  assert.equal(query.get("model"),cell.model.name)
  assert.equal(query.get("powerMode"),"MAXN")
})

test("quality reasons come from variant checks, count each cell once and never infer an absent threshold",()=>{
  const assessment={...cell.assessment,checklist:{...cell.assessment.checklist,criteriaMet:false,checks:[{code:"FrozenPowerWindowPolicy",result:"unknown" as const,message:"raw",sourceIds:[]}]}}
  const variant={id:"v",condition:{},assessment,batchProgress:cell.batchProgress!,resultRows:[],requestIds:[],runIds:[],profileIds:[]}
  const measured={...cell,assessment:{...cell.assessment,displayStatus:"usable" as const},variants:[variant,{...variant,id:"v2"}]}
  assert.equal(coverageQuality(measured).label,"판정 불가")
  assert.deepEqual(coverageQualityReasons([measured,cell]),[{code:"FrozenPowerWindowPolicy",label:"전력 기록 기준 미확인",count:1},{code:"QualityUnavailable",label:"품질 근거 없음",count:1}])
  assert.equal(coverageQuality(cell).label,"측정 근거 없음")
  assert.equal(coverageMeasurementStatus(measured),"insufficient")
  const met={...variant,assessment:{...assessment,checklist:{...assessment.checklist,criteriaMet:true,checks:[{...assessment.checklist.checks[0],result:"pass" as const}]}}}
  assert.equal(coverageQuality({...cell,variants:[met]}).label,"기준 충족")
})

test("all current condition checks affect quality while explicit detail selection remains available",()=>{
  const pass={...cell.assessment,checklist:{...cell.assessment.checklist,criteriaMet:true,checks:[{code:"ActiveTrials",result:"pass" as const,message:"enough",sourceIds:[]}]}}
  const passing={id:"passing",condition:{},assessment:pass,batchProgress:cell.batchProgress!,resultRows:[],requestIds:[],runIds:[],profileIds:[]}
  const failing={...passing,id:"failing",assessment:{...pass,checklist:{...pass.checklist,criteriaMet:false,checks:[{code:"ActiveTrials",result:"fail" as const,message:"few",sourceIds:[]}]}}}
  const mixed={...cell,variants:[passing,failing]}
  assert.equal(coverageQuality(mixed).label,"기준 미충족")
  assert.deepEqual(coverageQualityReasons([mixed]),[{code:"ActiveTrials",label:"실행 반복 부족",count:1}])
  assert.equal(coverageVariant(mixed,"failing"),failing)
  assert.equal(coverageVariant(mixed,"passing"),passing)
  assert.equal(coverageQuality({...cell,variants:[failing]}).label,"기준 미충족")
})

test("only explicit failing checks fail quality; absent measurements and unknown checks stay distinct",()=>{
  assert.equal(coverageQuality(cell).label,"측정 근거 없음")
  const checks=[{code:"threshold",result:"unknown" as const,message:"허용 기준 미제공",sourceIds:[]}]
  assert.equal(coverageQuality({...cell,assessment:{...cell.assessment,checklist:{...cell.assessment.checklist,criteriaMet:false,checks}}}).label,"판정 불가")
  assert.equal(coverageQuality({...cell,assessment:{...cell.assessment,checklist:{...cell.assessment.checklist,criteriaMet:false,checks:[{code:"threshold",result:"fail",message:"초과",sourceIds:[]}]}}}).label,"기준 미충족")
})
test("unsupported status and reason drilldown retain the combinations hidden by default",()=>{
  const unsupported={...cell,support:{...cell.support,verdict:"Incompatible" as const,reasons:[{code:"RuntimeUnsupported",message:"미지원"}]}}
  assert.equal(coverageFilterVisible(unsupported,inventory,"unsupported"),true)
  assert.equal(coverageFilterVisible(unsupported,inventory,"unsupported","RuntimeUnsupported"),true)
  assert.equal(coverageFilterVisible(unsupported,inventory,"unsupported","Other"),false)
  assert.equal(coverageFilterVisible(unsupported,inventory,""),false)
  assert.equal(coverageMeasurementStatus(unsupported),"unsupported")
})

test("scope counts use the same measurement state as visible cells, with unsupported counted separately",()=>{
  const partial={...cell,assessment:{...cell.assessment,displayStatus:"usable" as const}}
  const complete={...partial,batchProgress:{...cell.batchProgress!,measured:cell.batchProgress!.target}}
  const failed={...complete,assessment:{...cell.assessment,displayStatus:"problem" as const}}
  const unsupported={...complete,support:{...cell.support,verdict:"Incompatible" as const}}
  const counts=coverageHelpers.coverageScopeCounts([partial,complete,failed,unsupported])
  assert.equal(counts.measuredCells,1)
  assert.equal(counts.cells,3)
  assert.equal(counts.unsupportedCells,1)
  assert.equal(counts.failedCells,1)
})


function renderCoverage(query="",extraCells:MeasurementCoverageCell[]=[],freshness:"fresh"|"stale"="fresh",isFetching=false,inspectInterval?:(interval:(query:{state:{data?:{cells:MeasurementCoverageCell[]};error:{status:number}|null}})=>number|false)=>void) {
  const source=readFileSync(new URL("../features/measurement-coverage.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports:Record<string,unknown>={}
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const unsupported={...cell,id:"unsupported",executionPath:"other",support:{...cell.support,verdict:"Incompatible",reasons:[{code:"RuntimeUnsupported",message:"미지원"}]}}
  const data={dataAsOf:"2026-10-30T03:04:05Z",freshness,cells:[cell,{...cell,id:"empty",powerMode:"15W",batchProgress:{...cell.batchProgress!,measured:[]}},unsupported,...extraCells],scope:{targetBatches:[1,2,4,8]},nextActions:[],sources:[]}
  const modules:Record<string,unknown>={
    "next/link":{__esModule:true,default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},
    "next/navigation":{useRouter:()=>({replace:()=>{}}),useSearchParams:()=>new URLSearchParams(query)},
    "@tanstack/react-query":{useQuery:({queryKey,refetchInterval}:{queryKey:string[];refetchInterval?:(query:{state:{data?:{cells:MeasurementCoverageCell[]};error:{status:number}|null}})=>number|false})=>{if(refetchInterval)inspectInterval?.(refetchInterval);return {data:queryKey[1]==="coverage-inventory"?inventory:queryKey[1]==="model-compatibility"?[]:queryKey[1]==="coverage-profile-times"?{profiles:[]}:data,error:null,isFetching,refetch:()=>{}}}},
    "@/lib/client":{useSession:()=>({config:{namespace:"ns",pollIntervalMs:5000}}),useChillQuery:(path:string)=>({data:path.startsWith("v1/observations/occupancy")?undefined:{runs:[]},error:null,isPending:path.startsWith("v1/observations/occupancy")}),isCoverageWarming:()=>false},
    "@/lib/measurement-coverage":coverageHelpers,
    "@/lib/measurement-assessment":assessmentHelpers,
    "@/lib/measurements":measurementHelpers,
    "@/lib/dispatch-occupancy":occupancyHelpers,
    "@/lib/scheduler":{},
    "@/lib/measurement-summary":measurementSummary,
    "@/components/measurement-navigation":{MeasurementNavigation:()=>null,MeasurementEvidenceChecks:()=>null},
    "@/components/screen":{Screen:({children,action}:{children?:ReactNode;action?:ReactNode})=>createElement("main",null,createElement("header",null,action),children)},
    "@/components/ui/button":{Button:({children,disabled}:{children?:ReactNode;disabled?:boolean})=>createElement("button",{disabled},children)},
    "@/components/ui/input":{Input:()=>null},
    "@/components/ui/sheet":{Sheet:container,SheetContent:container,SheetHeader:container,SheetTitle:container},
    "./measurement-coverage.css":{},
  }
  function loadShared(file:string) {
    const result={}
    const source=readFileSync(new URL(file,import.meta.url),"utf8")
    const code=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
    runInNewContext(code,{exports:result,require:(name:string)=>name in modules?modules[name]:require(name),URLSearchParams})
    return result
  }
  modules["@/hooks/use-measurement-data"]=loadShared("../hooks/use-measurement-data.ts")
  modules["@/components/measurement-filters"]=loadShared("../components/measurement-filters.tsx")
  runInNewContext(compiled,{exports,require:(name:string)=>name in modules?modules[name]:require(name),URLSearchParams})
  return renderToStaticMarkup(createElement(exports.MeasurementCoveragePage as ComponentType))
}

test("W8 scheduler provenance and observed statistics are separate facts, never legacy adoption",()=>{
  const window={start:"2026-11-03T00:00:00Z",end:"2026-11-03T00:01:00Z"}
  const statistics={median:-1000000,p25:-2000000,p75:0,iqr:2000000,p10:-3000000,p90:1000000,mean:-500000,negativeFraction:0.75}
  const base={node:{name:"node",uid:"node-uid"},profileID:"profile",batch:4,state:"present" as const,request:null,phase:null,reason:"",d:null,evidence:null,adopted:true}
  const occupancy=[{...base,schedulerD:{source:"measured" as const,value:3000000,pairs:12,window,updatedAt:"2026-11-03T00:02:00Z"},observedD:{statistics,pairs:20,window}},...(["dom","default"] as const).map((source,index)=>({...base,batch:16+index,schedulerD:{source,value:0,pairs:0,window:null,updatedAt:"2026-11-03T00:02:00Z"},observedD:null})),{...base,batch:8,schedulerD:null,observedD:null}]
  const html=renderCoverage("cell=w8-cell",[{...cell,id:"w8-cell",occupancy}])
  const detail=html.slice(html.indexOf('aria-label="d · 분배 점유시간"')).split("</section>")[0]
  assert.match(detail,/스케줄러 d/)
  assert.match(detail,/관측 d\(B\)/)
  assert.match(detail,/측정/)
  assert.match(detail,/DOM/)
  assert.match(detail,/기본값/)
  assert.match(detail,/3\.00/)
  assert.match(detail,/-1\.00/)
  assert.match(detail,/IQR/)
  assert.match(detail,/p10/)
  assert.match(detail,/75\.00/)
  assert.doesNotMatch(detail,/채택|Accepted|NotSaturated/)
})

test("current measurement identity and time come only from the server even for failed or partial cells",()=>{
  const current={id:"latest-measurement",model:model.name,deviceClass:cls.name,executionPath:"cpu",powerMode:"MAXN",measuredAt:"2026-11-03T01:02:03Z"}
  for(const phase of ["failed","partial"] as const)for(const currentMeasurement of [current,null]) {
    const assessment={...cell.assessment,status:phase,displayStatus:phase==="failed"?"problem" as const:"insufficient" as const,checklist:{...cell.assessment.checklist,criteriaMet:false,checks:[{code:"PowerSource",result:"fail" as const,message:"전력 출처 불일치",sourceIds:[]}]}}
    const selectedCell:MeasurementCoverageCell={...cell,id:"current-cell",currentMeasurement,assessment,variantCount:1,variants:[{id:"history-variant",condition:{},assessment,batchProgress:cell.batchProgress!,resultRows:[resultRow(30)],profileIds:["older-success"],requestIds:[],runIds:[]}]}
    const html=renderCoverage("cell=current-cell",[selectedCell])
    const selected=html.slice(html.indexOf('id="coverage-selected-condition"'))
    const facts=selected.match(/<table[^>]*aria-label="현재 측정"[^>]*>.*?<\/table>/)?.[0]
    assert.ok(facts)
    if(currentMeasurement) {
      assert.match(facts,/latest-measurement/)
      assert.match(facts,/<time dateTime="2026-11-03T01:02:03Z">/)
      assert.ok(facts.includes(measurementHelpers.formatMeasurementTime(current.measuredAt)))
    } else {
      assert.match(facts,/없음/)
      assert.match(facts,/미제공/)
      assert.doesNotMatch(facts,/<time/)
    }
    assert.doesNotMatch(facts,/older-success|history-variant/)
    assert.match(selected,/<a href="\/measurements\/requests\/new\?/)
  }
})
test("execution declaration diagnostics remain visible without changing cell status or request actions",()=>{
  const selected={...cell,id:"diagnostic-cell",support:{...cell.support,verdict:"Compatible" as const,reasons:[],diagnostics:["Multiple runtime entries matched; last entry selected."]}}
  const html=renderCoverage("cell=diagnostic-cell",[selected])
  const detail=html.slice(html.indexOf('id="coverage-selected-condition"'))
  assert.match(detail,/aria-label="실행 선언 진단"/)
  assert.match(detail,/Multiple runtime entries matched; last entry selected\./)
  assert.equal(coverageMeasurementStatus(selected),cell.assessment.displayStatus)
  assert.match(detail,/<a href="\/measurements\/requests\/new\?/)
})
test("M1–M2 unsupported cells stay visible but only supported cells contribute denominators",()=>{
  const html=renderCoverage()
  assert.equal((html.match(/data-coverage-cell=/g)??[]).length,3)
  assert.match(html,/15W/)
  assert.match(html,/미측정/)
  assert.match(html,/지원 안 함/)
  assert.match(html,/측정 있음 \/ 지원 조합/)
  assert.match(html,/배치 측정 \/ 목표/)
  assert.match(html,/정책 미설정 \/ 측정 근거/)
  assert.match(html,/측정 근거 없음 \(조합\)/)
  assert.match(html,/<td data-label="측정 있음 \/ 지원 조합">0\/2<\/td><td data-label="배치 측정 \/ 목표">2\/8<\/td>/)
  assert.match(html,/<td data-label="지원 안 함 \(집계 제외\)">1<\/td>/)
  const unsupported=renderCoverage("status=unsupported&supportReason=RuntimeUnsupported")
  assert.equal((unsupported.match(/data-coverage-cell=/g)??[]).length,1)
  assert.match(unsupported,/<td data-label="지원 안 함 \(집계 제외\)">1<\/td>/)
})

test("coverage header shows server observation time, freshness and refresh status",()=>{
  const html=renderCoverage()
  const header=html.match(/<dl[^>]*aria-label="범위 기준"[^>]*>.*?<\/dl>/)?.[0]
  assert.ok(header)
  assert.match(header,/기준 시각 KST/)
  assert.ok(header.includes(measurementHelpers.formatMeasurementTime("2026-10-30T03:04:05Z")))
  assert.match(header,/신선도/)
  assert.match(header,/최신/)
  assert.match(header,/조회 갱신/)
  assert.match(header,/완료/)
  assert.match(html,/<button[^>]*>갱신<\/button>/)
  const stale=renderCoverage("",[],"stale").match(/<dl[^>]*aria-label="범위 기준"[^>]*>.*?<\/dl>/)?.[0]
  assert.ok(stale)
  assert.match(stale,/오래된 관측/)
  const refreshing=renderCoverage("",[],"stale",true)
  assert.match(refreshing.match(/<dl[^>]*aria-label="범위 기준"[^>]*>.*?<\/dl>/)?.[0]??"",/조회 중/)
  assert.match(refreshing,/<button[^>]*disabled=""[^>]*>갱신<\/button>/)
})

test("empty failed and unsupported selected results do not claim to be unmeasured",()=>{
  const failed:MeasurementCoverageCell={...cell,id:"failed-empty",batchProgress:{...cell.batchProgress!,measured:[]},assessment:{...cell.assessment,status:"failed",displayStatus:"problem",label:"문제"},failureSummary:[{requestId:"ns/failed/request-uid",runId:"",runNamespace:"",runName:"",batch:null,code:"Other",exitCode:null,message:"실행 전 실패",relation:"unresolved"}]}
  for(const [id,label] of [["failed-empty","문제"],["unsupported","지원 안 함"]]) {
    const html=renderCoverage(`cell=${id}`,[failed])
    const selected=html.slice(html.indexOf('id="coverage-selected-condition"'))
    assert.match(selected,new RegExp(`<td>${label}<\\/td>`))
    const results=selected.match(/<section><h3>배치별 결과<\/h3>.*?<\/section>/)?.[0]
    assert.ok(results)
    assert.match(results,/미기록/)
    assert.doesNotMatch(results,/미측정/)
  }
  const unmeasured=renderCoverage("cell=empty").split('id="coverage-selected-condition"')[1]
  assert.match(unmeasured.match(/<section><h3>배치별 결과<\/h3>.*?<\/section>/)?.[0]??"",/미측정/)
})

test("M3 existing used results and failed requests coexist in scope counts",()=>{
  const failed={...cell,assessment:{...cell.assessment,displayStatus:"problem" as const,scope:{...cell.assessment.scope,schedulerUsed:true}}}
  const counts=coverageHelpers.coverageScopeCounts([failed])
  assert.equal(counts.usedCells,1)
  assert.equal(counts.failedCells,1)
  assert.equal(counts.measuredCells,0)
  const html=renderCoverage("",[failed])
  assert.match(html,/현재 사용 \/ 지원 조합/)
  assert.match(html,/문제 \/ 지원 조합/)
})

test("M4 missing target denominator does not render fake progress",()=>{
  const noTarget={...cell,id:"no-target",powerMode:"no-target",batchProgress:null}
  const html=renderCoverage("",[noTarget])
  assert.match(html,/목표 미제공/)
  const button=html.match(/<button[^>]*data-coverage-cell="no-target"[^>]*>.*?<\/button>/)?.[0]
  assert.ok(button)
  assert.doesNotMatch(button,/<progress/)
  assert.equal(coverageHelpers.coverageScopeCounts([noTarget]).targetBatches,0)
})

test("same-condition request preserves source identities and rejects replaced names",()=>{
  const params=new URL(coverageRequestHref(cell,"ns","/measurements"),"http://local").searchParams
  assert.equal(params.get("modelUID"),model.uid)
  assert.equal(params.get("deviceClassUID"),cls.uid)
  assert.equal(params.get("nodeUID"),"node-uid")
  assert.deepEqual(resolveMeasurementPrefill(params,"ns",inventory).errors,[])
  const replaced={...inventory,models:[{...model,uid:"replacement"}]}
  assert.match(resolveMeasurementPrefill(params,"ns",replaced).errors.join(" "),/모델 UID 불일치/)
})

test("coverage keeps active filters visible and omits empty result table headings and repeated absent targets",()=>{
  const noTarget={...cell,id:"no-target",powerMode:"no-target",batchProgress:null}
  const html=renderCoverage("cell=no-target&status=unmeasured&node=node",[noTarget])
  assert.match(html,/선택한 필터/)
  assert.match(html,/전체 해제/)
  assert.match(html,/상태: 미측정/)
  assert.doesNotMatch(html,/CPU 상한|CPU quota|CPU 코어/)
  const result=html.match(/<section><h3>배치별 결과<\/h3>.*?<\/section>/)?.[0]??""
  assert.match(result,/미측정/)
  assert.doesNotMatch(result,/<thead/)
  const button=html.match(/<button[^>]*data-coverage-cell="no-target"[^>]*>.*?<\/button>/)?.[0]??""
  assert.doesNotMatch(button,/목표 미제공/)
})

test("progressing cell shows recorded run progress without inventing a planned total or ETA",()=>{
  const running={...cell,id:"running",assessment:{...cell.assessment,displayStatus:"running" as const}}
  const html=renderCoverage("cell=running",[running])
  assert.match(html,/진행 회차/)
  assert.match(html,/예정 회차/)
  assert.match(html,/미제공/)
  assert.doesNotMatch(html,/완료 예정/)
})

test("R18 quality summary separates 48 measured policy-unset cells from five without evidence",()=>{
  const assessment={...cell.assessment,evidence:{available:true,accepted:true,reasons:[]},checklist:{...cell.assessment.checklist,checks:[{code:"FrozenPowerWindowPolicy",result:"unknown" as const,reason:"PolicyUnset",message:"policy",sourceIds:[]}]}}
  const measured={...cell,assessment,variants:[{id:"measured",condition:{},assessment,batchProgress:cell.batchProgress!,resultRows:[resultRow(10)],requestIds:[],runIds:[],profileIds:[]}]}
  const counts=coverageHelpers.coverageScopeCounts([...Array.from({length:48},()=>measured),...Array.from({length:5},()=>cell)])
  assert.equal(counts.unknownQualityCells,48)
  assert.equal(counts.noEvidenceCells,5)
  assert.equal(counts.policyUnsetCells,48)
  assert.equal(coverageQuality(measured).reasons[0].label,"전력 기록 정책 미설정")
})

test("coverage occupancy keeps legacy records separate from scheduler provenance and targeted retries",()=>{
  const base={node:{name:"node",uid:"node-uid"},profileID:"ns/beo/beo-uid",request:{name:"occ",uid:"occ-uid"},phase:null,reason:"",d:null,evidence:null,adopted:null}
  const occupancy:NonNullable<MeasurementCoverageCell["occupancy"]>=[
    {...base,batch:1,state:"none",request:null},
    {...base,batch:2,state:"measuring",phase:"Measuring"},
    {...base,batch:4,state:"failed",phase:"Insufficient",reason:"NotSaturated"},
    {...base,batch:8,state:"present",phase:"Complete",d:{perRequestNanos:1000000,throughput:100,idleFraction:0.01,verdict:"Accepted"}},
  ]
  const html=renderCoverage("cell=d-cell",[{...cell,id:"d-cell",executionPath:"d-cpu",occupancy}])
  assert.match(html,/d 없음 1 \/ 측정 중 1 \/ 실패 1 \/ 있음 1/)
  const detail=html.slice(html.indexOf('aria-label="d · 분배 점유시간"')).split("</section>")[0]
  assert.match(detail,/Node/)
  assert.match(detail,/d \(ms\/요청\)/)
  assert.doesNotMatch(detail,/1\.00/)
  assert.match(detail,/부하 미포화/)
  assert.doesNotMatch(detail,/채택/)
  const links=[...detail.matchAll(/href="([^"]+)"[^>]*>d 요청<\/a>/g)].map(match=>new URL(match[1].replaceAll("&amp;","&"),"https://dashboard.invalid")).filter(link=>link.searchParams.has("batch"))
  assert.equal(links.length,2)
  assert.deepEqual(links.map(link=>link.searchParams.get("batch")),["1","4"])
  for(const link of links) {
    assert.equal(link.pathname,"/measurements/requests/new")
    assert.equal(link.searchParams.get("kind"),"dispatch-occupancy")
    assert.equal(link.searchParams.get("nodeUID"),"node-uid")
    assert.equal(link.searchParams.get("model"),model.name)
    assert.equal(link.searchParams.get("powerMode"),"MAXN")
    assert.equal(link.searchParams.get("path"),"d-cpu")
  }
})

const occupancyBase={node:{name:"beo-target",uid:"target-uid"},profileID:"ns/beo/beo-uid",request:null,phase:null,reason:"",d:null,evidence:null,adopted:null}
const currentMeasurement={id:"ns/current-beo/current-uid",model:model.name,deviceClass:cls.name,executionPath:"cpu",powerMode:"MAXN",measuredAt:"2026-11-03T01:02:03Z"}

function cellOccupancyLinks(selected:MeasurementCoverageCell) {
  const html=renderCoverage(`cell=${selected.id}`,[selected])
  const section=html.slice(html.indexOf('aria-label="d · 분배 점유시간"')).split("</section>")[0]
  return [...section.matchAll(/href="([^"]+)"[^>]*>d 요청<\/a>/g)].map(match=>new URL(match[1].replaceAll("&amp;","&"),"https://dashboard.invalid"))
}

test("coverage regression: partial, present, measuring and empty occupancy retain one cell-level d request",()=>{
  const partial:NonNullable<MeasurementCoverageCell["occupancy"]>=[{...occupancyBase,batch:1,state:"present"}]
  for(const occupancy of [partial,undefined,[],[{...occupancyBase,batch:1,state:"measuring" as const}],[...partial,{...occupancyBase,batch:2,state:"none" as const}]]) {
    const selected={...cell,id:"request-cell",executionPath:"request-cpu",currentMeasurement,occupancy}
    const links=cellOccupancyLinks(selected).filter(link=>!link.searchParams.has("batch"))
    assert.equal(links.length,1,"every occupancy state must expose exactly one cell-level request")
    assert.equal(links[0].searchParams.get("model"),model.name)
    assert.equal(links[0].searchParams.get("deviceClass"),cls.name)
    assert.equal(links[0].searchParams.get("path"),"request-cpu")
    assert.equal(links[0].searchParams.get("powerMode"),"MAXN")
    assert.equal(links[0].searchParams.get("measurementID"),currentMeasurement.id)
    assert.equal(links[0].searchParams.has("profileID"),false)
  }
})

test("coverage regression: cell-level d request omits mismatched representative Node identity",()=>{
  const selected={...cell,id:"node-mismatch",currentMeasurement,support:{...cell.support,node:{name:"representative-not-beo-target",uid:"representative-uid"}}}
  const links=cellOccupancyLinks(selected)
  assert.equal(links.length,1)
  assert.equal(links[0].searchParams.has("node"),false)
  assert.equal(links[0].searchParams.has("nodeUID"),false)
})

for(const status of [401,403])test(`coverage regression: HTTP ${status} stops cached measuring occupancy polling`,()=>{
  let inspected=false
  renderCoverage("",[],"fresh",false,interval=>{
    inspected=true
    const data={cells:[{...cell,occupancy:[{...occupancyBase,batch:1,state:"measuring" as const}]}]}
    assert.equal(interval({state:{data,error:null}}),5000)
    assert.equal(interval({state:{data,error:{status}}}),false)
    assert.equal(interval({state:{data:{cells:[cell]},error:null}}),false)
  })
  assert.equal(inspected,true)
})

test("coverage regression: cell accessible name includes occupancy summary",()=>{
  const selected={...cell,id:"accessible-d",executionPath:"accessible-cpu",occupancy:[{...occupancyBase,batch:1,state:"measuring" as const}]}
  const html=renderCoverage("",[selected])
  const button=html.match(/<button[^>]*data-coverage-cell="accessible-d"[^>]*>/)?.[0]
  assert.ok(button)
  const name=button.match(/aria-label="([^"]*)"/)?.[1]
  assert.ok(name?.includes(occupancyHelpers.occupancySummary(selected.occupancy)))
})

