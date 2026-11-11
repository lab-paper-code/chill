import assert from "node:assert/strict"
import test from "node:test"
import { requestInputs } from "../lib/measurement-request.ts"
import type { MeasurementPlan } from "../lib/read-types.ts"
import type { MeasurementRequestDocument } from "../lib/measurement-document.ts"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { runInNewContext } from "node:vm"
import { createElement } from "react"
import type { ComponentType, ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as requestHelpers from "../lib/measurement-request.ts"
import * as coverageHelpers from "../lib/measurement-coverage.ts"
import * as measurementHelpers from "../lib/measurements.ts"
import * as measurementSummary from "../lib/measurement-summary.ts"

const condition={model:"model & one",deviceClass:"device",node:"node",path:"cuda",powerMode:"MAXN"}
const plan:MeasurementPlan={metadata:{name:"source",namespace:"ns",uid:"uid",resourceVersion:"1",creationTimestamp:"2026-10-30T00:00:00Z"},spec:{state:{candidate:{modelSpec:condition.model,deviceClass:condition.deviceClass,preprocessor:"pre",executionPath:condition.path,powerMode:condition.powerMode},targetNode:condition.node},activeProtocol:{warmupIterations:5,durationSeconds:10,dataset:"data",metricObservation:{intervalMilliseconds:1000,timeoutMilliseconds:500,durationSeconds:11}},idleProtocol:{settlingDurationSeconds:2,durationSeconds:10,metricObservation:{intervalMilliseconds:1000,timeoutMilliseconds:500,durationSeconds:11}},trialsPerBatch:3,retryLimit:1}}

function renderRequest(props:Record<string,unknown>={},query="",error="",stage=0,bodies:unknown[]=[],compatibility:Parameters<typeof coverageHelpers.serverCompatibility>[1]=[],mutations:Record<string,unknown>[]=[] ,patch?:Record<string,unknown>,unknown=false,interact?:(render:()=>ReactNode)=>ReactNode) {
  const source=readFileSync(new URL("../features/measurement-request.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(`${source}\nexport { Funnel };`,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url),exports:Record<string,unknown>={}
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  let mutationIndex=0,stateIndex=0
  const states:unknown[]=[]
  const modules:Record<string,unknown>={
    "react":{...require("react"),...(interact?{useEffect:()=>{},useRef:(current:unknown)=>{
      const index=stateIndex++
      if(!(index in states))states[index]={current}
      return states[index]
    }}:{}),useState:(initial:unknown)=>{
      const index=stateIndex++
      if(interact) {
        if(!(index in states))states[index]=typeof initial==="function"?(initial as ()=>unknown)():initial
        return [states[index],(value:unknown)=>{states[index]=typeof value==="function"?(value as (current:unknown)=>unknown)(states[index]):value}]
      }
      const state=require("react").useState(initial)
      if(index===3&&unknown)return [true,state[1]]
      if(patch&&state[0]&&typeof state[0]==="object"&&"workload" in state[0])return [requestHelpers.fillDefaults({...state[0],...patch} as requestHelpers.Draft,props.context as requestHelpers.Context),state[1]]
      return initial===""?[error,state[1]]:initial===0?[stage,state[1]]:state
    }},
    "next/link":{__esModule:true,default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},
    "next/navigation":{useRouter:()=>({replace:()=>{}}),useSearchParams:()=>new URLSearchParams(query?`${query}&input=form`:"input=form")},
    "@/lib/client":{useChillMutation:()=>({isPending:false,reset:()=>{},...mutations[mutationIndex++]}),isCoverageWarming:()=>false},
    "@/components/read-region":{SourceJSON:({inline,value}:{inline?:boolean;value:unknown})=>{if(inline)bodies.push(value);return null},SourceExport:()=>null},
    "@/components/measurement-request-source":{MeasurementRequestSource:({value}:{value:unknown})=>{bodies.push(value);return null}},
    "@/components/screen":{Screen:container,Rows:container,RowDialog:()=>null},
    "@/features/read-pages":{preflightConclusion:()=>"형식 검사 통과",PreflightAnswer:()=>null},
    "@/features/resources":{powerModeText:()=>"고정"},
    "@/components/ui/button":{Button:({children,disabled,"aria-current":current}:{children?:ReactNode;disabled?:boolean;"aria-current"?:"step"})=>createElement("button",{disabled,"aria-current":current},children)},
    "@/components/ui/input":{Input:()=>null},
    "@/lib/measurement-request":requestHelpers,
    "@/lib/scheduler":{},
    "@/lib/measurement-summary":measurementSummary,
    "@/lib/measurement-coverage":coverageHelpers,
    "@/hooks/use-measurement-data":{useModelCompatibility:()=>({data:compatibility})},
    "@/components/measurement-navigation":{MeasurementNavigation:()=>null},
    "@/features/measurement-document-request":{MeasurementDocumentRequestPage:()=>null},
    "@/components/measurement-preparation-guide":{MeasurementPreparationGuide:()=>null},
    "@/features/dispatch-occupancy-request":{MeasurementKindSelector:({disabled}:{disabled?:boolean})=>createElement("select",{"aria-label":"측정 종류",disabled},createElement("option",{value:"dispatch-occupancy"},"d · 분배 점유시간"))},
    "@/lib/measurements":measurementHelpers,
  }
  const submission:Record<string,unknown>={}
  const submissionCode=transpileModule(readFileSync(new URL("../hooks/use-measurement-submission.ts",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS}}).outputText
  runInNewContext(submissionCode,{exports:submission,require:(name:string)=>name in modules?modules[name]:require(name),URLSearchParams})
  modules["@/hooks/use-measurement-submission"]=submission
  runInNewContext(compiled,{exports,require:(name:string)=>name in modules?modules[name]:require(name),URLSearchParams})
  const Funnel=exports.Funnel as (props:Record<string,unknown>)=>ReactNode
  const funnelProps={
    namespace:"ns",from:null,checking:false,refetchPlans:async()=>({error:null}),
    context:{models:[],workloads:[],classes:[],plans:[],preprocessors:[],datasets:[],profiles:[]},
    prefill:{active:false,values:{},errors:[],returnTo:"/measurements",node:""},
    ...props,
  }
  const render=()=>{stateIndex=0;mutationIndex=0;return Funnel(funnelProps)}
  return renderToStaticMarkup(interact?interact(render):createElement(Funnel,funnelProps))
}

function elements(node:ReactNode):{type:unknown;props:Record<string,unknown>}[] {
  if(Array.isArray(node))return node.flatMap(elements)
  if(!node||typeof node!=="object"||!("props" in node))return []
  const element=node as {type:unknown;props:Record<string,unknown>}
  return [element,...elements(element.props.children as ReactNode)]
}
function change(render:()=>ReactNode,name:string,value:string) {
  const field=elements(render()).find(item=>item.props.name===name)
  assert.ok(field,`field ${name} exists`)
  ;(field.props.onChange as (event:unknown)=>void)({target:{value,valueAsNumber:Number(value)}})
}
const workloadContext={
  models:[{metadata:{name:condition.model,uid:"model"},spec:{artifacts:[],executionPaths:[{name:condition.path,artifact:"onnx",runtime:{family:"firstfloor",backend:"ort-cuda"}}]}}],
  workloads:[{desired:{metadata:{name:"chosen-workload",uid:"workload"},spec:{modelRef:{name:condition.model},preprocessorRef:{name:"pre"},datasetRef:{name:"data"},slo:{percentile:95,targetLatency:"1s"}}}}],
  classes:[],plans:[{...plan,spec:{...plan.spec,activeProtocol:{...plan.spec.activeProtocol,workload:"chosen-workload"}}}],profiles:[],
  preprocessors:["pre","other-pre"].map(name=>({metadata:{name,uid:name},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},artifacts:[{target:{model:condition.model,artifact:"onnx"}}]}})),
  datasets:["data","other-data"].map(name=>({metadata:{name,uid:name},spec:{fields:[{name:"image",format:"JPEG",maxBytes:512}]}})),
}
test("explicit Workload selection fills desired refs and emits its name in the actual request",()=>{
  const bodies:unknown[]=[]
  renderRequest({context:workloadContext},"","",0,bodies,[],[],undefined,false,render=>{
    change(render,"workload","chosen-workload")
    return render()
  })
  const body=JSON.parse(JSON.stringify(bodies.at(-1)))
  assert.equal(body.spec.activeProtocol.workload,"chosen-workload")
  assert.equal(body.spec.state.candidate.modelSpec,condition.model)
  assert.equal(body.spec.state.candidate.preprocessor,"pre")
  assert.equal(body.spec.activeProtocol.dataset,"data")
})

test("the guided form submits current conditions without diagnostics despite catalog metadata changes",()=>{
  const context={...workloadContext,
    models:workloadContext.models.map(model=>({...model,metadata:{...model.metadata}})),
    preprocessors:workloadContext.preprocessors.map(item=>({...item,metadata:{...item.metadata}})),
    classes:[{name:condition.deviceClass,uid:"class",resourceVersion:"1",architecture:"amd64",powerModes:[{name:condition.powerMode}],nodes:[{name:condition.node,uid:"node",ready:"True",schedulingDisabled:false}]}],
  }
  const submitted:{path:string;body:MeasurementRequestDocument}[]=[]
  renderRequest({context,from:"source"},"from=source","",0,[],[],[{},{},{mutate:(request:typeof submitted[number])=>submitted.push(request)}],undefined,false,render=>{
    for(let index=0;index<2;index++) {
      const next=elements(render()).find(item=>item.props.children==="다음")
      assert.ok(next)
      assert.equal(next.props.disabled,false)
      ;(next.props.onClick as ()=>void)()
    }
    assert.ok(elements(render()).some(item=>item.props["aria-label"]==="검사·제출"))
    context.classes[0].resourceVersion="2"
    context.models[0].metadata.uid="replacement-model"
    context.preprocessors[0].metadata.uid="replacement-preprocessor"
    const action=elements(render()).find(item=>item.props.children==="측정 요청")
    assert.ok(action)
    assert.equal(action.props.disabled,false)
    ;(action.props.onClick as ()=>void)()
    return render()
  })
  assert.equal(submitted.at(-1)?.path,"v1/namespaces/ns/batch-energy-optimizations")
  assert.equal((submitted.at(-1)?.body.spec.activeProtocol as Record<string,unknown>).workload,"chosen-workload")
})
test("defaults and a uniquely matching profile never choose a Workload; missing selection renders an error",()=>{
  const profile={id:"profile",nodeName:condition.node,measuredAt:"2026-11-05",condition:{logicalModel:condition.model,deviceClass:condition.deviceClass,powerMode:condition.powerMode,artifact:{executionPath:condition.path},preprocessor:{name:"pre"},activeProtocol:{workloadName:"chosen-workload",warmupIterations:5,durationSeconds:30,dataset:{name:"data"},metricIntervalMilliseconds:1000,metricTimeoutMilliseconds:500,metricDurationSeconds:30},idleProtocol:{settlingDurationSeconds:2,durationSeconds:10,metricIntervalMilliseconds:1000,metricTimeoutMilliseconds:500,metricDurationSeconds:11}}}
  for(const context of [workloadContext,{...workloadContext,plans:[],profiles:[profile]},{...workloadContext,workloads:[]}]) {
    const bodies:unknown[]=[],sent:unknown[]=[]
    const html=renderRequest({context,prefill:{active:true,values:condition,node:condition.node,errors:[],returnTo:"/measurements"}},"","",0,bodies,[],[{mutate:(body:unknown)=>sent.push(body)},{mutate:(body:unknown)=>sent.push(body)},{}],undefined,false,render=>{
      const next=elements(render()).find(item=>item.props.children==="다음")
      assert.ok(next)
      ;(next.props.onClick as ()=>void)()
      return render()
    })
    const body=JSON.parse(JSON.stringify(bodies.at(-1)))
    assert.ok(!body.spec.activeProtocol.workload)
    assert.match(html,/id="measurement-workload-error"[^>]*>[^<]+/)
    assert.match(html,/role="alert"/)
    assert.equal(sent.length,0)
  }
})
test("copy changes to model, preprocessor or dataset discard the old Workload",()=>{
  for(const [name,value] of [["model","other-model"],["preprocessor","other-pre"],["dataset","other-data"]]) {
    const bodies:unknown[]=[]
    renderRequest({context:workloadContext,from:"source"},"from=source","",0,bodies,[],[],undefined,false,render=>{
      change(render,name,value)
      return render()
    })
    const body=JSON.parse(JSON.stringify(bodies.at(-1)))
    assert.ok(!body.spec.activeProtocol.workload)
  }
})
test("node and path-only copied requests retain the chosen Workload",()=>{
  const context=workloadContext as unknown as requestHelpers.Context
  for(const patch of [{node:"other-node"},{path:"other-path"},{deviceClass:"other-class"},{powerMode:"other-power"}]) {
    const copied=requestHelpers.begin("source",context).draft
    const body=requestHelpers.manifest(requestHelpers.updateDraft(copied,patch,context),context,"ns")
    assert.equal(body.spec.activeProtocol.workload,"chosen-workload")
  }
})
test("Workload preflight rejection is rendered through the existing server error path",()=>{
  const reason="workload chosen-workload percentile is outside finite sample coverage"
  const html=renderRequest({context:workloadContext,from:"source"},"from=source","",2,[],[],[{}, {data:{status:200,data:{resolves:false,reason}}}, {}])
  assert.ok(html.includes(reason))
  assert.match(html,/id="measurement-rejection"[^>]*role="alert"/)
})

test("final POST preflight rejection is visible without optional diagnostics",()=>{
  const reason="workload chosen-workload percentile is outside finite sample coverage"
  const html=renderRequest({context:workloadContext,from:"source"},"from=source","",2,[],[],[{}, {}, {error:{status:422,body:"Final request rejected",data:{code:"MeasurementPreflightRejected",reason}}}])
  assert.ok(html.includes(reason))
  assert.ok(html.includes("MeasurementPreflightRejected"))
  assert.match(html,/id="measurement-rejection"[^>]*role="alert"/)
})


test("unmeasured request leaves a surplus-field-only dataset unselected and required",()=>{
  const context={models:[{metadata:{name:"opaque-id",uid:"model"},spec:{artifacts:[],executionPaths:[{name:"cpu",artifact:"onnx",runtime:{family:"firstfloor",backend:"ort-cpu"}}]}}],workloads:[],classes:[],plans:[],profiles:[],preprocessors:[{metadata:{name:"image-pre",uid:"pre"},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},artifacts:[{target:{model:"opaque-id",artifact:"onnx"}}]}}],datasets:[{metadata:{name:"vqa",uid:"data"},spec:{fields:[{name:"image",format:"JPEG",maxBytes:512},{name:"question",format:"UTF8",maxBytes:512}]}}]}
  const html=renderRequest({context,prefill:{active:true,values:{model:"opaque-id",path:"cpu",deviceClass:"",powerMode:""},node:"",errors:[],returnTo:"/measurements"}})
  assert.match(html,/데이터셋<select[^>]*required=""/)
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

test("new and copied requests construct CPU-free manifests with the complete measured protocol",()=>{
  const source={...plan,spec:{...plan.spec,retryLimit:2,pointPolicy:{confidenceLevel:"0.9"},activeProtocol:{...plan.spec.activeProtocol,durationToleranceSeconds:0.5},idleProtocol:{...plan.spec.idleProtocol,durationToleranceSeconds:0.25}}}
  const context={models:[],workloads:[],classes:[],profiles:[],plans:[source],preprocessors:[{metadata:{name:"pre"},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},artifacts:[{target:{model:condition.model,artifact:"onnx"}}]}}],datasets:[{metadata:{name:"data"},spec:{fields:[{name:"image",format:"JPEG",maxBytes:512}]}}]}
  const prefill={active:true,values:condition,node:condition.node,errors:[],returnTo:"/measurements"}
  for(const props of [{context,prefill},{context,from:"source"}]) {
    const bodies:unknown[]=[]
    renderRequest(props,"","",1,bodies)
    const body=JSON.parse(JSON.stringify(bodies.at(-1)))
    assert.equal(body.apiVersion,"edge.dacs.io/v1alpha1")
    assert.equal(body.kind,"BatchEnergyOptimization")
    assert.equal(body.metadata.namespace,"ns")
    assert.deepEqual(body.spec,{...source.spec,activeProtocol:{...source.spec.activeProtocol,workload:""}})
    assert.ok(!Object.hasOwn(body.spec,"batchSizes"))
    assert.equal(source.spec.retryLimit,2)
  }
})

