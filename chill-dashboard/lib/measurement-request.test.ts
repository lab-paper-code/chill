import assert from "node:assert/strict"
import test from "node:test"
import { datasetGap, requestNumber, requestBatches, requestDefaults, requestInputs, requestInitialBatches, requestTargetBatches, requestConditionChanges, requestPreflightReason, requestRejectionGuidance, requestOperationalSummary, requestFailureFacts, requestCheckCurrent, requestHref, requestRunHref, requestUsedResults, requestCopySource, requestCopyQuery, requestExecutionStatus } from "./measurement-request.ts"
import type { MeasurementAssessment, MeasurementCoverageCell, MeasurementPlan, ProfilesView, ResultRow } from "./read-types.ts"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { runInNewContext } from "node:vm"
import { createElement } from "react"
import type { ComponentType, ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as requestHelpers from "./measurement-request.ts"
import * as coverageHelpers from "./measurement-coverage.ts"
import * as measurementHelpers from "./measurements.ts"
import * as measurementSummary from "./measurement-summary.ts"

const condition={model:"model & one",deviceClass:"device",node:"node",path:"cuda",powerMode:"MAXN"}
const plan:MeasurementPlan={metadata:{name:"source",namespace:"ns",uid:"uid",resourceVersion:"1",creationTimestamp:"2026-10-30T00:00:00Z"},spec:{state:{candidate:{modelSpec:condition.model,deviceClass:condition.deviceClass,preprocessor:"pre",executionPath:condition.path,powerMode:condition.powerMode},targetNode:condition.node},activeProtocol:{warmupIterations:5,durationSeconds:10,dataset:"data",metricObservation:{intervalMilliseconds:1000,timeoutMilliseconds:500,durationSeconds:11}},idleProtocol:{settlingDurationSeconds:2,durationSeconds:10,metricObservation:{intervalMilliseconds:1000,timeoutMilliseconds:500,durationSeconds:11}},trialsPerBatch:3,retryLimit:1}}

test("request query batches decode, deduplicate and sort without accepting malformed values",()=>{
  const query=new URLSearchParams({...condition,batches:"8,1,4,1"})
  assert.equal(query.get("model"),condition.model)
  assert.deepEqual(requestBatches(query),{values:[1,4,8],error:""})
  for(const raw of ["0", "-1", "1.5", "1e2", "1,", "2147483648", "1&batches=2"]){const result=requestBatches(new URLSearchParams(`batches=${raw}`));assert.notEqual(result.error,"");assert.deepEqual(result.values,[])}
  assert.deepEqual(requestBatches(new URLSearchParams()),{values:[],error:""})
})

test("default protocol comes from newest server request with the same condition, never another mode or Node",()=>{
  const newer={...plan,metadata:{...plan.metadata,name:"newer",creationTimestamp:"2026-10-31T00:00:00Z"},spec:{...plan.spec,retryLimit:2}}
  const selected=requestDefaults(condition,[plan,newer],[])
  assert.deepEqual(selected?.spec,newer.spec)
  assert.match(selected?.source??"",/newer/)
  for(const patch of [{model:"other"},{deviceClass:"other"},{node:"other"},{path:"cpu"},{powerMode:"15W"}])assert.equal(requestDefaults({...condition,...patch},[plan],[]),null)
  assert.equal(requestDefaults({...condition,node:""},[plan],[])?.spec.state.targetNode,"node")
  assert.equal(plan.spec.activeProtocol.dataset,"data")
})

test("CPU-free catalog fallback retains measured protocol and identifies unprovided retry policy",()=>{
  const profiles=[{id:"profile",nodeName:"node",measuredAt:"2026-10-30T00:00:00Z",condition:{logicalModel:condition.model,deviceClass:condition.deviceClass,powerMode:condition.powerMode,artifact:{executionPath:condition.path},preprocessor:{name:"pre"},activeProtocol:{warmupIterations:5,durationSeconds:10,dataset:{name:"data"},metricIntervalMilliseconds:1000,metricTimeoutMilliseconds:500,metricDurationSeconds:11},idleProtocol:{settlingDurationSeconds:2,durationSeconds:10,metricIntervalMilliseconds:1000,metricTimeoutMilliseconds:500,metricDurationSeconds:11}}}] as unknown as ProfilesView["profiles"]
  const selected=requestDefaults(condition,[],profiles)
  assert.deepEqual(selected?.spec,{...plan.spec,retryLimit:NaN})
  assert.match(selected?.source??"",/profile/)
  assert.equal(requestDefaults({...condition,path:"cpu"},[],profiles),null)
})

test("unmeasured targets reuse successful protocol without copying another device's batches or Node",()=>{
  const success={...plan,status:{phase:"Complete"},spec:{...plan.spec,batchSizes:[16]}}
  const otherTarget={...condition,deviceClass:"arm-board",node:"arm-node",path:"arm-cpu",powerMode:"15W"}
  const defaults=requestDefaults(otherTarget,[success],[])
  assert.equal(defaults?.exact,false)
  assert.match(defaults?.source??"",/source.*device.*같은 모델/)
  assert.deepEqual(defaults?.spec,{...plan.spec,state:{...plan.spec.state,targetNode:""},batchSizes:undefined})
  const pathOnly=requestDefaults({...condition,model:"new-model"},[success],[])
  assert.match(pathOnly?.source??"",/같은 실행 경로/)
  assert.equal(requestDefaults({...otherTarget,model:"new-model"},[success],[]),null)
  assert.equal(requestDefaults(otherTarget,[{...success,status:{phase:"Failed"}}],[]),null)
})

test("empty coverage batch query is a missing selection, not a permanent URL error",()=>{
  assert.deepEqual(requestBatches(new URLSearchParams("batches=")),{values:[],error:""})
  assert.deepEqual(requestInitialBatches([],16),[1,2,4])
  assert.deepEqual(requestInitialBatches([],2),[1,2])
  assert.deepEqual(requestInitialBatches([8,1],16),[1,8])
})

test("unmeasured model inputs prefer same-model history and never infer an image task from VQA surplus fields",()=>{
  const context={preprocessors:[{metadata:{name:"image-pre"},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},artifacts:[{target:{model:"opaque-id",artifact:"onnx"}}]}}],datasets:[{metadata:{name:"vqa"},spec:{fields:[{name:"image",format:"JPEG",maxBytes:512},{name:"question",format:"UTF8",maxBytes:512}]}}]} as Parameters<typeof requestInputs>[4]
  assert.equal(requestInputs("opaque-id","onnx","","",context).dataset,"")
  assert.equal(requestInputs("opaque-id","onnx","image-pre","vqa",context,{dataset:true}).dataset,"")
  assert.equal(requestInputs("opaque-id","onnx","image-pre","vqa",context).dataset,"vqa")
  const full={...context,datasets:[...context.datasets,{metadata:{name:"images"},spec:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]}}]} as Parameters<typeof requestInputs>[4]
  assert.equal(requestInputs("opaque-id","onnx","","",full).dataset,"images")
  const historical={...full,plans:[{...plan,spec:{...plan.spec,state:{...plan.spec.state,candidate:{...plan.spec.state.candidate,modelSpec:"opaque-id"}},activeProtocol:{...plan.spec.activeProtocol,dataset:"vqa"}}}]}
  assert.equal(requestInputs("opaque-id","onnx","","",historical).dataset,"vqa")
  assert.equal(requestInputs("opaque-id","onnx","","",{...full,plans:[plan]}).dataset,"images")
  assert.equal(requestInputs("other-model","onnx","image-pre","vqa",full,{preprocessor:true,dataset:true}).preprocessor,"")
  const ambiguous={...full,datasets:[...full.datasets,{...full.datasets[1],metadata:{...full.datasets[1].metadata,name:"images-two"}}]}
  assert.equal(requestInputs("opaque-id","onnx","","",ambiguous).dataset,"")
  assert.match(requestInputs("opaque-id","onnx","","",ambiguous).warning,/여러 개/)
  const profileHistory={...ambiguous,profiles:[{measuredAt:"2026-10-31",condition:{logicalModel:"opaque-id",preprocessor:{name:"image-pre"},activeProtocol:{dataset:{name:"images-two"}}}}] as unknown as ProfilesView["profiles"]}
  assert.equal(requestInputs("opaque-id","onnx","","",profileHistory).dataset,"images-two")
})

