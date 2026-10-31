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
      "next/link":{default:({children,href}:{children:React.ReactNode;href:string})=>React.createElement("a",{href},children)},"next/navigation":{},
    }
    const source=readFileSync(new URL("../features/evaluation-request.tsx",import.meta.url),"utf8")
    const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
    const exports:{EvaluationSubmit?:typeof EvaluationSubmit}={}
    new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports)
    function render() {
      cursor=0;mutationIndex=0;buttons.clear()
      return renderToStaticMarkup(exports.EvaluationSubmit!({name:"run",spec,existing:{name:"run",uid:"old"},onSpecChange:()=>{},onSubmitted:value=>{submitted=value.uid}}))
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
  const active={items:[{name:"run",uid:"uid",stageIndex:1,stagesTotal:1,stage:{id:"base",mode:"autonomous",plannedSeconds:60,injections:[{id:"kill",action:"delete-serving-pod",offsetSeconds:0},{id:"cordon",action:"cordon-delete",offsetSeconds:0},{id:"restore",action:"uncordon",offsetSeconds:10}]}}]}
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