test("automatic requests ignore legacy URL batches and variant batch progress without offering fixed-K fields",()=>{
  const context={...workloadContext}
  const selected={requestIds:["ns/source/uid"],batchProgress:{target:[1,2,4,8],measured:[1,2],missing:[4,8]}}
  for(const query of ["","batches=2","batches="]) {
    const bodies:unknown[]=[]
    const html=renderRequest({context,from:"source",selected},query,"",1,bodies)
    const body=JSON.parse(JSON.stringify(bodies.at(-1)))
    assert.ok(!Object.hasOwn(body.spec,"batchSizes"))
    assert.equal(body.spec.retryLimit,plan.spec.retryLimit)
    assert.doesNotMatch(html,/name="batchSizes"/)
  }
})

test("preflight success and admission transport failure identify their own checks without HTTP zero",()=>{
  const html=renderRequest({},"","",2,[],[],[{error:{status:0,data:null,body:"No response"}},{data:{status:200,data:{resolves:true}}},{}])
  assert.match(html,/접수 검사/)
  assert.match(html,/실행 계획 검사/)
  assert.match(html,/응답 미수신/)
  assert.match(html,/HTTP 200/)
  assert.match(html,/형식 검사 통과/)
  assert.doesNotMatch(html,/HTTP 0|연결 상태를 확인/)
})