test("shared dataset compatibility rejects missing fields, formats and oversized records",()=>{
  const pre={input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]}} as Parameters<typeof datasetGap>[1]
  for(const fields of [[],[{name:"photo",format:"JPEG",maxBytes:512}],[{name:"image",format:"UTF8",maxBytes:512}],[{name:"image",format:"JPEG",maxBytes:1025}]]) {
    const data={metadata:{name:"bad"},spec:{fields}}
    assert.notEqual(datasetGap(data.spec as Parameters<typeof datasetGap>[0],pre),"")
    const context={preprocessors:[{metadata:{name:"pre"},spec:{...pre,artifacts:[{target:{model:"model",artifact:"onnx"}}]}}],datasets:[data]} as Parameters<typeof requestInputs>[4]
    const inputs=requestInputs("model","onnx","pre","bad",context,{dataset:true})
    assert.equal(inputs.dataset,"")
    assert.match(inputs.warning,/image.*JPEG.*1024.*등록/)
    assert.match(requestInputs("model","onnx","pre","bad",context).warning,/image/)
  }
  assert.equal(datasetGap({source:{image:"dataset",path:"/records"},fields:[{name:"image",format:"JPEG",maxBytes:1024},{name:"question",format:"UTF8",maxBytes:512}]},pre),"")
})

