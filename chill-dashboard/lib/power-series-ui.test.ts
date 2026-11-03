import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import { createElement, type ReactElement, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import * as powerSeries from "./power-series.ts"
import { displayTime } from "./live-display.ts"
import type { LiveMetrics } from "./live.ts"
import * as displayHelpers from "./live-display.ts"
import { initialLiveState } from "./live.ts"
import * as resourcesData from "../features/resources-data.ts"
import type { OperatingPowerSeries } from "./read-types.ts"

const require=createRequire(import.meta.url)
function load(file:string,modules:Record<string,unknown>){
  const exports:Record<string,(props:Record<string,unknown>)=>ReactElement>={}
  const code=transpileModule(readFileSync(new URL(file,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",code)((name:string)=>modules[name]??(name.startsWith("@/")||name.startsWith("./")?{}:require(name)),exports)
  return exports
}
test("rendered power chart retains interval timestamps, breaks nulls and labels measured zero",()=>{
  const chart=load("../features/power-series.tsx",{"@/lib/power-series":powerSeries,"@/lib/live-display":{displayTime}})
  const points=[{from:1000,to:6000,wholeWatts:0,nodes:{}},{from:6000,to:11000,wholeWatts:null,nodes:{}},{from:11000,to:12000,wholeWatts:20,nodes:{}}]
  const html=renderToStaticMarkup(createElement(chart.PowerTrend,{points}))
  assert.match(html,/전력 추세/)
  assert.match(html,/0 W/)
  assert.equal((html.match(/<polyline/g)??[]).length,2)
  assert.match(html,/data-from="1000"/)
  assert.match(html,/data-to="6000"/)
  assert.match(html,/관측 공백/)
})

test("power hook seeds bounded recent history on reentry then retains appended live intervals",()=>{
  const cells:unknown[]=[],effects:(()=>void)[]=[],requests:string[]=[]
  let cursor=0
  const now=Date.now(),stamp=(offset:number)=>new Date(now+offset*1000).toISOString()
  const seed={from:stamp(-900),to:stamp(0),buckets:[{from:stamp(-900),to:stamp(-895),wholePoolMeanWatts:0,activePoolMeanWatts:0,nodes:[]}]}
  const modules={
    react:{useState:(initial:unknown)=>{const index=cursor++;if(!(index in cells))cells[index]=typeof initial==="function"?initial():initial;return [cells[index],(value:unknown)=>{cells[index]=typeof value==="function"?value(cells[index]):value} ]},useEffect:(effect:()=>void)=>effects.push(effect)},
    "@/lib/client":{useChillQuery:(path:string)=>{requests.push(path);return {data:seed}}},
    "@/lib/power-series":powerSeries,
  }
  const hook=load("../features/power-series.tsx",modules).usePowerSeries as unknown as (state:{status:string;metrics:LiveMetrics[]})=>{points:powerSeries.PowerPoint[]}
  let state={status:"live",metrics:[] as LiveMetrics[]}
  let value=hook(state)
  const request=new URL(requests[0],"https://local")
  assert.equal(request.searchParams.get("bucket"),"5s")
  assert.equal(request.searchParams.get("format"),"columns")
  assert.equal(Date.parse(request.searchParams.get("to")!)-Date.parse(request.searchParams.get("from")!),900000)
  assert.equal(value.points[0].wholeWatts,0)
  const metric:LiveMetrics={type:"metrics",seq:1,at:stamp(1),bucketAt:stamp(0),workloads:{},nodes:{},pool:{wholeWatts:0,activeWatts:0}}
  state={...state,metrics:[metric]};cursor=0;hook(state);effects.splice(0).forEach(effect=>effect())
  cursor=0;value=hook({...state,metrics:[]})
  assert.equal(value.points.at(-1)!.from,Date.parse(metric.bucketAt))
  assert.equal(value.points.at(-1)!.wholeWatts,0)
})

test("timeline requests selected five-second chunks, blocks partial results and refreshes every chunk",()=>{
  const from="2026-11-01T00:00:00Z",to="2026-11-01T01:00:00Z"
  const requests:{paths:string[];decoder?:(body:string)=>unknown}[]=[],refreshed:number[]=[]
  let results:{data?:OperatingPowerSeries;error:Error|null;isPending:boolean;refetch:()=>void}[]=[]
  let gate:{data?:OperatingPowerSeries;error?:Error|null;isPending?:boolean;refetch?:()=>void}|undefined
  const modules={
    react:{...require("react"),useState:()=>["5s",()=>{}]},
    "@/lib/client":{
      useChillQuery:()=>({data:undefined}),
      useChillQueries:(paths:string[],options:{decoder?:(body:string)=>unknown})=>{requests.push({paths,...options});return results},
    },
    "@/lib/power-series":powerSeries,
    "@/lib/live-display":{displayTime},
    "@/components/read-region":{
      QueryGate:({query,children}:{query:typeof gate;children:ReactNode})=>{gate=query;return query?.error||query?.isPending?null:children},
      SourceJSON:()=>null,SourceExport:()=>null,
    },
  }
  const screen=load("../features/power-series.tsx",modules)
  renderToStaticMarkup(createElement(screen.PowerTimeline,{from,to}))
  assert.equal(requests[0].paths.length,4)
  assert.equal(typeof requests[0].decoder,"function")
  results=requests[0].paths.map((path,index)=>{
    const params=new URL(path,"https://local").searchParams
    assert.equal(params.get("format"),"columns")
    assert.equal(params.get("bucket"),"5s")
    return {data:{from:params.get("from")!,to:params.get("to")!,bucketNanos:5000000000,buckets:[]},error:null,isPending:false,refetch:()=>{refreshed.push(index)}}
  })
  let html=renderToStaticMarkup(createElement(screen.PowerTimeline,{from,to}))
  assert.match(html,/<option value="5s" selected="">5초<\/option>/)
  assert.equal(gate!.data!.from,from)
  assert.equal(gate!.data!.to,to)
  const warming=new Error("ArchiveWarming")
  results[1]={...results[1],data:undefined,error:warming}
  html=renderToStaticMarkup(createElement(screen.PowerTimeline,{from,to}))
  assert.equal(gate!.error,warming)
  assert.equal(gate!.data,undefined)
  assert.doesNotMatch(html,/aria-label="전력 관측 범위"/)
  gate!.refetch!()
  assert.deepEqual(refreshed,[0,1,2,3])
  results[1]={...results[1],error:null,isPending:true}
  renderToStaticMarkup(createElement(screen.PowerTimeline,{from,to}))
  assert.equal(gate!.isPending,true)
  assert.equal(gate!.data,undefined)
})

test("records power interval renders bucket timeline independently from period summary",()=>{
  const timeline=()=>null
  const screen=load("../features/scheduler.tsx",{"./power-series":{PowerTimeline:timeline},"@/lib/client":{useChillQuery:()=>({data:undefined})}})
  const tree=screen.PowerInterval({from:"2026-11-01T00:00:00Z",to:"2026-11-01T00:15:00Z"})
  function contains(value:unknown):boolean{
    if(Array.isArray(value))return value.some(contains)
    if(!value||typeof value!=="object"||!("props" in value))return false
    const element=value as {type:unknown;props:{children?:unknown}}
    return element.type===timeline||contains(element.props.children)
  }
  assert.equal(contains(tree),true)
})

test("device power presents one observation range and no explanatory paragraph",()=>{
  const query={error:null,isPending:false,refetch:()=>{}},state={...initialLiveState(),status:"fixture" as const}
  const chart=load("../features/power-series.tsx",{"@/lib/power-series":powerSeries,"@/lib/live-display":displayHelpers})
  const screen=load("../features/resources.tsx",{
    "next/link":{default:function Link({children}:{children?:ReactNode}){return createElement("a",{},children)}},
    "next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({replace:()=>{}})},
    "@/lib/client":{useChillQuery:()=>query},"@/lib/use-live":{useLive:()=>({state})},
    "./measurement-coverage":{useCoverageInventory:()=>query},"@/lib/live-display":displayHelpers,
    "@/lib/status":{STATUS_AXES:{}},"@/components/status-cell":{StatusCell:()=>null},
    "@/components/ui/sheet":{Sheet:()=>null},"./resources-data":resourcesData,
    "./power-series":{...chart,usePowerSeries:()=>({query,points:[{from:1000,to:6000,wholeWatts:0,nodes:{}}]})},
  })
  const html=renderToStaticMarkup(createElement(screen.DevicesPage,{}))
  const facts=html.match(/<table class="devices-table devices-power-facts"[\s\S]*?<\/table>/)![0]
  assert.equal((facts.match(/<th>/g)??[]).length,4)
  assert.equal((html.match(/aria-label="전력 관측 범위"/g)??[]).length,1)
  assert.doesNotMatch(html,/<p>최근 15분/)
})