test("editing a copied name or trial count preserves the original protocol instead of a newer same-target request",()=>{
  const original={...plan,spec:{...plan.spec,pointPolicy:{confidenceLevel:"0.9"},activeProtocol:{...plan.spec.activeProtocol,durationToleranceSeconds:0.5}}}
  const newer={...original,metadata:{...original.metadata,name:"newer",uid:"newer",creationTimestamp:"2026-11-02"},spec:{...original.spec,pointPolicy:{confidenceLevel:"0.99"},activeProtocol:{...original.spec.activeProtocol,durationSeconds:99,durationToleranceSeconds:1}}}
  const context={models:[],workloads:[],classes:[],profiles:[],plans:[original,newer],preprocessors:[{metadata:{name:"pre"},spec:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1024}]},artifacts:[{target:{model:condition.model,artifact:"onnx"}}]}}],datasets:[{metadata:{name:"data"},spec:{fields:[{name:"image",format:"JPEG",maxBytes:512}]}}]}
  for(const patch of [{name:"copy-edited"},{trials:5}]) {
    const bodies:unknown[]=[]
    renderRequest({context,from:"source"},"from=source","",1,bodies,[],[],patch)
    const body=JSON.parse(JSON.stringify(bodies.at(-1)))
    assert.deepEqual(body.spec.activeProtocol,{...original.spec.activeProtocol,workload:""})
    assert.deepEqual(body.spec.pointPolicy,original.spec.pointPolicy)
  }
})