test("empty datasets explain the required contract and missing summary values use a dash",()=>{
  const context:Parameters<typeof requestInputs>[4]={preprocessors:[{metadata:{name:"pre",uid:"pre-uid",resourceVersion:"1",generation:1},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},output:{fields:[]},artifacts:[{target:{model:"model",artifact:"onnx"},source:{image:"preprocessor:fixture",architecture:"arm64",path:"/preprocess"}}]}}],datasets:[]}
  const inputs=requestInputs("model","onnx","","",context)
  assert.equal(inputs.dataset,"")
  assert.match(inputs.warning,/image.*JPEG.*1024.*등록/)
  assert.equal(requestNumber(NaN),"—")
  assert.equal(requestNumber(Infinity),"—")
  assert.equal(requestNumber(0),"0")
  assert.equal(requestNumber(3),"3")
})

test("frozen condition explanation lists only observed changed fields, not missing facts",()=>{
  const before={nodeUID:"old",activeProtocol:{dataset:{name:"vqa"}},runtimeImage:"same",preprocessor:{name:"image"}}
  const after={nodeUID:"new",activeProtocol:{dataset:{name:"images"}},runtimeImage:"same"}
  const changes=requestConditionChanges(before,after)
  assert.deepEqual(changes.map(change=>change.field),["nodeUID","activeProtocol.dataset.name"])
  assert.deepEqual(changes[1],{field:"activeProtocol.dataset.name",label:"데이터셋",before:"vqa",after:"images"})
  assert.deepEqual(requestConditionChanges(before,undefined),[])
  assert.match(requestPreflightReason({code:"VariantConditionChanged",message:"English warning"}),/기존.*제외.*확인/)
  assert.equal(requestPreflightReason({code:"FutureReason",message:"서버 상세"}),"서버 상세")
})

test("request batch selection preserves explicit URL scope and previous target without inventing batches",()=>{
  assert.deepEqual(requestTargetBatches(new URLSearchParams("batches=8,2"),[1,2,4,8],4),[2,8])
  assert.deepEqual(requestTargetBatches(new URLSearchParams("batches="),[1]),[])
  assert.deepEqual(requestTargetBatches(new URLSearchParams(),[1]),[1])
  assert.deepEqual(requestTargetBatches(new URLSearchParams(),[]),[])
  assert.deepEqual(requestTargetBatches(new URLSearchParams(),undefined,2),[1,2])
})

test("changing a preprocessor clears only inherited datasets, preserving deliberate mismatched selections",()=>{
  const context={preprocessors:[{metadata:{name:"image-pre"},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},artifacts:[{target:{model:"model",artifact:"onnx"}}]}}],datasets:[{metadata:{name:"vqa"},spec:{fields:[{name:"image",format:"UTF8",maxBytes:512}]}}]} as Parameters<typeof requestInputs>[4]
  assert.equal(requestInputs("model","onnx","image-pre","vqa",context,{dataset:true}).dataset,"")
  assert.equal(requestInputs("model","onnx","image-pre","vqa",context,{dataset:false}).dataset,"vqa")
})


