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
import * as liveDisplay from "./live-display.ts"
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
    "@/lib/client":{useSession:()=>({config:{maxWindowHours:24}}),useWorkloadDefinitions:()=>[],useChillQuery:(path:string)=>{calls.push(path);return {data:path==="v1/scheduler"?view:path.startsWith("v1/observations/load?")?load:path==="v1/workloads?view=summary"?[{desired:{metadata:{uid:definition.uid,name:definition.name}}}]:[],error:null}}},
    "@/lib/scheduler":scheduler,"@/lib/workload-identity":identity,"@/lib/date-time":dates,
    "@/lib/live-display":liveDisplay,
    "@/components/ui/chart":{},"recharts":{},
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

test("snapshot deep links freeze the first response and require explicit latest adoption",()=>{
  const require=createRequire(import.meta.url),params=new URLSearchParams("view=snapshot")
  let data:unknown,cursor=0,changed=false
  const cells:unknown[]=[]
  const modules:Record<string,unknown>={
    react:{useState:(initial:unknown)=>{const index=cursor++;if(!(index in cells))cells[index]=typeof initial==="function"?initial():initial;return [cells[index],(value:unknown)=>{cells[index]=value;changed=true}]}},
    "next/navigation":{useSearchParams:()=>params},
    "@/lib/client":{useChillQuery:()=>({data})},"@/lib/scheduler":scheduler,
    "@/lib/labels":{},"@/lib/capacity":{},"@/lib/date-time":{},"@/lib/workload-identity":{},
    "@/lib/live-display":liveDisplay,
    "@/components/ui/chart":{},"recharts":{},
    "@/features/scheduler-profiles":{},"@/features/scheduler-capacity":{},
    "@/components/read-region":{},"@/components/screen":{},"@/components/ui/button":{},
    "@/components/data-table":{},"@/lib/source-export":{},"next/link":{},
  }
  const exports:Record<string,unknown>={}
  const compiled=transpileModule(readFileSync(new URL("../features/scheduler.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports)
  type Element={type:((props:Record<string,unknown>)=>Element)|string;props:Record<string,unknown>}
  const entry=(exports.SchedulerEvidence as (props:unknown)=>Element)({kind:"candidates"})
  function render():Element {
    let tree:Element
    do {changed=false;cursor=0;tree=(entry.type as (props:Record<string,unknown>)=>Element)(entry.props)}while(changed)
    return tree!
  }
  function nodes(value:unknown):Element[] {
    if(Array.isArray(value))return value.flatMap(nodes)
    if(!value||typeof value!=="object"||!("props" in value))return []
    const element=value as Element;return [element,...nodes(element.props.children)]
  }
  const view=(at:string)=>({state:{snapshot:{at,workloads:[]},decision:{at,search:{windowStart:at,windowEnd:at},keep:{id:"keep"},target:null}}})
  render()
  const first=view("2026-11-01T00:00:00Z");data=first
  let tree=render()
  assert.equal(nodes(tree).find(node=>typeof node.type==="function"&&node.type.name==="SnapshotDetails")?.props.snapshot,first.state.snapshot)
  const latest=view("2026-11-01T00:01:00Z");data=latest;tree=render()
  assert.equal(nodes(tree).find(node=>typeof node.type==="function"&&node.type.name==="SnapshotDetails")?.props.snapshot,first.state.snapshot)
  assert.ok(nodes(tree).some(node=>node.props.children==="새 판단 수신"))
  const adopt=nodes(tree).find(node=>node.props.children==="최신 응답")!
  ;(adopt.props.onClick as ()=>void)()
  tree=render()
  assert.equal(nodes(tree).find(node=>typeof node.type==="function"&&node.type.name==="SnapshotDetails")?.props.snapshot,latest.state.snapshot)
  assert.equal(nodes(tree).some(node=>node.props.children==="새 판단 수신"),false)
})

test("power overview preserves server interval aggregates and zero versus unavailable node watts",()=>{
  const require=createRequire(import.meta.url),calls:string[]=[]
  const power={from:"2026-11-03T00:00:00Z",to:"2026-11-03T00:05:00Z",complete:false,wholePoolJoules:68560,wholePoolMeanWatts:228.55,activePoolJoules:0,activePoolMeanWatts:0,reasons:[],nodes:[
    {node:{uid:"a",name:"고정"},joules:0,meanWatts:0,samples:300,reasons:[],sourceTimestampAbsent:false},
    {node:{uid:"b",name:"미관측"},joules:null,meanWatts:null,samples:0,reasons:[],sourceTimestampAbsent:false},
  ]}
  const wrap=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const modules:Record<string,unknown>={
    "next/link":{default:wrap},"next/navigation":{},"@/lib/client":{useChillQuery:(path:string)=>{calls.push(path);return {data:power}}},
    "@/lib/scheduler":scheduler,"@/lib/live-display":liveDisplay,"@/lib/workload-identity":identity,"@/lib/date-time":dates,
    "@/features/scheduler-profiles":{},"@/features/scheduler-capacity":{},"@/lib/capacity":{},"@/lib/labels":{},"@/lib/source-export":{},
    "@/components/read-region":{QueryGate:wrap,SourceJSON:()=>null,SourceExport:()=>null,StatusBadge:wrap},
    "@/components/screen":{Facts:({items}:{items:[string,ReactNode][]})=>createElement("dl",null,items.map(([label,value])=>createElement("div",{key:label},label,":",value)))},
    "@/components/data-table":{DataTable:({data,columns}:{data:unknown[];columns:{key:string;value:(row:unknown)=>ReactNode}[]})=>createElement("table",null,createElement("tbody",null,data.map((row,index)=>createElement("tr",{key:index},columns.map(column=>createElement("td",{key:column.key},column.value(row)))))))},
    "@/components/ui/button":{Button:wrap},"@/components/ui/chart":{ChartContainer:wrap,ChartTooltip:()=>null,ChartTooltipContent:()=>null},
    recharts:{BarChart:wrap,Bar:()=>null,CartesianGrid:()=>null,XAxis:()=>null,YAxis:()=>null},
  }
  const exports:Record<string,unknown>={}
  const compiled=transpileModule(readFileSync(new URL("../features/scheduler.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports)
  const {PowerInterval}=exports as typeof Screen
  const html=renderToStaticMarkup(createElement(PowerInterval,{from:power.from,to:power.to}))
  assert.match(html,/68\.56 kJ/)
  assert.match(html,/228\.55 W/)
  assert.match(html,/0 W/)
  assert.match(html,/미제공/)
  assert.match(html,/관측 공백 있음/)
  assert.match(html,/노드별 평균 전력/)
  const request=new URL(calls[0],"https://dashboard.invalid")
  assert.equal(request.searchParams.get("from"),power.from)
  assert.equal(request.searchParams.get("to"),power.to)
})
