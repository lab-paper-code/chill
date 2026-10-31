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
      "@/lib/experiments":{},"@/features/evaluation-author":{},"@/components/ui/button":{Button},
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