test("rejected preflight points to exact repair fields instead of generic permission or network advice",()=>{
  const idle=requestRejectionGuidance("model-loaded idle measurement protocol is invalid")
  assert.deepEqual(idle?.fields,["settling"])
  assert.equal(idle?.message,"유휴 측정 조건 확인")
  assert.deepEqual(idle?.constraints[0],["안정 시간 (s)","≥ 1"])
  const metric=requestRejectionGuidance("invalid or under-covering metric observation request")
  assert.ok(metric?.fields.includes("idleWindow"))
  assert.deepEqual(metric?.constraints[1],["대기 한도","< 기록 간격"])
  assert.equal(requestRejectionGuidance("future unknown rejection"),null)
  assert.deepEqual(requestOperationalSummary(undefined),{servingImpact:null,estimatedDurationSeconds:null})
  const answer={resolves:true,campaign:{spec:plan.spec},batchPlan:{plannedBatches:[1,2]}}
  assert.deepEqual(requestOperationalSummary(answer),{servingImpact:null,estimatedDurationSeconds:96})
  assert.equal(requestOperationalSummary({...answer,campaign:{spec:{...plan.spec,activeProtocol:{...plan.spec.activeProtocol,warmupIterations:500}}}}).estimatedDurationSeconds,96)
  assert.equal(requestOperationalSummary({...answer,batchPlan:{plannedBatches:[1]}}).estimatedDurationSeconds,66)
  assert.equal(requestOperationalSummary({...answer,batchPlan:{plannedBatches:[1,2,4]}}).estimatedDurationSeconds,126)
  assert.equal(requestOperationalSummary({...answer,campaign:{spec:{...plan.spec,idleProtocol:{durationSeconds:10}}}}).estimatedDurationSeconds,null)
  assert.equal(requestOperationalSummary({...answer,batchPlan:{plannedBatches:[]}}).estimatedDurationSeconds,0)
  assert.equal(requestOperationalSummary({...answer,resolves:false}).estimatedDurationSeconds,null)
  const pools={workloads:[{workload:{uid:"service"},instances:[{node:{name:"node"},stopped:false}]},{workload:{uid:"service"},instances:[{node:{name:"node"},stopped:false}]},{workload:{uid:"other"},instances:[{node:{name:"elsewhere"},stopped:false}]}],issues:[]}
  assert.equal(requestOperationalSummary(answer,pools).servingImpact,1)
  assert.equal(requestOperationalSummary(answer,{...pools,issues:[{code:"Unavailable"}]}).servingImpact,null)
  assert.equal(requestOperationalSummary(answer,{...pools,workloads:[]}).servingImpact,0)
})

test("failure cause rows retain server reason and message before a generic classification",()=>{
  assert.deepEqual(requestFailureFacts([{type:"Complete",status:"False",reason:"NativePreparationFailed",message:"Pod image missing"}]),[["서버 상태","Complete: False"],["서버 사유","NativePreparationFailed"],["서버 메시지","Pod image missing"]])
  assert.deepEqual(requestFailureFacts([]),[["서버 사유","미제공"],["서버 메시지","미제공"]])
})

test("successful checks are invalid after any input or source identity change",()=>{
  const body=JSON.stringify({name:"checked",batch:[1],sourceUID:"original"})
  assert.equal(requestCheckCurrent(body,body,true,true),true)
  assert.equal(requestCheckCurrent(null,body,true,true),false)
  for(const current of [JSON.stringify({name:"renamed",batch:[1],sourceUID:"original"}),JSON.stringify({name:"checked",batch:[2],sourceUID:"original"}),JSON.stringify({name:"checked",batch:[1],sourceUID:"replacement"})])assert.equal(requestCheckCurrent(body,current,true,true),false)
  assert.equal(requestCheckCurrent(body,body,false,true),false)
  assert.equal(requestCheckCurrent(body,body,true,false),false)
})

