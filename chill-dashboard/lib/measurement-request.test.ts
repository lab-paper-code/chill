import assert from "node:assert/strict"
import test from "node:test"
import { datasetGap, requestNumber, requestBatches, requestDefaults, requestInputs, requestInitialBatches, requestTargetBatches, requestConditionChanges, requestPreflightReason, requestRejectionGuidance, requestOperationalSummary, requestFailureFacts, requestCheckCurrent, requestHref, requestRunHref, requestUsedResults } from "./measurement-request.ts"
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

const condition={model:"model & one",deviceClass:"device",node:"node",path:"cuda",powerMode:"MAXN"}
const plan:MeasurementPlan={metadata:{name:"source",namespace:"ns",uid:"uid",resourceVersion:"1",creationTimestamp:"2026-10-30T00:00:00Z"},spec:{state:{candidate:{modelSpec:condition.model,deviceClass:condition.deviceClass,preprocessor:"pre",executionPath:condition.path,powerMode:condition.powerMode},targetNode:condition.node,cpuRequest:"2"},activeProtocol:{warmupIterations:5,durationSeconds:10,dataset:"data",metricObservation:{intervalMilliseconds:1000,timeoutMilliseconds:500,durationSeconds:11}},idleProtocol:{settlingDurationSeconds:2,durationSeconds:10,metricObservation:{intervalMilliseconds:1000,timeoutMilliseconds:500,durationSeconds:11}},trialsPerBatch:3,retryLimit:1}}

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
  const before={nodeUID:"old",cpu:{limit:"2",affinityCPUs:[1,2]},activeProtocol:{dataset:{name:"vqa"}},runtimeImage:"same",preprocessor:{name:"image"}}
  const after={nodeUID:"new",cpu:{limit:"2",affinityCPUs:[3,4]},activeProtocol:{dataset:{name:"images"}},runtimeImage:"same"}
  const changes=requestConditionChanges(before,after)
  assert.deepEqual(changes.map(change=>change.field),["nodeUID","cpu.affinityCPUs","activeProtocol.dataset.name"])
  assert.deepEqual(changes[1],{field:"cpu.affinityCPUs",label:"CPU 코어 고정",before:"[1,2]",after:"[3,4]"})
  assert.deepEqual(requestConditionChanges(before,undefined),[])
  assert.match(requestPreflightReason({code:"VariantConditionChanged",message:"English warning"}),/기존.*제외.*확인/)
  assert.match(requestPreflightReason({code:"ImmutableImagesUnconfirmed",message:"English warning"}),/이미지.*확인/)
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

