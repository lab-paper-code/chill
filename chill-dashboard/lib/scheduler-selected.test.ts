import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as scheduler from "./scheduler.ts"
import * as identity from "./workload-identity.ts"
import * as dates from "./date-time.ts"
import type * as Screen from "../features/scheduler.tsx"

test("selected service evidence retains configured percentile, zero prediction, missing prediction and exact load window",()=>{
  const require=createRequire(import.meta.url)
  let params=new URLSearchParams("open=service:selected")
  const from="2026-10-31T01:02:03.123456789Z",to="2026-10-31T01:03:03.987654321Z",calls:string[]=[]
  const definition={uid:"selected",name:"선택 서비스",slo:{percentile:99,targetLatencyNanos:100000000}}
  const view={state:{snapshot:{at:from,workloads:[definition,{...definition,uid:"other",name:"다른 서비스"}]},decision:{at:from,search:{windowStart:from,windowEnd:to},keep:{id:"keep",keep:true,status:"Keep",expectedJoules:0,slo:[{uid:"selected",expectedNanos:0,samples:2,normalizedExceedance:0}]},target:{id:"target",keep:false,status:"Pending",expectedJoules:null,slo:[{uid:"selected",expectedNanos:null,samples:0}]}}}}
  const load={from,to,population:"사건 시각",workloads:[{uid:"selected",received:0,accepted:0,assigned:0,completed:0,receivedInputsPerSecond:0,acceptedInputsPerSecond:0,assignedInputsPerSecond:0,completionEventsPerSecond:0,instanceAssignments:{}}]}
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const modules:Record<string,unknown>={
    "react":require("react"),"next/link":{default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},
    "next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:()=>{}})},
    "@/lib/client":{useSession:()=>({config:{maxWindowHours:24}}),useWorkloadDefinitions:()=>[],useChillQuery:(path:string)=>{calls.push(path);return {data:path==="v1/scheduler"?view:path.startsWith("v1/observations/load?")?load:path==="v1/workloads"?[{desired:{metadata:{uid:definition.uid,name:definition.name}}}]:[],error:null}}},
    "@/lib/scheduler":scheduler,"@/lib/workload-identity":identity,"@/lib/date-time":dates,
    "@/features/scheduler-profiles":{},"@/features/scheduler-capacity":{},"@/lib/capacity":{},"@/lib/labels":{statusLabel:(value:string)=>value},"@/lib/source-export":{},
    "@/components/read-region":{QueryGate:container,SourceJSON:()=>null,SourceExport:()=>null,StatusBadge:container},
    "@/components/ui/button":{Button:container},"@/components/screen":{Facts:()=>null},
    "@/components/data-table":{DataTable:({data,columns}:{data:unknown[];columns:{key:string;value:(row:unknown)=>ReactNode}[]})=>createElement("table",null,createElement("tbody",null,data.map((row,index)=>createElement("tr",{key:index},columns.map(column=>createElement("td",{key:column.key},column.value(row)))))))},
  }
  const exports:Record<string,unknown>={}
  const source=readFileSync(new URL("../features/scheduler.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports)
  const {SchedulerEvidence}=exports as typeof Screen
  const html=renderToStaticMarkup(createElement(SchedulerEvidence,{kind:"estimates"}))
  assert.match(html,/선택 서비스/)
  assert.doesNotMatch(html,/다른 서비스/)
  assert.match(html,/p99/)
  assert.match(html,/<td>0<\/td>/)
  assert.match(html,/판정 불가/)
  params=new URLSearchParams({open:"service:selected",evidence:"load",from,to})
  const loadHTML=renderToStaticMarkup(createElement(SchedulerEvidence,{kind:"load"}))
  assert.match(loadHTML,/선택 서비스/)
  const address=new URL(calls.find(path=>path.startsWith("v1/observations/load?"))!,"https://dashboard.invalid/")
  assert.equal(address.searchParams.get("from"),from)
  assert.equal(address.searchParams.get("to"),to)
})