test("canonical request and child round links preserve namespaces and immutable identities",()=>{
  const request=new URL(requestHref("ns / one","parent / one","parent-uid"),"https://dashboard.invalid")
  assert.equal(request.pathname,"/measurements/requests/parent%20%2F%20one")
  assert.equal(request.searchParams.get("namespace"),"ns / one")
  assert.equal(request.searchParams.get("uid"),"parent-uid")
  const child=new URL(requestRunHref("ns","run / one","run-uid",{name:"parent",uid:"parent-uid"}),"https://dashboard.invalid")
  assert.equal(child.pathname,"/measurements/requests/parent/runs/run%20%2F%20one")
  assert.equal(child.searchParams.get("requestUID"),"parent-uid")
  assert.equal(child.searchParams.get("uid"),"run-uid")
  const orphan=new URL(requestRunHref("ns","orphan","run-uid"),"https://dashboard.invalid")
  assert.equal(orphan.pathname,"/measurements/requests")
  assert.equal(orphan.searchParams.get("run"),"orphan")
})

test("child round links retain parent identity or the filtered search return context",()=>{
  const parent=requestHref("ns","parent","parent-uid")
  const child=new URL(requestRunHref("ns","round","round-uid",{name:"parent",uid:"parent-uid"}),"https://dashboard.invalid")
  assert.equal(child.searchParams.get("returnTo"),parent)
  const filtered="/measurements/requests?namespace=ns&view=runs&model=model&page=3"
  const search=new URL(requestRunHref("ns","round","round-uid",undefined,filtered),"https://dashboard.invalid")
  assert.equal(search.searchParams.get("returnTo"),filtered)
  const unsafe=new URL(requestRunHref("ns","round","round-uid",undefined,"https://outside.invalid"),"https://dashboard.invalid")
  assert.equal(unsafe.searchParams.get("returnTo"),"/measurements/requests?namespace=ns&view=runs")
})


test("name-only copies pin observed namespace and UID, preserve navigation, and reject replacements",()=>{
  const params=new URLSearchParams("from=source&returnTo=%2Fmeasurements%2Frequests%3Fpage%3D2")
  const found=requestCopySource("source","ns",null,[plan])
  assert.equal(found.source,plan)
  assert.equal(found.error,"")
  const pinned=new URLSearchParams(requestCopyQuery(params,plan))
  assert.equal(pinned.get("namespace"),"ns")
  assert.equal(pinned.get("fromNamespace"),"ns")
  assert.equal(pinned.get("fromUID"),"uid")
  assert.equal(pinned.get("returnTo"),"/measurements/requests?page=2")
  assert.equal(requestCopySource("source","ns","old",[plan]).source,undefined)
  assert.notEqual(requestCopySource("source","ns","old",[plan]).error,"")
  assert.equal(requestCopySource("source","other",null,[plan]).source,undefined)
  assert.equal(requestCopySource("source","ns",null,[{...plan,metadata:{...plan.metadata,uid:""}}]).source,undefined)
  assert.equal(requestExecutionStatus("private-phase"),"미확인")
})