function renderRequest(props:Record<string,unknown>={},query="",error="",stage=0) {
  const source=readFileSync(new URL("../features/measurement-request.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(`${source}\nexport { Funnel };`,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports:Record<string,unknown>={}
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const modules:Record<string,unknown>={
    "react":{...require("react"),useState:(initial:unknown)=>{const state=require("react").useState(initial);return initial===""?[error,state[1]]:initial===0?[stage,state[1]]:state}},
    "next/link":{__esModule:true,default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},
    "next/navigation":{useRouter:()=>({replace:()=>{}}),useSearchParams:()=>new URLSearchParams(query)},
    "@/lib/client":{useChillMutation:()=>({isPending:false})},
    "@/components/read-region":{SourceJSON:()=>null},
    "@/components/screen":{Screen:container,Rows:container,RowDialog:()=>null},
    "@/features/read-pages":{},
    "@/features/resources":{powerModeText:()=>"고정"},
    "@/components/ui/button":{Button:({children,disabled,"aria-current":current}:{children?:ReactNode;disabled?:boolean;"aria-current"?:"step"})=>createElement("button",{disabled,"aria-current":current},children)},
    "@/components/ui/input":{Input:()=>null},
    "@/lib/measurement-request":requestHelpers,
    "@/lib/scheduler":{},
    "@/lib/measurement-coverage":coverageHelpers,
    "@/features/measurement-coverage":{useModelCompatibility:()=>({})},
    "@/components/measurement-navigation":{MeasurementNavigation:()=>null},
    "@/lib/measurements":measurementHelpers,
  }
  runInNewContext(compiled,{exports,require:(name:string)=>name in modules?modules[name]:require(name),URLSearchParams})
  const Funnel=exports.Funnel as ComponentType<Record<string,unknown>>
  return renderToStaticMarkup(createElement(Funnel,{
    namespace:"ns",from:null,checking:false,refetchPlans:async()=>({error:null}),
    context:{models:[],workloads:[],classes:[],plans:[],preprocessors:[],datasets:[],profiles:[]},
    prefill:{active:false,values:{},errors:[],returnTo:"/measurements",node:""},
    ...props,
  }))
}

test("request Funnel renders the undeclared-batch branch without a stale condition variable",()=>{
  const html=renderRequest()
  assert.match(html,/>배치 \(건\)</)
  assert.doesNotMatch(html,/NaN/)
  assert.match(html,/<th>예열 \(회\)<\/th>/)
  assert.match(html,/<td>—<\/td>/)
  assert.match(html,/<details id="measurement-conditions" open=""/)
  assert.match(html,/href="\/datasets\/new"/)
})

test("unmeasured request leaves a surplus-field-only dataset unselected and required",()=>{
  const context={models:[{metadata:{name:"opaque-id",uid:"model"},spec:{artifacts:[],executionPaths:[{name:"cpu",artifact:"onnx",runtime:{family:"firstfloor",backend:"ort-cpu"}}]}}],workloads:[],classes:[],plans:[],profiles:[],preprocessors:[{metadata:{name:"image-pre",uid:"pre"},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},artifacts:[{target:{model:"opaque-id",artifact:"onnx"}}]}}],datasets:[{metadata:{name:"vqa",uid:"data"},spec:{fields:[{name:"image",format:"JPEG",maxBytes:512},{name:"question",format:"UTF8",maxBytes:512}]}}]}
  const html=renderRequest({context,prefill:{active:true,values:{model:"opaque-id",path:"cpu",deviceClass:"",powerMode:""},node:"",errors:[],returnTo:"/measurements"}})
  assert.match(html,/데이터셋<select required=""/)
  assert.match(html,/<option value="" selected="">데이터셋 선택<\/option>/)
  assert.doesNotMatch(html,/<option value="vqa" selected=""/)
  assert.match(html,/<details id="measurement-conditions" open=""/)
})

test("copied request and selected variant keep their dataset instead of the newest same-model dataset",()=>{
  const newer={...plan,metadata:{...plan.metadata,name:"newer",uid:"newer-uid",creationTimestamp:"2026-11-01T00:00:00Z"},spec:{...plan.spec,activeProtocol:{...plan.spec.activeProtocol,dataset:"new-data"}}}
  const context={models:[],workloads:[],classes:[],profiles:[],plans:[plan,newer],preprocessors:[{metadata:{name:"pre"},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},artifacts:[{target:{model:condition.model,artifact:"onnx"}}]}}],datasets:["data","new-data"].map(name=>({metadata:{name},spec:{fields:[{name:"image",format:"JPEG",maxBytes:512}]}}))} as unknown as Parameters<typeof requestInputs>[4]
  assert.equal(requestInputs(condition.model,"onnx","pre","data",context,{preprocessor:true,dataset:true}).dataset,"data")
  assert.equal(requestInputs(condition.model,"onnx","pre","",context,{preprocessor:true,dataset:true}).dataset,"new-data")
  const selected={requestIds:["ns/source/uid"],batchProgress:{target:[1],measured:[],missing:[1]}}
  for(const props of [{context,from:"source"},{context,selected}]) {
    const html=renderRequest(props)
    assert.match(html,/<option value="data" selected="">data<\/option>/)
    assert.doesNotMatch(html,/<option value="new-data" selected=""/)
  }
})

test("required batches stay outside collapsed conditions and validation appears once",()=>{
  const issue="한 번에 처리할 건수를 하나 이상 골라 주세요."
  const html=renderRequest({},"",issue)
  const beforeBatches=html.slice(0,html.indexOf('id="measurement-batches"'))
  const detailTags=beforeBatches.match(/<\/?details\b[^>]*>/g)??[]
  const collapsed=detailTags.reduce((count,tag)=>count+(tag.startsWith("</")?-1:1),0)
  assert.equal(collapsed,0)
  assert.equal(html.split(issue).length-1,1)
  assert.match(html,/href="#measurement-size-error"/)
  assert.doesNotMatch(html,/>2147483647</)
  assert.match(html,/>제한 없음</)
})

