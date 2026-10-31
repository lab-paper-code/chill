import assert from "node:assert/strict"
import test from "node:test"
import { datasetGap, requestNumber, requestBatches, requestDefaults, requestInputs, requestInitialBatches, requestTargetBatches, requestConditionChanges, requestPreflightReason, requestRejectionGuidance, requestOperationalSummary } from "./measurement-request.ts"
import type { MeasurementPlan, ProfilesView } from "./read-types.ts"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { runInNewContext } from "node:vm"
import { createElement } from "react"
import type { ComponentType, ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit } from "typescript"
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

test("automatic dataset selection accepts a subset input contract just like modelcatalog.Compatible",()=>{
  const context={preprocessors:[{metadata:{name:"image-pre"},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},artifacts:[{target:{model:"opaque-id",artifact:"onnx"}}]}}],datasets:[{metadata:{name:"vqa"},spec:{fields:[{name:"image",format:"JPEG",maxBytes:512},{name:"question",format:"UTF8",maxBytes:512}]}}]} as Parameters<typeof requestInputs>[4]
  assert.deepEqual(requestInputs("opaque-id","onnx","","",context),{preprocessor:"image-pre",dataset:"vqa",warning:""})
  assert.equal(requestInputs("opaque-id","onnx","image-pre","vqa",context,{preprocessor:true,dataset:true}).dataset,"vqa")
  const full={...context,datasets:[...context.datasets,{metadata:{name:"images"},spec:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]}}]} as Parameters<typeof requestInputs>[4]
  assert.equal(requestInputs("opaque-id","onnx","image-pre","vqa",full,{dataset:true}).dataset,"vqa")
  assert.equal(requestInputs("opaque-id","onnx","","",full).dataset,"")
  assert.match(requestInputs("opaque-id","onnx","","",full).warning,/여러 개/)
  assert.equal(requestInputs("other-model","onnx","image-pre","vqa",full,{preprocessor:true,dataset:true}).preprocessor,"")
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

test("request Funnel renders the undeclared-batch branch without a stale condition variable",()=>{
  const source=readFileSync(new URL("../features/measurement-request.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(`${source}\nexport { Funnel };`,{compilerOptions:{module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports:Record<string,unknown>={}
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const modules:Record<string,unknown>={
    "next/link":{__esModule:true,default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},
    "next/navigation":{useRouter:()=>({replace:()=>{}}),useSearchParams:()=>new URLSearchParams()},
    "@/lib/client":{useChillMutation:()=>({isPending:false})},
    "@/components/read-region":{SourceJSON:()=>null},
    "@/components/screen":{Screen:container,Rows:container,RowDialog:()=>null},
    "@/features/read-pages":{},
    "@/features/resources":{powerModeText:()=>"고정"},
    "@/components/ui/button":{Button:container},
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
  const html=renderToStaticMarkup(createElement(Funnel,{
    namespace:"ns",from:null,checking:false,refetchPlans:async()=>({error:null}),
    context:{models:[],workloads:[],classes:[],plans:[],preprocessors:[],datasets:[],profiles:[]},
    prefill:{active:false,values:{},errors:[],returnTo:"/measurements",node:""},
  }))
  assert.match(html,/CPU·데이터셋·측정 방식이 같은 기존 결과가 있는지 검사해요/)
  assert.doesNotMatch(html,/NaN/)
  assert.match(html,/<th>예열 \(회\)<\/th>/)
  assert.match(html,/<td>—<\/td>/)
  assert.doesNotMatch(html,/<details open=""/)
  assert.match(html,/href="\/datasets\/new"/)
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
  assert.deepEqual(requestOperationalSummary(undefined),{servingImpact:"미제공",estimatedDurationSeconds:null})
  assert.deepEqual(requestOperationalSummary({servingImpact:"CPU 점유",estimatedDurationSeconds:90}),{servingImpact:"CPU 점유",estimatedDurationSeconds:90})
})
