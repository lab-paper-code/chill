import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import * as React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule,ModuleKind,JsxEmit,ScriptTarget } from "typescript"
import * as display from "./live-display.ts"
import { initialLiveState } from "./live.ts"

type Element={type:unknown;key?:string|null;props:Record<string,unknown>}
const require=createRequire(import.meta.url)
function harness(){
  const at="2026-11-01T01:16:44.827689578Z",cursor="2026-11-01T03:00:00.000000001Z",cells:unknown[]=[],calls:string[]=[]
  let index=0,page:Record<string,unknown>={events:[],nextCursor:cursor,coverage:{from:"2026-11-01T02:00:00Z",to:cursor,sources:[],gaps:[{from:"2026-11-01T02:30:00Z",to:"2026-11-01T02:31:00Z",stream:"events",reason:"source unavailable"}]}},error:unknown
  const event={type:"event",kind:"cluster",at,cluster:{kind:"issue",at,event:"raised",source:"serving-instance",reason:"context deadline exceeded"}}
  const modules:Record<string,unknown>={react:{...React,useState:(initial:unknown)=>{const n=index++;if(!(n in cells))cells[n]=typeof initial==="function"?initial():initial;return [cells[n],(next:unknown)=>{cells[n]=typeof next==="function"?next(cells[n]):next}]}},"next/link":{default:"a"},"next/navigation":{},"@/lib/client":{useChillQuery:(path:string)=>{calls.push(path);return {data:error?undefined:page,error}}},"@/lib/use-live":{useLive:()=>({state:initialLiveState()})},"@/lib/live-display":display,"@/lib/live":{},"@/lib/scheduler-history":{evidenceError:()=>"조회 시간 초과"},"@/lib/scheduler":{stringifySchedulerJSON:JSON.stringify},"@/lib/source-export":{safeSource:(value:unknown)=>value},"./scheduler":{},"@/components/status-cell":{},"@/lib/status":{},"@/components/ui/sheet":{},"./evaluation-active":{},"@/lib/evaluation-active":{},"./power-series":{},"./live-board.css":{},"./scheduler-workload-states":{}}
  modules["@/components/observation-age"]={}
  modules["@/components/serving-instance"]={}
  modules["@/components/ui/tooltip"]={}
  const exports:Record<string,(props:Record<string,unknown>)=>unknown>={}
  const compiled=transpileModule(readFileSync(new URL("../features/live-board.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
  const buttons:Element[]=[]
  function resolve(node:unknown):React.ReactNode{
    if(Array.isArray(node))return node.map((child,index)=>React.createElement(React.Fragment,{key:index},resolve(child)))
    if(!node||typeof node!=="object")return node as React.ReactNode
    const element=node as Element
    if(typeof element.type==="function")return resolve(element.type(element.props))
    if(element.type==="button")buttons.push(element)
    const {children,...props}=element.props
    return React.createElement(element.type as React.ElementType,{...props,key:element.key},resolve(children))
  }
  return {at,cursor,calls,event,setPage:(value:Record<string,unknown>)=>{page=value},setError:(value:unknown)=>{error=value},render:()=>{index=0;buttons.length=0;return renderToStaticMarkup(resolve(exports.IncidentRecord({identity:JSON.stringify(["cluster",at,"serving-instance","raised"])})))},more:()=>{const button=buttons.find(row=>row.props.children==="더 찾기");assert.ok(button,"user can explicitly search the next append window");(button.props.onClick as ()=>void)()}}
}

test("an absent exact incident continues only on user request and shows partial coverage",()=>{
  const h=harness(),first=h.render()
  assert.match(first,/찾는 중/);assert.match(first,/읽은 범위/);assert.match(first,/12:00:00 KST/);assert.match(first,/공백/)
  assert.equal(new URL(h.calls.at(-1)!,"https://local").searchParams.has("cursor"),false)
  h.more()
  h.setPage({events:[h.event],nextCursor:"2026-11-01T04:00:00Z",coverage:{from:h.cursor,to:"2026-11-01T04:00:00Z",sources:[],gaps:[]}})
  const found=h.render()
  assert.equal(new URL(h.calls.at(-1)!,"https://local").searchParams.get("cursor"),h.cursor)
  assert.match(found,/serving-instance/);assert.match(found,/이후 기록 미확인/)
})

test("legacy exact incident responses and timeouts retain their empty/error behavior",()=>{
  const h=harness();h.setPage({events:[]})
  assert.match(h.render(),/보관 범위 밖 사건/)
  h.setError(new Error("timeout"));assert.match(h.render(),/조회 시간 초과/)
})