test("variant prefill chooses missing batches while explicit URL batches, including empty, win",()=>{
  const context={models:[],workloads:[],classes:[],plans:[plan],preprocessors:[],datasets:[],profiles:[]}
  const selected={requestIds:["ns/source/uid"],batchProgress:{target:[1,2,4,8],measured:[1,2],missing:[4,8]}}
  const props={context,selected}
  const checked=(html:string)=>[...html.matchAll(/<input\b[^>]*>/g)].map(match=>match[0]).filter(input=>input.includes('checked=""')&&input.includes('name="batchSizes"')).map(input=>Number(input.match(/\bvalue="(\d+)"/)?.[1]))
  for(const draft of [props,{...props,context:{...context,plans:[]},prefill:{active:true,values:condition,errors:[],returnTo:"/measurements",node:condition.node}}]) {
    assert.deepEqual(checked(renderRequest(draft)),[4,8])
    assert.deepEqual(checked(renderRequest(draft,"batches=2")),[2])
    assert.deepEqual(checked(renderRequest(draft,"batches=")),[])
    assert.deepEqual(checked(renderRequest({...draft,selected:{...selected,batchProgress:{...selected.batchProgress,missing:[]}}})),[])
  }
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

test("creation stages keep target, conditions and check submission distinct",()=>{
  const target=renderRequest()
  assert.match(target,/aria-label="요청 단계"/)
  assert.match(target,/aria-current="step">대상/)
  assert.doesNotMatch(target,/>요청 범위 검사</)
  const review=renderRequest({},"","",2)
  assert.match(review,/aria-label="검사·제출"/)
  assert.match(review,/>요청 범위 검사</)
})

const retainedAssessment:MeasurementAssessment={status:"complete",displayStatus:"usable",label:"측정 완료",reasons:[],execution:null,evidence:{available:true,accepted:true,reasons:[]},checklist:{criteriaMet:null,version:"result-checklist.v1",checks:[]},scope:{schedulerUsed:true,reused:false,facts:[]}}
const retainedProgress={basis:"requested",target:[4],measured:[4],criteriaMet:[],missing:[],inProgress:[],unknown:[]}
const retainedRow:ResultRow={id:"retained",rowKind:"direct",profileId:"profile",sourceProfileId:null,batch:4,condition:{metricSource:{nodeName:"node"}},p95Milliseconds:12,latencySampleCount:100,idleWatts:2,activeWatts:3,totalJoulesPerItem:4,incrementalJoulesPerItem:1,confidence:null,activeTrialCount:3,idleTrialCount:3,completedItems:100,completedCalls:25,powerSource:null,assessment:retainedAssessment,sources:[],missingReasons:[],reuse:null}
const retainedCell:MeasurementCoverageCell={id:"cell",model:{name:condition.model,uid:"model-uid"},deviceClass:{name:condition.deviceClass,uid:"device-uid"},executionPath:condition.path,powerMode:condition.powerMode,support:{verdict:"Compatible",node:{name:"node",uid:"node-uid"},reasons:[]},assessment:retainedAssessment,variantCount:1,variants:[{id:"variant",condition:{metricSource:{nodeName:"node"}},assessment:retainedAssessment,batchProgress:retainedProgress,resultRows:[retainedRow],requestIds:[],runIds:[],profileIds:["profile"]}],batchProgress:retainedProgress,representative:null,failureSummary:[],history:[]}

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
  const data={campaign:{...plan.metadata,spec:plan.spec,phase:"Failed",conditions:[{type:"Complete",status:"False",reason:"NativePreparationFailed",message:"Pod image missing"}]},assessment,resultRows:[],result:{excludedTrials:[{run:{namespace:"ns",name:"failed-child",uid:"child"}}]}}
  const modules:Record<string,unknown>={
    "next/link":{__esModule:true,default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},
    "next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({push:()=>{}})},
    "@tanstack/react-query":{useQueryClient:()=>({})},
    "@/lib/client":{useChillQuery:()=>({data}),useChillMutation:()=>({})},
    "@/lib/measurement-request":requestHelpers,
    "@/lib/measurement-assessment":{assessmentReasons:()=>[]},
    "@/lib/measurement-coverage":coverageHelpers,
    "@/lib/measurements":measurementHelpers,
    "@/lib/scheduler":{},
    "@/features/measurement-coverage":{useMeasurementCoverage:()=>({data:{cells:[{...retainedCell,assessment}]}})},
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
  assert.match(html,/12\.00/)
  assert.match(html,/checks/)
  assert.match(html,/동일 조건 요청/)
  assert.match(html,/fromUID=uid/)
  assert.ok(html.indexOf("Pod image missing")<html.indexOf("측정 실패 · 요청 결과 없음"))
  assert.match(html,/<details><summary[^>]*>관리/)
})