function renderRequestList(kind:"requests"|"runs",record:unknown,cells:MeasurementCoverageCell[]=[]) {
  const source=readFileSync(new URL(`../features/measurement-${kind}.tsx`,import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports:Record<string,unknown>={}
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const table=({columns,data,urlState}:{columns:{key:string;label:string;value:(row:unknown)=>ReactNode}[];data:unknown[];urlState?:boolean})=>createElement("table",{"data-url-state":urlState},createElement("thead",null,createElement("tr",null,columns.map(column=>createElement("th",{key:column.key},column.label)))),createElement("tbody",null,data.map((row,index)=>createElement("tr",{key:index},columns.map(column=>createElement("td",{key:column.key},column.value(row)))))))
  const modules:Record<string,unknown>={
    "next/link":{__esModule:true,default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},
    "next/navigation":{useSearchParams:()=>new URLSearchParams()},
    "@/lib/client":{useSession:()=>({config:{namespace:"ns"}}),useChillQuery:()=>({data:{items:[record],runs:[record],page:{total:1}}})},
    "@/lib/measurements":measurementHelpers,
    "@/lib/measurement-request":requestHelpers,
    "@/lib/measurement-coverage":coverageHelpers,
    "@/lib/measurement-summary":measurementSummary,
    "@/components/measurement-filters":{MeasurementFilters:()=>null},
    "@/hooks/use-measurement-data":{useMeasurementPlans:()=>({data:{items:[record],page:{total:1}}}),useMeasurementCoverage:()=>({data:{cells}})},
    "@/features/measurement-plans":{MeasurementPlanSheet:()=>null},
    "@/components/read-region":{QueryGate:container,SourceJSON:({inline,value}:{inline?:boolean;value:unknown})=>inline?createElement("pre",null,JSON.stringify(value)):null,SourceExport:()=>null},
    "@/components/screen":{Screen:container},
    "@/components/data-table":{DataTable:table,useServerTable:()=>({query:""})},
    "@/components/ui/button":{Button:container},
    "@/components/status-cell":{StatusCell:({label}:{label:string})=>createElement("span",null,label)},
    "@/components/measurement-navigation":{MeasurementNavigation:()=>null,MeasurementStatus:({assessment}:{assessment?:MeasurementAssessment})=>createElement("span",null,assessment?.scope.schedulerUsed?"사용 중":"미사용")},
  }
  runInNewContext(compiled,{exports,require:(name:string)=>name in modules?modules[name]:require(name),URLSearchParams})
  return renderToStaticMarkup(createElement(exports[kind==="requests"?"MeasurementRequestsPage":"MeasurementRunsPage"] as ComponentType))
}

test("request and run summaries translate dynamic reasons and retain unknown text only in source disclosure",()=>{
  const assessment:MeasurementAssessment={status:"running",displayStatus:"running",label:"진행 중",reasons:[],execution:null,evidence:{available:false,accepted:false,reasons:[]},checklist:{criteriaMet:null,version:"result-checklist.v1",checks:[]},scope:{schedulerUsed:false,reused:false,facts:[]}}
  const reason={code:"FutureReason",label:"Private English reason",message:"No verified catalog profile for current condition"}
  const requests=renderRequestList("requests",{...plan,assessment:{...assessment,reasons:[reason]}})
  assert.match(requests,/현재 조건의 검증된 프로파일 없음/)
  assert.doesNotMatch(requests,/Private English reason/)
  assert.match(requests,/data-url-state="true"/)
  const run={namespace:"ns",name:"round",uid:"run",creationTimestamp:"2026-11-01",generation:1,spec:{candidate:plan.spec.state.candidate,targetNode:"node",measurement:{batch:4,workloadMode:"private-protocol",dataset:"data"}},status:{},assessment:{...assessment,reasons:[{...reason,message:"private unknown reason"}]}}
  const runs=renderRequestList("runs",run)
  assert.ok((runs.match(/<th>/g)??[]).length<=5)
  assert.match(runs,/기타 부하 방식/)
  assert.match(runs,/<details><summary[^>]*>조건·사유·출처/)
  assert.match(runs,/부하 방식 원문/)
  assert.match(runs,/private-protocol/)
  assert.match(runs,/상세 사유: 원문/)
  assert.match(runs,/private unknown reason/)
  assert.match(runs,/모델/)
  assert.match(runs,/데이터셋/)
})

test("failed requests retain a separate catalog-unavailable status after their failure reason",()=>{
  const failure={code:"MeasurementFailed",message:"native preparation Pod failed"}
  const catalog={code:"CatalogUnavailable",message:"serving catalog is unavailable"}
  const html=renderRequestList("requests",{...plan,status:{phase:"Failed"},assessment:{...retainedAssessment,reasons:[catalog],failureSummary:[failure]}})
  assert.match(html,/<li>모델 준비 실패<\/li><li>카탈로그 상태: 카탈로그 조회 불가<\/li>/)
  assert.doesNotMatch(html,/상세 사유: 원문|serving catalog is unavailable/)
  const complete=renderRequestList("requests",{...plan,assessment:{...retainedAssessment,reasons:[catalog]}})
  assert.match(complete,/카탈로그 상태: 카탈로그 조회 불가/)
})

test("request rows show each normalized failure cause once",()=>{
  const failure={code:"MeasurementFailed",message:"native preparation Pod failed"}
  const html=renderRequestList("requests",{...plan,status:{phase:"Failed"},assessment:{...retainedAssessment,failureSummary:[failure,{...failure,runId:"another-run"}]}})
  assert.equal(html.split("모델 준비 실패").length-1,1)
})

const retainedAssessment:MeasurementAssessment={status:"complete",displayStatus:"usable",label:"측정 완료",reasons:[],execution:null,evidence:{available:true,accepted:true,reasons:[]},checklist:{criteriaMet:null,version:"result-checklist.v1",checks:[]},scope:{schedulerUsed:true,reused:false,facts:[]}}
const retainedProgress={basis:"requested",target:[4],measured:[4],criteriaMet:[],missing:[],inProgress:[],unknown:[]}
const retainedRow:ResultRow={id:"retained",rowKind:"direct",profileId:"profile",sourceProfileId:null,batch:4,condition:{metricSource:{nodeName:"node"}},p95Milliseconds:12,latencySampleCount:100,idleWatts:2,activeWatts:3,totalJoulesPerItem:4,incrementalJoulesPerItem:1,confidence:null,activeTrialCount:3,idleTrialCount:3,completedItems:100,completedCalls:25,powerSource:null,assessment:retainedAssessment,sources:[],missingReasons:[],reuse:null}
const retainedCell:MeasurementCoverageCell={id:"cell",model:{name:condition.model,uid:"model-uid"},deviceClass:{name:condition.deviceClass,uid:"device-uid"},executionPath:condition.path,powerMode:condition.powerMode,currentMeasurement:null,support:{verdict:"Compatible",node:{name:"node",uid:"node-uid"},reasons:[],diagnostics:[]},assessment:retainedAssessment,variantCount:1,variants:[{id:"variant",condition:{metricSource:{nodeName:"node"}},assessment:retainedAssessment,batchProgress:retainedProgress,resultRows:[retainedRow],requestIds:[],runIds:[],profileIds:["profile"]}],batchProgress:retainedProgress,representative:null,failureSummary:[],history:[]}

test("failed latest requests do not remove independently retained current-use results",()=>{
  assert.deepEqual(requestUsedResults([retainedCell],plan.spec),[retainedRow])
  assert.deepEqual(requestUsedResults([retainedCell],{...plan.spec,state:{...plan.spec.state,targetNode:"other"}}),[])
  assert.deepEqual(requestUsedResults([{...retainedCell,variants:retainedCell.variants.map(variant=>({...variant,assessment:{...variant.assessment,scope:{...variant.assessment.scope,schedulerUsed:false}}}))}],plan.spec),[])
})

test("failed request detail renders failure evidence, current results, checks and same-condition action together",()=>{
  const source=readFileSync(new URL("../features/measurement-plans.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports:Record<string,unknown>={}
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const assessment=retainedAssessment
  const data={campaign:{...plan.metadata,spec:plan.spec,phase:"Failed",conditions:[{type:"Complete",status:"False",reason:"NativePreparationFailed",message:"Pod image missing"}]},assessment,resultRows:[] as ResultRow[],result:{excludedTrials:[{run:{namespace:"ns",name:"failed-child",uid:"child"}}]}}
  let queryError:{status:number;data:unknown}|undefined
  const modules:Record<string,unknown>={
    "next/link":{__esModule:true,default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},
    "next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({push:()=>{}})},
    "@tanstack/react-query":{useQueryClient:()=>({})},
    "@/lib/client":{useChillQuery:(path:string)=>queryError&&path.endsWith("/result")?{error:queryError}:{data},useChillMutation:()=>({})},
    "@/lib/measurement-request":requestHelpers,
    "@/lib/measurement-assessment":{assessmentReasons:()=>[]},
    "@/lib/measurement-coverage":coverageHelpers,
    "@/lib/measurements":measurementHelpers,
    "@/lib/scheduler":{},
    "@/lib/measurement-summary":measurementSummary,
    "@/hooks/use-measurement-data":{useMeasurementPlans:()=>({data}),useMeasurementCoverage:()=>({data:{cells:[{...retainedCell,assessment}]}})},
    "@/features/measurement-runs":{MeasurementChildRuns:()=>createElement("p",null,"child-rounds")},
    "@/components/read-region":{QueryGate:container,SourceJSON:()=>null,SourceExport:()=>null},
    "@/components/screen":{Screen:container,Rows:container,Facts:()=>null},
    "@/components/ui/button":{Button:container},
    "@/components/ui/dialog":Object.fromEntries(["Dialog","DialogContent","DialogDescription","DialogHeader","DialogTitle"].map(key=>[key,container])),
    "@/components/ui/sheet":{},
    "@/components/ui/table":Object.fromEntries(["Table","TableBody","TableCell","TableHead","TableHeader","TableRow"].map(key=>[key,container])),
    "@/components/status-cell":{StatusCell:({label}:{label:string})=>createElement("span",null,label)},
    "@/components/measurement-navigation":{MeasurementStatus:()=>null,MeasurementEvidenceChecks:()=>createElement("p",null,"checks")},
  }
  runInNewContext(compiled,{exports,require:(name:string)=>name in modules?modules[name]:require(name),URLSearchParams})
  const html=renderToStaticMarkup(createElement(exports.MeasurementPlanDetails as ComponentType<{namespace:string;name:string}>,{namespace:"ns",name:"source"}))
  assert.match(html,/NativePreparationFailed/)
  assert.match(html,/Pod image missing/)
  assert.match(html,/failed-child/)
  assert.match(html,/현재 사용 결과/)
  assert.match(html,/별도 보관 결과/)
  assert.match(html,/12\.00/)
  assert.match(html,/checks/)
  assert.match(html,/동일 조건 요청/)
  assert.match(html,/fromUID=uid/)
  assert.ok(html.indexOf("Pod image missing")<html.indexOf("측정 실패 · 요청 결과 없음"))
  assert.match(html,/<details><summary[^>]*>관리/)
  data.campaign.phase="Measuring"
  Object.assign(data.campaign,{completedTrials:7})
  const progressing=renderToStaticMarkup(createElement(exports.MeasurementPlanDetails as ComponentType<{namespace:string;name:string}>,{namespace:"ns",name:"source"}))
  assert.match(progressing,/aria-label="요청 진행"/)
  assert.match(progressing,/완료 회차 \(회\)<\/th><td[^>]*>7</)
  assert.match(progressing,/예정 회차 \(회\)<\/th><td[^>]*>미제공</)
  assert.ok(progressing.indexOf('aria-label="요청 진행"')<progressing.indexOf(">요청 조건</summary>"))
  data.campaign.phase="Complete";data.resultRows=[retainedRow]
  const success=renderToStaticMarkup(createElement(exports.MeasurementPlanDetails as ComponentType<{namespace:string;name:string}>,{namespace:"ns",name:"source"}))
  assert.equal((success.match(/12\.00/g)??[]).length,1)
  assert.doesNotMatch(success,/별도 보관 결과/)
  data.campaign.phase="Failed";data.resultRows=[]
  queryError={status:502,data:{...data,code:"BatchEnergyOptimizationResultUnavailable"}}
  const unavailable=renderToStaticMarkup(createElement(exports.MeasurementPlanDetails as ComponentType<{namespace:string;name:string}>,{namespace:"ns",name:"source"}))
  assert.match(unavailable,/HTTP 502/)
  assert.match(unavailable,/NativePreparationFailed/)
  assert.match(unavailable,/별도 보관 결과/)
  queryError={status:403,data}
  const denied=renderToStaticMarkup(createElement(exports.MeasurementPlanDetails as ComponentType<{namespace:string;name:string}>,{namespace:"ns",name:"source"}))
  assert.doesNotMatch(denied,/NativePreparationFailed/)
})


test("historical request rows retain their own usage instead of inheriting another current result in the same cell",()=>{
  const html=renderRequestList("requests",{...plan,assessment:{...retainedAssessment,scope:{...retainedAssessment.scope,schedulerUsed:false}}},[retainedCell])
  assert.match(html,/>미사용</)
  assert.doesNotMatch(html,/>사용 중</)
})

