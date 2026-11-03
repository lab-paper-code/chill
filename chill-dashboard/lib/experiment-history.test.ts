import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as helpers from "./experiments.ts"
import * as measurements from "./measurements.ts"
import { parseSchedulerJSON } from "./scheduler.ts"
import type { Experiment } from "./experiments.ts"
import type * as ClientModule from "./client.tsx"
import type * as ConclusionModule from "../features/experiments-conclusion.tsx"
import type * as ExperimentsModule from "../features/experiments.tsx"

const require=createRequire(import.meta.url)
function load<T>(path:string,modules:Record<string,unknown>):T {
  const source=readFileSync(new URL(path,import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:Record<string,unknown>={}
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:name.startsWith("@/")||name==="next/navigation"?{}:name.endsWith(".css")?{}:require(name),exports)
  return exports as T
}
const {ApiError}=load<typeof ClientModule>("./client.tsx",{"lucide-react":{}})
const fixture=JSON.parse(readFileSync(new URL("./fixtures/experiment-213.json",import.meta.url),"utf8")).publication as Experiment
const run:Experiment={...fixture,id:"candidate",comparisons:[{left:{stageID:fixture.stages[0].id},right:{historical:{publicationID:"baseline",stageID:"baseline-stage"}},reason:"선언된 과거 비교"}]}

function harness(publication:Experiment=run) {
  let params=new URLSearchParams("a=candidate"),queries:Array<{error:Error|null;data?:Experiment;isPending:boolean;refetch?:()=>void}>=[]
  const paths:string[]=[],historicalPaths:string[][]=[],buttons:Array<{text:string;click:()=>void}>=[],charts:Array<{dataKey:string;onClick?:(entry:{payload:{key:string}})=>void}>=[],events:Array<{from:string;to:string;returnTo?:string;label?:string}>=[]
  const selects:Array<{value:string;onChange:(event:{target:{value:string}})=>void}>=[],sheets:Array<{open:boolean;onOpenChange:(open:boolean)=>void}>=[]
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const Button=({children,onClick,asChild,...props}:{children?:ReactNode;onClick?:()=>void;asChild?:boolean})=>{if(onClick)buttons.push({text:renderToStaticMarkup(createElement("span",null,children)),click:onClick});return asChild?children:createElement("button",props,children)}
  const runtime=require("react/jsx-runtime")
  const track=(method:"jsx"|"jsxs")=>(type:unknown,props:{children?:ReactNode;onClick?:()=>void;value?:string;onChange?:(event:{target:{value:string}})=>void},key?:string)=>{
    if(type==="button"&&props.onClick)buttons.push({text:renderToStaticMarkup(createElement("span",null,props.children)),click:props.onClick})
    if(type==="select"&&props.onChange)selects.push({value:props.value??"",onChange:props.onChange})
    return runtime[method](type,props,key)
  }
  const modules:Record<string,unknown>={
    "react/jsx-runtime":{...runtime,jsx:track("jsx"),jsxs:track("jsxs")},
    "@/lib/client":{ApiError,useChillQueries:(paths:string[])=>{historicalPaths.push(paths);return queries.slice(0,paths.length)},useSession:()=>({config:{namespace:"test",maxPageSize:25}}),useChillQuery:(path:string)=>{paths.push(path);return {data:path.startsWith("v1/experiments?")?{items:[publication],page:{offset:5,limit:5,total:11,nextOffset:10}}:publication,error:null,isFetching:false,refetch:()=>{}}}},
    "@/lib/experiments":helpers,"@/lib/measurements":measurements,"@/lib/scheduler":{parseSchedulerJSON},
    "next/link":{default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},"next/navigation":{useSearchParams:()=>params},
    "recharts":{...Object.fromEntries(["CartesianGrid","ComposedChart","ResponsiveContainer","Tooltip","XAxis","YAxis"].map(name=>[name,container])),Bar:(props:typeof charts[number])=>{charts.push(props);return null},Line:(props:typeof charts[number])=>{charts.push(props);return null}},
    "@/components/screen":{Screen:({children,action,back}:{children?:ReactNode;action?:ReactNode;back?:[string,string]})=>createElement("main",null,back&&createElement("a",{href:back[0]},back[1]),action,children),Empty:container},"@/components/read-region":{QueryGate:container,SourceJSON:()=>null,SourceExport:()=>null},
    "@/components/ui/button":{Button},"@/components/ui/sheet":{Sheet:({children,...props}:{children?:ReactNode;open:boolean;onOpenChange:(open:boolean)=>void})=>{sheets.push(props);return props.open?container({children}):null},SheetContent:container,SheetHeader:container,SheetTitle:container},
    "@/components/ui/dialog":Object.fromEntries(["Dialog","DialogContent","DialogHeader","DialogTitle"].map(name=>[name,container])),
    "@/components/ui/tooltip":{Tooltip:container,TooltipTrigger:container,TooltipContent:()=>null},
    "@/features/evaluation-request":{EvaluationCleanup:()=>null,EvaluationLaunch:()=>null},
    "@/features/scheduler-evidence":{SchedulerIntervalEvents:(props:typeof events[number])=>{events.push(props);return createElement("div",null,"기간 사건")}},
  }
  const conclusion=load<typeof ConclusionModule>("../features/experiments-conclusion.tsx",modules)
  modules["@/features/experiments-conclusion"]=conclusion
  const pages=load<typeof ExperimentsModule>("../features/experiments.tsx",modules)
  return {conclusion,pages,paths,historicalPaths,buttons,charts,events,selects,sheets,setSearch:(search:string)=>{params=new URLSearchParams(search)},setQueries:(value:typeof queries)=>{queries=value},render:(published=false)=>{paths.length=0;historicalPaths.length=0;buttons.length=0;charts.length=0;events.length=0;selects.length=0;sheets.length=0;return renderToStaticMarkup(createElement(published?pages.PublishedExperimentsPage:pages.ExperimentsPage))}}
}

test("historical errors retain candidate measurements and retry only failed explicit references",()=>{
  const app=harness(),retried:number[]=[]
  for(const error of [new ApiError(500,"",null),new ApiError(403,"",null),new Error("request timed out")]) {
    retried.length=0
    app.setQueries([{error,isPending:false,refetch:()=>{retried.push(0)}}])
    const html=app.render()
    assert.match(html,/role="alert"/)
    assert.match(html,/대상 게시 조회 실패/)
    assert.doesNotMatch(html,/대상 게시 없음/)
    assert.match(html,/공유 구간 차트/)
    assert.match(html,/선택 단계 원값/)
    app.buttons.find(button=>button.text.includes("다시 불러오기"))!.click()
    assert.deepEqual(retried,[0])
    assert.ok(!app.paths.some(path=>path.startsWith("v1/experiments?")))
    assert.deepEqual(app.historicalPaths[0],["v1/experiments/baseline"])
  }
  app.setQueries([{error:new ApiError(404,"",null),isPending:false}])
  assert.match(app.render(),/대상 게시 없음/)
  app.setQueries([{error:null,isPending:true}])
  const pending=app.render()
  assert.match(pending,/조회 중/)
  assert.doesNotMatch(pending,/대상 게시 없음/)
})

test("published list restores URL offset and never hydrates rows or historical comparisons",()=>{
  const app=harness()
  app.setSearch("view=published&offset=5&service=camera")
  const html=app.render(true)
  assert.deepEqual(app.paths,["v1/experiments?offset=5&limit=5"])
  assert.deepEqual(app.historicalPaths,[])
  assert.match(html,/href="\/experiments\?view=published&amp;offset=0&amp;service=camera"/)
  assert.match(html,/href="\/experiments\?view=published&amp;offset=10&amp;service=camera"/)
  assert.match(html,/href="\/experiments\/results\?a=candidate&amp;offset=5&amp;service=camera"/)
  assert.doesNotMatch(html,/단계별 원값|측정 규칙|최악 p95|에너지 Δ/)
  app.setSearch("view=published&offset=10")
  app.render(true)
  assert.deepEqual(app.paths,["v1/experiments?offset=10&limit=5"])
})

test("solo results show measured values without empty deltas, invented p95 or null-identity SLO lines",()=>{
  const solo:Experiment={...fixture,id:"candidate",comparisons:[],stages:[{id:"solo",from:fixture.from,to:fixture.to,kpis:{wholePoolJoules:1234,requests:{camera:{workloadUID:null,definitionID:null,offered:100,completedLatencyNS:{p95:20000000}},plc:{completedLatencyNS:{p95:30000000}}}}}]}
  const app=harness(solo),html=app.render()
  assert.match(html,/비교 없음/)
  assert.match(html,/1,234/)
  assert.match(html,/서비스 선택/)
  assert.doesNotMatch(html,/>차이<|기준 원값|후보 p95|기준 p95|전체 서비스 p95/)
  assert.ok(app.charts.some(chart=>chart.dataKey==="leftEnergy"))
  assert.ok(!app.charts.some(chart=>chart.dataKey.includes("P95")||chart.dataKey.includes("SLO")))
  assert.ok(html.indexOf('aria-label="공유 구간 차트"')<html.indexOf('aria-label="선택 단계 원값"'))
  assert.ok(html.indexOf('aria-label="선택 단계 원값"')<html.indexOf('aria-label="측정 규칙"'))
  app.setSearch("a=candidate&service=camera")
  app.render()
  assert.ok(app.charts.some(chart=>chart.dataKey==="leftP95"))
  assert.ok(!app.charts.some(chart=>chart.dataKey.includes("SLO")))
})

test("deep-linked Sheet sends the selected candidate or baseline period and exact return URL to events",()=>{
  const baseline:Experiment={...fixture,id:"baseline",stages:[{...fixture.stages[0],id:"baseline-stage",from:"2026-10-01T00:00:00Z",to:"2026-10-01T00:01:00Z"}]}
  const app=harness()
  app.setQueries([{error:null,data:baseline,isPending:false}])
  const selection=new URLSearchParams({...helpers.experimentIntervalSelection(run,helpers.experimentHeatmap(run,[run,baseline]).find(row=>row.other)!),service:"camera",offset:"5"})
  app.setSearch(selection.toString());app.render()
  assert.equal(app.events[0].from,run.stages[0].from)
  assert.equal(app.events[0].to,run.stages[0].to)
  assert.equal(app.events[0].label,"후보")
  assert.equal(app.events[0].returnTo,`/experiments/results?${selection}`)
  const legacy=new URLSearchParams(selection);legacy.delete("al")
  app.setSearch(legacy.toString());app.render()
  assert.equal(app.events[0].from,run.stages[0].from)
  selection.set("eventSide","baseline")
  app.setSearch(selection.toString());app.render()
  assert.deepEqual(app.events[0],{from:baseline.stages[0].from,to:baseline.stages[0].to,label:"기준",returnTo:`/experiments/results?${selection}`})
})

test("all measured chart lanes select the publication and stage identity rather than a filtered point index",()=>{
  const publication:Experiment={...fixture,id:"candidate",comparisons:[],stages:[{...fixture.stages[0],id:"no-p95",kpis:{wholePoolJoules:10,requests:{camera:{offered:100}}}},{...fixture.stages[0],id:"measured",kpis:{wholePoolJoules:20,requests:{camera:{offered:200,completedLatencyNS:{p95:10000000}}}}}]}
  const app=harness(publication)
  app.setSearch("a=candidate&service=camera&offset=5")
  app.render()
  const location=new URL("https://dashboard.invalid/experiments/results?a=candidate&service=camera&offset=5"),calls:string[]=[]
  const history={replaceState(_data:unknown,_unused:string,url?:string|URL|null){calls.push("replace");location.href=new URL(String(url),location).href},pushState(_data:unknown,_unused:string,url?:string|URL|null){calls.push("push");location.href=new URL(String(url),location).href}}
  const previousWindow=Object.getOwnPropertyDescriptor(globalThis,"window"),previousDocument=Object.getOwnPropertyDescriptor(globalThis,"document"),previousHTMLElement=Object.getOwnPropertyDescriptor(globalThis,"HTMLElement")
  Object.defineProperty(globalThis,"window",{configurable:true,value:{location,history}})
  Object.defineProperty(globalThis,"document",{configurable:true,value:{activeElement:null,body:null}})
  Object.defineProperty(globalThis,"HTMLElement",{configurable:true,value:class {}})
  try {
    const row=helpers.experimentHeatmap(publication).at(-1)!
    for(const key of ["observedRate","leftP95","leftEnergy"]) {
      const chart=app.charts.find(chart=>chart.dataKey===key)
      assert.ok(chart?.onClick,key)
      chart.onClick({payload:{key:row.key}})
      assert.equal(location.searchParams.get("a"),"candidate")
      assert.equal(location.searchParams.get("al"),"candidate")
      assert.equal(location.searchParams.get("as"),row.stage.id)
      assert.equal(location.searchParams.get("offset"),"5")
    }
    assert.equal(calls.length,3)
    app.buttons.find(button=>button.text.includes("구간 자세히"))!.click()
    assert.equal(location.searchParams.get("as"),helpers.experimentHeatmap(publication)[0].stage.id)
    app.buttons.find(button=>button.text.includes("camera 성공 응답 p95"))!.click()
    assert.equal(location.searchParams.get("as"),helpers.experimentHeatmap(publication)[0].stage.id)
    assert.equal(location.searchParams.get("service"),"camera")
    const interval=app.buttons.find(button=>button.text.includes("measured")&&!button.text.includes("no-p95"))
    assert.ok(interval)
    interval.click()
    assert.equal(location.searchParams.get("as"),row.stage.id)
  } finally {
    for(const [key,descriptor] of [["window",previousWindow],["document",previousDocument],["HTMLElement",previousHTMLElement]] as const)if(descriptor)Object.defineProperty(globalThis,key,descriptor);else Reflect.deleteProperty(globalThis,key)
  }
})

function withSelectionBrowser(app:{setSearch:(search:string)=>void},search:string,check:(location:URL)=>void) {
  const location=new URL(`https://dashboard.invalid/experiments/results?${search}`)
  const change=(_data:unknown,_unused:string,url?:string|URL|null)=>{location.href=new URL(String(url),location).href;app.setSearch(location.search)}
  const saved=["window","document","HTMLElement"].map(key=>[key,Object.getOwnPropertyDescriptor(globalThis,key)] as const)
  Object.defineProperty(globalThis,"window",{configurable:true,value:{location,history:{replaceState:change,pushState:change}}})
  Object.defineProperty(globalThis,"document",{configurable:true,value:{activeElement:null,body:null}})
  Object.defineProperty(globalThis,"HTMLElement",{configurable:true,value:class {}})
  app.setSearch(search)
  try {check(location)} finally {
    for(const [key,descriptor] of saved)if(descriptor)Object.defineProperty(globalThis,key,descriptor);else Reflect.deleteProperty(globalThis,key)
  }
}

test("IA review: closed comparison changes persist identity and reload without opening a Sheet",()=>{
  const stages=["first","base-first","later","base-later"].map((id,index)=>({...fixture.stages[0],id,kpis:{wholePoolJoules:100+index}}))
  const publication:Experiment={...fixture,id:"candidate",stages,comparisons:[{left:{stageID:"first"},right:{stageID:"base-first"},reason:"first pair"},{left:{stageID:"later"},right:{stageID:"base-later"},reason:"later pair"}]}
  const app=harness(publication),row=helpers.experimentHeatmap(publication).find(row=>row.stage.id==="later")!
  withSelectionBrowser(app,"a=candidate&offset=5&service=camera",location=>{
    app.render()
    app.selects[0].onChange({target:{value:row.key}})
    assert.equal(location.searchParams.get("as"),"later")
    const html=app.render()
    assert.match(html,/later · 단계 집계/)
    assert.equal(app.sheets.at(-1)?.open,false)
    const reload=harness(publication);reload.setSearch(location.search)
    assert.match(reload.render(),/later · 단계 집계/)
    assert.equal(reload.sheets.at(-1)?.open,false)
  })
})

test("IA review: opening then closing a nondefault interval preserves its values, including direct deep links",()=>{
  const publication:Experiment={...fixture,id:"candidate",comparisons:[],stages:["first","later"].map((id,index)=>({...fixture.stages[0],id,kpis:{wholePoolJoules:100+index}}))}
  for(const direct of [false,true]) {
    const app=harness(publication)
    withSelectionBrowser(app,`a=candidate&service=camera&offset=5${direct?"&al=candidate&as=later":""}`,location=>{
      app.render()
      if(!direct)app.charts.find(chart=>chart.dataKey==="leftEnergy")!.onClick!({payload:{key:helpers.experimentHeatmap(publication).find(row=>row.stage.id==="later")!.key}})
      assert.match(app.render(),/later · 단계 집계/)
      assert.equal(app.sheets.at(-1)?.open,true)
      app.sheets.at(-1)!.onOpenChange(false)
      assert.match(app.render(),/later · 단계 집계/)
      assert.equal(location.searchParams.get("as"),"later")
      assert.equal(app.sheets.at(-1)?.open,false)
      assert.equal(app.events.length,0)
      assert.equal(location.searchParams.get("offset"),"5")
      assert.equal(location.searchParams.get("service"),"camera")
    })
  }
})
