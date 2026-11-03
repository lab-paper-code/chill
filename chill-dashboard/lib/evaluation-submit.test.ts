import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import * as React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import { setImmediate } from "node:timers/promises"
import * as requestHelpers from "./evaluation-request.ts"
import * as authorHelpers from "./evaluation-author.ts"
import * as scheduler from "./scheduler.ts"
import * as activeHelpers from "./evaluation-active.ts"
import * as measurementHelpers from "./measurements.ts"
import { STATUS_AXES } from "./status.ts"
import * as routes from "./dashboard-routes.ts"
import type { EvaluationSubmit } from "../features/evaluation-request.tsx"

const require=createRequire(import.meta.url)
class ApiError extends Error {
  status:number
  unknownOutcome:boolean
  constructor(status:number,unknownOutcome=false){super("test response");this.status=status;this.unknownOutcome=unknownOutcome}
}

test("manual evaluation replacement re-read unlocks deleted and unchanged drafts and confirms a new UID",async()=>{
  for(const state of ["deleted","unchanged","updated"] as const) {
    const values:unknown[]=[],buttons=new Map<string,Record<string,unknown>>()
    let cursor=0,mutationIndex=0,reads=0,submitted=""
    const spec={question:"retained draft"},pre:{data?:{data:unknown};isPending:boolean;error:null;mutate:()=>void}={isPending:false,error:null,mutate:()=>{pre.data={data:{checks:[{id:"valid",passed:true}],liveTarget:[]}}}}
    const send={isPending:false,error:null,mutateAsync:async()=>{throw new ApiError(0,true)}}
    const lookup={data:undefined as unknown,error:null,isFetching:false,refetch:async()=>{
      reads++
      if(reads===1)return {data:undefined,error:new ApiError(503)}
      return state==="deleted"?{data:undefined,error:new ApiError(404)}:{data:{name:"run",uid:state==="updated"?"new":"old",spec},error:null}
    }}
    const Button=(props:Record<string,unknown>)=>{buttons.set(String(props.children),props);return React.createElement("button",{disabled:props.disabled as boolean},props.children as React.ReactNode)}
    const modules:Record<string,unknown>={
      react:{...React,useState:(initial:unknown)=>{const index=cursor++;if(!(index in values))values[index]=typeof initial==="function"?initial():initial;return [values[index],(next:unknown)=>{values[index]=typeof next==="function"?next(values[index]):next}]}},
      "@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{}})},
      "@/lib/client":{ApiError,useChillMutation:()=>mutationIndex++===0?pre:send,useChillQuery:()=>lookup},
      "@/lib/evaluation-request":requestHelpers,"@/lib/evaluation-author":authorHelpers,"@/lib/scheduler":scheduler,
      "@/lib/experiments":{},"@/features/evaluation-author":{},"@/features/evaluation-active":{},"@/lib/measurements":measurementHelpers,"@/components/ui/button":{Button},
      "@/components/read-region":{},"@/components/screen":{},"@/components/ui/input":{},"@/components/ui/dialog":{},
      "@/components/ui/tooltip":{},
      "next/link":{default:({children,href}:{children:React.ReactNode;href:string})=>React.createElement("a",{href},children)},"next/navigation":{},
    }
    const source=readFileSync(new URL("../features/evaluation-request.tsx",import.meta.url),"utf8")
    const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
    const exports:{EvaluationSubmit?:typeof EvaluationSubmit}={}
    new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports)
    function render() {
      cursor=0;mutationIndex=0;buttons.clear()
      return renderToStaticMarkup(exports.EvaluationSubmit!({name:"run",spec,existing:{name:"run",uid:"old"},onSubmitted:value=>{submitted=value.uid}}))
    }
    render();(buttons.get("사전 점검")!.onClick as ()=>void)();render()
    await (buttons.get("수정 제출")!.onClick as ()=>Promise<void>)()
    // The click handler starts the async operation without returning its promise.
    await setImmediate();assert.match(render(),/결과 미확인/)
    await (buttons.get("다시 조회")!.onClick as ()=>Promise<void>)()
    await setImmediate()
    const html=render()
    if(state==="updated")assert.equal(submitted,"new")
    else {
      assert.match(html,state==="deleted"?/삭제됨·재생성 필요/:/미변경/)
      assert.equal(buttons.get("사전 점검")?.disabled,false)
      assert.deepEqual(spec,{question:"retained draft"})
    }
    assert.ok(reads>=2)
  }
})

