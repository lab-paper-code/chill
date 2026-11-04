import assert from "node:assert/strict"
import { test } from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ScriptTarget, ModuleKind, JsxEmit } from "typescript"
import { initialLiveState, type LiveState } from "./live.ts"
import * as display from "./live-display.ts"
import * as data from "../features/resources-data.ts"
import * as icons from "lucide-react"

const at="2026-11-05T00:00:00Z",require=createRequire(import.meta.url)
function render(state:LiveState,selected=false,reads:Record<string,unknown>={}) {
  const wrap=({children}:{children?:ReactNode})=>createElement("div",{},children)
  const modules:Record<string,unknown>={
    "lucide-react":icons,
    "next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams(selected?{node:"node-private-uid"}:{}),useRouter:()=>({replace:()=>{}})},
    "@/lib/client":{useChillQuery:(path:string)=>({data:reads[path],isPending:false,error:null})},"@/lib/use-live":{useLive:()=>({state})},
    "@/hooks/use-measurement-data":{useCoverageInventory:()=>({isPending:false,error:null})},"@/lib/scheduler":{parseSchedulerJSON:JSON.parse,stringifySchedulerJSON:JSON.stringify},
    "@/lib/live-display":display,"@/lib/status":{STATUS_AXES:{}},"@/components/status-cell":{StatusCell:({label}:{label:string})=>createElement("span",{},label)},
    "@/components/read-region":{SourceJSON:()=>null,SourceExport:()=>null,StatusBadge:wrap},"@/lib/source-export":{safeSource:(value:unknown)=>value},
    "@/components/ui/sheet":{Sheet:({open,children}:{open:boolean;children:ReactNode})=>open?createElement("div",{},children):null,SheetContent:wrap,SheetHeader:wrap,SheetTitle:wrap},
    "@/components/ui/tooltip":{Tooltip:wrap,TooltipTrigger:wrap,TooltipContent:wrap},
    "./resources-data":data,"./power-series":{usePowerSeries:()=>({points:[],query:{isPending:false,error:null}}),PowerTrend:()=>null},"./resources.css":{},
  }
  const exports:Record<string,()=>ReactNode>={}
  const compiled=transpileModule(readFileSync(new URL("../features/resources.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const serving:Record<string,unknown>={}
  const servingCode=transpileModule(readFileSync(new URL("../components/serving-instance.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",servingCode)((name:string)=>name==="@/lib/live-display"?display:name.endsWith(".css")?{}:require(name),serving)
  modules["@/components/serving-instance"]=serving
  const observation:Record<string,unknown>={}
  const observationCode=transpileModule(readFileSync(new URL("../components/observation-age.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",observationCode)((name:string)=>name.endsWith(".css")?{}:require(name),observation)
  modules["@/components/observation-age"]=observation
  new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
  return renderToStaticMarkup(createElement(exports.DevicesPage))
}
function state():LiveState {
  return {...initialLiveState(),status:"fixture",lastObservedAt:at,lastPower:{watts:20,bucketAt:at},snapshot:{nodes:[{uid:"node-private-uid",name:"장치 A",ready:true,measurement:false,powerMode:"fixed",observedAt:at}],workloads:[{uid:"service-private-uid",name:"영상 분석"} as NonNullable<LiveState["snapshot"]>["workloads"][number]],instances:[{id:"pod-private-id",node:{uid:"node-private-uid",name:"장치 A"},ownerWorkloadUID:"service-private-uid",profileID:"profile-private-hash",ready:true,stopped:false,routable:{"service-private-uid":true},running:{"service-private-uid":2},waiting:{"service-private-uid":1},observedAt:at}],routing:{"service-private-uid":{"pod-private-id":1,other:3}},decision:null,actions:[],reservations:[]},metrics:[]}
}
test("device fleet shows service instance state and actual normalized routing without exposing identity text",()=>{
  const html=render(state())
  assert.ok(/25(?:\.0)?%/.test(html),"actual routing share is visible on the device's service instance")
  assert.ok(html.includes("영상 분석"))
  const text=html.replace(/<[^>]*>/g,"")
  assert.ok(!text.includes("node-private-uid"))
  assert.ok(!text.includes("pod-private-id"))
  assert.ok(!text.includes("profile-private-hash"))
})
test("device detail presents serving observations before power history and keeps raw identity in a disclosure",()=>{
  const html=render(state(),true)
  assert.ok(/25(?:\.0)?%/.test(html))
  assert.ok(html.includes('aria-label="처리 요청"'))
  assert.ok(html.includes('aria-label="대기 요청"'))
  const detail=html.slice(html.indexOf('class="devices-detail"'))
  const raw=detail.indexOf('<details class="devices-original"')
  assert.ok(raw>0)
  assert.ok(!detail.slice(0,raw).replace(/<[^>]*>/g,"").includes("pod-private-id"))
})
test("device power observations keep identical element slots across freshness and missing-power changes",()=>{
  const first=state(),second=state()
  second.lastPower={watts:20,bucketAt:"2020-01-01T00:00:00Z"}
  second.metrics=[{type:"metrics",at,seq:1,bucketAt:at,nodes:{"node-private-uid":{watts:20,observedAt:at}},workloads:{},pool:{wholeWatts:20,activeWatts:20}}]
  const structure=(html:string)=>(html.match(/<\/?[a-z][^>]*>/g)??[]).map(tag=>tag.match(/^<\/?[a-z]+/)?.[0])
  assert.deepEqual(structure(render(first)),structure(render(second)))
})

test("REST-only placements retain services without fabricating live routing or request observations",()=>{
  const rest=state()
  rest.snapshot!.instances=[]
  const ref={uid:"node-private-uid",name:"장치 A"}
  const instance={id:"pod-private-id",node:ref,profile:"hash",ready:false,stopped:false,workloads:[{uid:"service-private-uid",name:"영상 분석"}],running:{},waiting:{},assignedUnknown:{},observedAt:at}
  const html=render(rest,false,{"v1/pools":{observedAt:at,workloads:[{workload:instance.workloads[0],nodes:[ref],instances:[instance]}],measurement:[],available:[],other:[]}})
  assert.ok(html.includes("영상 분석"))
  assert.ok(!/25(?:\.0)?%/.test(html))
  const counts=html.match(/aria-label="(?:처리|대기) 요청"[^>]*>(.*?)<\/span>/g)??[]
  assert.equal(counts.length,2)
  assert.ok(counts.every(value=>!/>[^<]*\b0\b/.test(value)),"absent request counts never become observed zero")
})

test("device header and aggregate power retain shared connection and age slots",()=>{
  const live=state()
  live.metrics=[{type:"metrics",at,seq:1,bucketAt:at,nodes:{},workloads:{},pool:{wholeWatts:20,activeWatts:20}}]
  const html=render(live)
  const heading=html.slice(html.indexOf('class="devices-heading-status"'),html.indexOf('<section'))
  assert.ok(heading.includes('class="live-observation"'))
  assert.ok(/class="observation-age-value">[\d,]+</.test(heading),"metric age remains numeric rather than a freshness verdict")
  const power=html.slice(html.indexOf('devices-power-facts'),html.indexOf('devices-gap-table'))
  assert.ok(power.includes('class="observation-age"'))
  assert.ok(power.includes(`title="${at.replace("Z",".000Z")}"`))
  live.lastPower=null
  assert.ok(render(live).includes('class="observation-age-unit"></span>'))
})

test("shared physical instances enclose services under one accessible group label",()=>{
  const shared=state()
  shared.snapshot!.workloads.push({...shared.snapshot!.workloads[0],uid:"second-service",name:"다른 서비스"})
  shared.snapshot!.instances[0].routable["second-service"]=true
  shared.snapshot!.instances[0].running["second-service"]=1
  const html=render(shared,true)
  const groups=html.match(/class="devices-instance" role="group" aria-label="[^"]+"/g)??[]
  assert.equal(groups.length,2,"one physical group in the fleet and one in the Sheet")
  assert.equal((html.match(/class="devices-instance-label"/g)??[]).length,2)
  assert.equal((html.match(/class="devices-service-instance"/g)??[]).length,4)
  const detail=html.slice(html.indexOf('class="devices-detail"'),html.indexOf('<details class="devices-original"'))
  assert.ok(!detail.includes('class="serving-instance-mode"'))
})