test("creation stages keep target, conditions and check submission distinct",()=>{
  const target=renderRequest()
  assert.match(target,/aria-label="요청 단계"/)
  assert.match(target,/aria-current="step">대상/)
  assert.doesNotMatch(target,/>선택 사전검사</)
  const review=renderRequest({},"","",2)
  assert.match(review,/aria-label="검사·제출"/)
  assert.match(review,/>선택 사전검사</)
})
test("copied source identity is reviewable without a UID query and replacement blocks submission",()=>{
  const context={models:[],workloads:[],classes:[],plans:[plan],preprocessors:[],datasets:[],profiles:[]}
  const html=renderRequest({context,from:"source"},"from=source","",2)
  assert.match(html,/원본 저장 영역<\/th><td[^>]*>ns</)
  assert.match(html,/원본 UID<\/th><td[^>]*>uid</)
  const mismatch=renderRequest({context,from:"source"},"from=source&fromUID=old","",2)
  assert.match(mismatch,/복사 원본.*식별자/)
  assert.match(mismatch,/<button disabled="">선택 사전검사<\/button>/)
})

test("advanced timing shows actual measurement and observation windows without inventing warmup seconds",()=>{
  const context={models:[],workloads:[],classes:[],plans:[plan],preprocessors:[],datasets:[],profiles:[]}
  const html=renderRequest({context,from:"source"},"from=source","",1)
  assert.match(html,/aria-label="측정 시간과 전력 기록 창"/)
  assert.match(html,/실행 측정<\/th><td[^>]*>10<\/td><td[^>]*>11<\/td>/)
  assert.match(html,/유휴 측정<\/th><td[^>]*>10<\/td><td[^>]*>11<\/td>/)
  assert.match(html,/예열 시간.*미제공/)
  const unknown=renderRequest()
  assert.doesNotMatch(unknown,/width:NaN|width:Infinity/)
})
test("compatibility diagnostics are displayed without adding a request gate",()=>{
  const selection={modelSpec:"model",executionPath:"cpu",deviceClass:"class",node:"node"}
  const object={name:"object",uid:"uid",resourceVersion:"1"}
  const reads:Parameters<typeof coverageHelpers.serverCompatibility>[1]=[{node:"node",data:{readAt:"2026-11-03T00:00:00Z",selection,inputs:{modelSpec:object,deviceClass:object,node:object,runtimeDeclaration:null},verdict:"Compatible",reasons:[],diagnostics:["Last runtime entry selected."]}}]
  const html=renderRequest({},"","",0,[],reads)
  assert.match(html,/aria-label="실행 선언 진단"/)
  assert.match(html,/node: Last runtime entry selected\./)
  assert.equal((html.match(/disabled=""/g)??[]).length,(renderRequest().match(/disabled=""/g)??[]).length)
})
test("P2 profile kind selector survives profile-only dependency failures and coverage loading",()=>{
  const source=readFileSync(new URL("../features/measurement-request.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const require=createRequire(import.meta.url)
  for(const dependency of ["v1/preprocessors","v1/datasets","profiles","coverage"]) {
    const exports:Record<string,unknown>={}
    const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
    const modules:Record<string,unknown>={
      "next/navigation":{useSearchParams:()=>new URLSearchParams(dependency==="coverage"?"input=form&variantId=selected":"input=form"),useRouter:()=>({replace:()=>{}})},
      "@/lib/client":{useSession:()=>({config:{namespace:"ns"},token:"fixture"}),useChillQuery:(path:string)=>path===dependency?{error:{status:503}}:{data:{items:[]}}},
      "@/hooks/use-measurement-data":{useRequestProfileHistory:()=>dependency==="profiles"?{error:{status:503},isPending:false}:{data:{profiles:[]},isPending:false},useMeasurementPlans:()=>({data:{items:[]}}),useCoverageInventory:()=>({data:{classes:[]}}),useMeasurementCoverage:()=>({})},
      "@/components/read-region":{QueryGate:({query,children}:{query:{error?:unknown};children?:ReactNode})=>query.error?createElement("p",null,"조회 실패"):children},
      "@/components/screen":{Screen:container},
      "@/components/measurement-navigation":{MeasurementNavigation:()=>null},
      "@/features/dispatch-occupancy-request":{MeasurementKindSelector:({disabled}:{disabled?:boolean})=>createElement("select",{"aria-label":"측정 종류",disabled},createElement("option",{value:"dispatch-occupancy"},"d · 분배 점유시간"))},
    }
    runInNewContext(compiled,{exports,require:(name:string)=>name in modules?modules[name]:name.startsWith("@/")?{}:require(name),URLSearchParams})
    const html=renderToStaticMarkup(createElement(exports.MeasurementRequestPage as ComponentType))
    assert.match(html,/<select aria-label="측정 종류">/,dependency)
    assert.match(html,/value="dispatch-occupancy"/,dependency)
    if(dependency==="profiles")assert.match(html,/조회 실패/,dependency)
  }
})

test("P2 profile kind transition remains blocked during all mutations and unknown submission",()=>{
  assert.match(renderRequest(),/<select aria-label="측정 종류">/)
  for(let index=0;index<3;index++) {
    const mutations=Array.from({length:3},(_,position)=>({isPending:position===index}))
    assert.match(renderRequest({},"","",0,[],[],mutations),/<select aria-label="측정 종류" disabled="">/)
  }
  assert.match(renderRequest({},"","",0,[],[],[],undefined,true),/<select aria-label="측정 종류" disabled="">/)
})

test("failed list refresh displays the error while a pinned unknown submission stays blocked",()=>{
  const plans={error:{status:503}}
  const context={models:[],workloads:[],classes:[],profiles:[],plans:[plan],preprocessors:[],datasets:[]}
  const html=renderRequest({context,from:"source",plansError:plans.error},"from=source","",2,[],[],[],{name:"fixed-unknown-name"},true)
  assert.match(html,/측정 요청 목록 갱신 실패 \(HTTP 503\)/)
  assert.match(html,/fixed-unknown-name: 자동 재제출·이름 변경 차단/)
  assert.match(html,/<button[^>]*disabled=""[^>]*>측정 요청<\/button>/)
})