test("active evaluation injection table translates wire action names",()=>{
  const container=({children}:{children?:React.ReactNode})=>React.createElement("div",null,children)
  const active={items:[{name:"run",uid:"uid",stageIndex:1,stagesTotal:1,stage:{id:"base",plannedSeconds:60,injections:[{id:"kill",action:"delete-serving-pod",offsetSeconds:0},{id:"cordon",action:"cordon-delete",offsetSeconds:0},{id:"restore",action:"uncordon",offsetSeconds:10}]}}]}
  const modules:Record<string,unknown>={
    "@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/evaluations/active"?active:{items:[]}}),useChillMutation:()=>({})},
    "@/lib/evaluation-active":activeHelpers,
    "@/lib/status":{STATUS_AXES},"@/components/status-cell":{StatusCell:container},
    "@/components/ui/dialog":{Dialog:container,DialogContent:container,DialogDescription:container,DialogHeader:container,DialogTitle:container},
  }
  const source=readFileSync(new URL("../features/evaluation-active.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:{EvaluationActive?:React.ComponentType<{now:number}>}={}
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports)
  const html=renderToStaticMarkup(React.createElement(exports.EvaluationActive!,{now:0}))
  assert.match(html,/노드 차단·Pod 삭제/)
  assert.match(html,/노드 차단 해제/)
  assert.match(html,/서비스 Pod 삭제/)
})

test("compact experiment status keeps cleanup, running, empty and denied states separate",()=>{
  let answer:unknown
  const modules:Record<string,unknown>={
    "@/lib/client":{useChillQuery:()=>answer},
    "@/lib/evaluation-active":activeHelpers,
    "@/lib/status":{STATUS_AXES},
    "@/components/status-cell":{StatusCell:({axis,label}:{axis:string;label:string})=>React.createElement("span",{"data-axis":axis},label)},
    "@/components/ui/dialog":{},
  }
  const source=readFileSync(new URL("../features/evaluation-active.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:{EvaluationStatusCell?:React.ComponentType}={}
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports)
  for(const [phases,label] of [[[],"활성 없음"],[["Running"],"실행 중"],[["Running","Finishing"],"정리 중"],[["Running","Blocked"],"정리 실패"],[["Pending"],"시작 전"]] as [string[],string][]) {
    answer={data:{items:phases.map((phase,index)=>({name:String(index),uid:String(index),phase}))}}
    const html=renderToStaticMarkup(React.createElement(exports.EvaluationStatusCell!))
    assert.match(html,/data-axis="실험 상태"/)
    assert.ok(html.includes(label))
  }
  answer={error:{status:403}}
  assert.match(renderToStaticMarkup(React.createElement(exports.EvaluationStatusCell!)),/권한 없음/)
})

test("return observation requires matching UID and explicit pool release, not request acceptance",()=>{
  let observed={name:"run",uid:"old",conditions:[{type:"PoolAcquired",status:"True"}]}
  const container=({children}:{children?:React.ReactNode})=>React.createElement("div",null,children)
  const modules:Record<string,unknown>={
    "@tanstack/react-query":{useQueryClient:()=>({})},
    "@/lib/client":{useChillQuery:()=>({data:observed}),useChillMutation:()=>({isSuccess:true})},
    "@/lib/evaluation-request":requestHelpers,"@/lib/evaluation-author":authorHelpers,"@/lib/scheduler":scheduler,
    "@/lib/experiments":{},"@/lib/measurements":measurementHelpers,"@/features/evaluation-active":{},
    "@/components/ui/button":{Button:container},"@/components/ui/input":{Input:container},
    "@/components/ui/dialog":{Dialog:container,DialogContent:container,DialogHeader:container,DialogTitle:container},
    "@/components/ui/tooltip":{},
    "@/components/read-region":{},"@/components/screen":{},
  }
  const source=readFileSync(new URL("../features/evaluation-request.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:{EvaluationCleanup?:React.ComponentType<{run:unknown}>}={}
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports)
  const run={definition:{run:{name:"run",uid:"old"}},outcome:{cleanup:{status:"Failed"}}}
  const render=()=>renderToStaticMarkup(React.createElement(exports.EvaluationCleanup!,{run}))
  assert.match(render(),/확인 접수/)
  assert.match(render(),/점유 중/)
  assert.doesNotMatch(render(),/반환 관측/)
  observed={...observed,conditions:[{type:"PoolAcquired",status:"False"}]}
  assert.match(render(),/반환 관측/)
  observed={...observed,uid:"new"}
  assert.doesNotMatch(render(),/반환 관측/)
  assert.match(render(),/미확인/)
})

type Element={type:unknown;props:Record<string,unknown>}
function elements(node:unknown):Element[] {
  if(Array.isArray(node))return node.flatMap(elements)
  if(!node||typeof node!=="object"||!("props" in node))return []
  const element=node as Element
  return [element,...elements(element.props.children),...elements(element.props.action)]
}
function hookRunner() {
  const values:unknown[]=[]
  let cursor=0
  const effects:(()=>void)[]=[]
  return {
    effects,
    reset(){values.length=0},
    react:{...React,useState(initial:unknown) {
      const index=cursor++
      if(!Object.hasOwn(values,index))values[index]=typeof initial==="function"?initial():initial
      return [values[index],(next:unknown)=>{values[index]=typeof next==="function"?next(values[index]):next}]
    },useEffect(effect:()=>void){effects.push(effect)}},
    render(Component:(props:Record<string,unknown>)=>unknown,props:Record<string,unknown>={}) {cursor=0;effects.length=0;return Component(props)},
  }
}
function loadUI(path:string,modules:Record<string,unknown>={}) {
  const defaults:Record<string,unknown>={
    "@/lib/evaluation-request":requestHelpers,"@/lib/evaluation-author":authorHelpers,"@/lib/scheduler":scheduler,
    "@/lib/experiments":{},"@/lib/measurements":measurementHelpers,"@/features/evaluation-active":{EvaluationActive:"active"},
    "@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{}})},
    "@/components/screen":{Screen:"main"},"@/components/read-region":{QueryGate:"gate",SourceJSON:"source"},
    "@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},
    "@/components/ui/dialog":{Dialog:"dialog",DialogContent:"div",DialogHeader:"header",DialogTitle:"h2",DialogDescription:"p"},
    "@/components/ui/tooltip":{Tooltip:"tooltip",TooltipProvider:"provider",TooltipTrigger:"trigger",TooltipContent:"content"},
    "next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({replace:()=>{}})},
  }
  const source=readFileSync(new URL(path,import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:Record<string,(props:Record<string,unknown>)=>unknown>={}
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:name in defaults?defaults[name]:name.startsWith("@/")?{}:require(name),exports)
  return exports
}

test("execution list retains every server phase and links to UID-scoped child pages",()=>{
  const runner=hookRunner(),items=["Pending","Running","Finishing","Blocked","Done"].map(phase=>({name:phase,uid:`uid-${phase}`,phase,spec:{}}))
  const calls:string[]=[]
  const {EvaluationsPage}=loadUI("../features/evaluation-request.tsx",{react:runner.react,"@/lib/client":{useChillQuery:(path:string)=>{calls.push(path);return {data:{items}}}}})
  const tree=elements(runner.render(EvaluationsPage))
  assert.deepEqual(calls,["v1/evaluations"])
  for(const run of items)assert.ok(tree.some(item=>item.type==="a"&&item.props.href===`/experiments/runs/${run.name}?uid=${run.uid}`))
  assert.ok(tree.some(item=>item.type==="a"&&item.props.href==="/experiments?view=published&offset=0"))
  assert.ok(!tree.some(item=>"run" in item.props||item.type==="active"))
})

test("execution child detail separates list, navigation and one control surface",()=>{
  const runner=hookRunner(),params=new URLSearchParams("uid=old&offset=5&service=workload"),navigations:string[]=[],calls:string[]=[]
  let run={name:"run/one",uid:"old",phase:"Running",spec:{},currentStage:"stage-a",publicationID:""}
  const {EvaluationRunPage}=loadUI("../features/evaluation-request.tsx",{
    react:runner.react,"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:(url:string)=>navigations.push(url)})},
    "@/lib/client":{useChillQuery:(path:string)=>{calls.push(path);return {data:run,refetch:async()=>{}}}},
  })
  let tree=elements(runner.render(EvaluationRunPage,{name:run.name}))
  assert.deepEqual(calls,["v1/evaluations/run%2Fone"])
  assert.ok(!tree.some(item=>item.props["aria-label"]==="실험 실행 목록"))
  assert.ok(tree.some(item=>item.type==="a"&&item.props["aria-current"]==="page"&&item.props.children==="진행"))
  assert.ok(tree.some(item=>item.props["aria-disabled"]===true&&item.props.children==="결과 · 미게시"))
  assert.equal(tree.find(item=>item.type==="active")?.props.controls,false)
  assert.equal(tree.find(item=>item.type==="active")?.props.uid,"old")
  run={...run,publicationID:"actual/publication"}
  tree=elements(runner.render(EvaluationRunPage,{name:run.name}))
  runner.effects[0]?.()
  assert.equal(navigations.length,0,"publication must not auto-switch the screen")
  assert.ok(tree.some(item=>item.type==="a"&&item.props.href==="/experiments/results?offset=5&service=workload&a=actual%2Fpublication"))
  run={...run,uid:"replacement"}
  tree=elements(runner.render(EvaluationRunPage,{name:run.name}))
  assert.ok(tree.some(item=>item.props.children==="실행 교체됨"))
  assert.ok(!tree.some(item=>"run" in item.props||item.type==="active"),"old UID must expose no replacement mutations")
  assert.ok(tree.some(item=>item.type==="a"&&item.props.href==="/experiments/runs/run%2Fone?offset=5&service=workload&uid=replacement"))
  params.delete("uid")
  runner.render(EvaluationRunPage,{name:run.name});runner.effects[0]?.()
  assert.deepEqual(navigations,["/experiments/runs/run%2Fone?offset=5&service=workload&uid=replacement"])
})

test("single execution control preserves UID, denied responses and uncertain outcome re-read",async()=>{
  for(const status of [403,0]) {
    const runner=hookRunner(),writes:unknown[]=[],reads:string[]=[]
    const run={name:"run/one",uid:"old",phase:"Running",spec:{}}
    const error=new ApiError(status,status===0)
    const send={isPending:false,isSuccess:false,error:null as ApiError|null,mutateAsync:async(input:unknown)=>{writes.push(input);send.error=error;throw error}}
    const modules={
      react:runner.react,"next/navigation":{useSearchParams:()=>new URLSearchParams("uid=old"),useRouter:()=>({replace:()=>{}})},
      "@/lib/client":{ApiError,useChillMutation:()=>send,useChillQuery:(path:string)=>({data:run,refetch:async()=>{reads.push(path)}})},
    }
    const {EvaluationRunPage}=loadUI("../features/evaluation-request.tsx",modules)
    const action=elements(runner.render(EvaluationRunPage,{name:run.name})).find(item=>item.props.run===run)!
    runner.reset()
    const render=()=>elements(runner.render(action.type as (props:Record<string,unknown>)=>unknown,action.props))
    let tree=render()
    ;(tree.find(item=>item.type==="button"&&item.props.children==="중지")!.props.onClick as ()=>void)()
    assert.equal(writes.length,0,"opening confirmation never sends a mutation")
    tree=render()
    ;(tree.find(item=>item.type==="button"&&item.props.children==="확인")!.props.onClick as ()=>void)()
    await setImmediate()
    tree=render()
    assert.deepEqual(writes,[{path:"v1/evaluations/run%2Fone?uid=old",method:"DELETE"}])
    if(status===0) {
      assert.ok(tree.some(item=>item.props.children==="결과 미확인"))
      assert.equal(tree.find(item=>item.type==="button"&&item.props.children==="확인")?.props.disabled,true)
      assert.ok(reads.length>0)
      ;(tree.find(item=>item.type==="button"&&item.props.children==="다시 조회")!.props.onClick as ()=>void)()
      await setImmediate();assert.equal(writes.length,1)
    } else {
      const failure=tree.find(item=>item.props.error===error)!
      assert.match(renderToStaticMarkup(React.createElement(failure.type as React.ComponentType<{error:ApiError}>,{error})),/권한 없음/)
    }
  }
})

test("read-only active progress hides duplicate stop controls without affecting operations consumers",()=>{
  const runner=hookRunner(),run={name:"run",uid:"uid",stageIndex:1,stagesTotal:1,stage:{id:"base",injections:[{id:"kill",action:"delete-serving-pod",offsetSeconds:0}]}}
  const {EvaluationActive}=loadUI("../features/evaluation-active.tsx",{
    react:runner.react,"@/lib/evaluation-active":activeHelpers,"@/lib/status":{STATUS_AXES},
    "@/lib/client":{useChillQuery:(path:string)=>({data:{items:path.endsWith("/active")?[run]:[{name:"run",uid:"uid",phase:"Running"}]}}),useChillMutation:()=>({})},
  })
  const readonly=elements(runner.render(EvaluationActive,{now:0,name:"run",uid:"uid",controls:false}))
  assert.ok(readonly.some(item=>item.type==="table"))
  assert.ok(!readonly.some(item=>item.props.children==="실험 중지"||item.type==="dialog"))
  const operational=elements(runner.render(EvaluationActive,{now:0}))
  assert.ok(operational.some(item=>item.props.children==="실험 중지"))
})

test("execution acceptance links to the actual response UID and never submits after draft changes",async()=>{
  const runner=hookRunner(),writes:unknown[]=[],accepted:unknown[]=[]
  let mutationIndex=0
  const pre={data:undefined as unknown,isPending:false,error:null,mutate:()=>{pre.data={data:{checks:[{id:"valid",passed:true}]}}}}
  const send={isPending:false,error:null,mutateAsync:async(input:unknown)=>{writes.push(input);return {data:{name:"actual/run",uid:"response-uid"}}}}
  const {EvaluationSubmit}=loadUI("../features/evaluation-request.tsx",{
    react:runner.react,"@/lib/client":{ApiError,useChillMutation:()=>mutationIndex++===0?pre:send,useChillQuery:()=>({refetch:async()=>({})})},
  })
  let spec={question:"first"}
  const render=()=>{mutationIndex=0;return elements(runner.render(EvaluationSubmit,{name:"draft",spec,onSubmitted:(value:unknown)=>accepted.push(value)}))}
  let tree=render()
  assert.equal(tree.find(item=>item.props.children==="실험 시작")?.props.disabled,true)
  ;(tree.find(item=>item.props.children==="사전 점검")!.props.onClick as ()=>void)()
  tree=render()
  assert.equal(tree.find(item=>item.props.children==="실험 시작")?.props.disabled,false)
  spec={question:"changed"};tree=render()
  assert.equal(tree.find(item=>item.props.children==="실험 시작")?.props.disabled,true)
  ;(tree.find(item=>item.props.children==="실험 시작")!.props.onClick as ()=>void)()
  await setImmediate();assert.equal(writes.length,0)
  ;(tree.find(item=>item.props.children==="사전 점검")!.props.onClick as ()=>void)()
  tree=render()
  ;(tree.find(item=>item.props.children==="실험 시작")!.props.onClick as ()=>void)()
  await setImmediate();tree=render()
  assert.equal(writes.length,1)
  assert.deepEqual(accepted,[{name:"actual/run",uid:"response-uid"}])
  assert.ok(tree.some(item=>item.type==="a"&&item.props.href==="/experiments/runs/actual%2Frun?uid=response-uid"))
})

test("server dispatch distinguishes execution, published list and encoded execution detail",async()=>{
  const {default:Page}=loadUI("../app/[...route]/page.tsx",{
    "@/lib/dashboard-routes":routes,
    "@/features/evaluation-request":{EvaluationsPage:"execution-list",EvaluationRunPage:"execution-detail"},
    "@/features/experiments":{PublishedExperimentsPage:"published-list",ExperimentsPage:"publication-detail"},
    "@/features/evaluation-author":{EvaluationAuthorPage:"execution-editor"},
    "next/navigation":{redirect:(url:string)=>{throw new Error(`redirect:${url}`)},notFound:()=>{throw new Error("not-found")}},
  })
  for(const [route,search,type] of [
    [["experiments"],{},"execution-list"],
    [["experiments"],{view:"published",offset:"5"},"published-list"],
    [["experiments","results"],{a:"publication"},"publication-detail"],
    [["experiments","runs","run%2Fone"],{uid:"old"},"execution-detail"],
    [["experiments","runs","edit"],{uid:"old"},"execution-detail"],
    [["experiments","runs","runs","edit"],{uid:"old"},"execution-editor"],
    [["experiments","runs","run%2Fone","edit"],{uid:"old"},"execution-editor"],
  ] as [string[],Record<string,string>,string][]) {
    const tree=elements(await Page({params:Promise.resolve({route}),searchParams:Promise.resolve(search)}))
    const screen=tree.find(item=>item.type===type)!
    assert.ok(screen,type)
    if(type==="execution-detail"||type==="execution-editor")assert.equal(screen.props.name,decodeURIComponent(route[2]))
  }
  await assert.rejects(async()=>Page({params:Promise.resolve({route:["experiments"]}),searchParams:Promise.resolve({name:"run",uid:"old"})}),/redirect:\/experiments\/runs\/run\?uid=old/)
})

test("missing or denied execution details never retain cached-run controls",()=>{
  const runner=hookRunner(),run={name:"run",uid:"old",phase:"Running",spec:{}}
  let status=403
  const {EvaluationRunPage}=loadUI("../features/evaluation-request.tsx",{
    react:runner.react,"next/navigation":{useSearchParams:()=>new URLSearchParams("uid=old"),useRouter:()=>({replace:()=>{}})},
    "@/lib/client":{useChillQuery:()=>({data:run,error:new ApiError(status)})},
  })
  for(status of [403,404]) {
    const tree=elements(runner.render(EvaluationRunPage,{name:"run"}))
    assert.ok(!tree.some(item=>"run" in item.props||item.type==="active"))
    if(status===404)assert.ok(tree.some(item=>item.props.children==="실행 없음"))
    else {
      const query=tree.find(item=>item.type==="gate")?.props.query
      assert.ok(query&&typeof query==="object"&&"error" in query&&query.error instanceof ApiError)
      assert.equal(query.error.status,403)
    }
  }
})

test("pending execution editing uses the collision-free canonical route",()=>{
  const runner=hookRunner(),run={name:"runs",uid:"old",phase:"Pending",spec:{}}
  const {EvaluationRunPage}=loadUI("../features/evaluation-request.tsx",{
    react:runner.react,"next/navigation":{useSearchParams:()=>new URLSearchParams("uid=old"),useRouter:()=>({replace:()=>{}})},
    "@/lib/client":{useChillQuery:()=>({data:run}),useChillMutation:()=>({})},
  })
  const action=elements(runner.render(EvaluationRunPage,{name:run.name})).find(item=>item.props.run===run)!
  runner.reset()
  const tree=elements(runner.render(action.type as (props:Record<string,unknown>)=>unknown,action.props))
  assert.ok(tree.some(item=>item.type==="a"&&item.props.href==="/experiments/runs/runs/edit?uid=old"))
  assert.ok(tree.some(item=>item.type==="button"&&item.props.children==="삭제"))
})

test("blackbox preflight displays observed ready targets without changing copied inputs",async()=>{
  const runner=hookRunner(),writes:unknown[]=[]
  let mutationIndex=0
  const spec={question:"external load",target:{components:[{name:"chill",resource:{apiVersion:"apps/v1",kind:"Deployment",namespace:"research",name:"chill"}}]}}
  const original=structuredClone(spec)
  const pre={data:undefined as unknown,isPending:false,error:null,mutate:()=>{pre.data={data:{checks:[{id:"target/chill",passed:true}],liveTarget:[{component:"chill",resource:spec.target.components[0].resource,liveImageIDs:["runtime@sha256:changed"],ready:true}]}}}}
  const send={isPending:false,error:null,mutateAsync:async(input:unknown)=>{writes.push(input);return {data:{name:"run",uid:"new"}}}}
  const {EvaluationSubmit}=loadUI("../features/evaluation-request.tsx",{
    react:runner.react,"@/lib/client":{ApiError,useChillMutation:()=>mutationIndex++===0?pre:send,useChillQuery:()=>({refetch:async()=>({})})},
  })
  const render=()=>{mutationIndex=0;return elements(runner.render(EvaluationSubmit,{name:"run",spec}))}
  let tree=render()
  ;(tree.find(item=>item.props.children==="사전 점검")!.props.onClick as ()=>void)()
  tree=render()
  const text=tree.flatMap(item=>item.props.children).filter(value=>typeof value==="string").join(" ")
  assert.match(text,/runtime@sha256:changed/)
  assert.match(text,/준비됨/)
  assert.equal(tree.find(item=>item.props.children==="실험 시작")?.props.disabled,false)
  assert.equal(tree.some(item=>item.type==="input"),false)
  ;(tree.find(item=>item.props.children==="실험 시작")!.props.onClick as ()=>void)()
  await setImmediate()
  const submitted=writes[0]
  assert.ok(submitted&&typeof submitted==="object"&&"body" in submitted&&typeof submitted.body==="string")
  assert.deepEqual(spec,original)
  assert.deepEqual(JSON.parse(submitted.body),{name:"run",spec})
})
