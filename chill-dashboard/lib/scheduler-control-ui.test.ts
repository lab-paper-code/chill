import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement, type ComponentType, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as history from "./scheduler-history.ts"
import * as display from "./live-display.ts"
import * as scheduler from "./scheduler.ts"
import * as dates from "./date-time.ts"
import * as identity from "./workload-identity.ts"
import * as labels from "./labels.ts"
import { compareAt } from "./live.ts"
import type { PathFeedback } from "./read-types.ts"

const require=createRequire(import.meta.url)
const fixture=JSON.parse(readFileSync(new URL("./fixtures/e5-scheduler-control.json",import.meta.url),"utf8"))
function controlComponents(){
  const exports:Record<string,ComponentType<Record<string,unknown>>>={}
  const source=readFileSync(new URL("../features/scheduler-workload-states.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>name==="@/lib/live-display"?display:require(name),exports)
  return exports
}

test("recorded Keep retains PLC/Smol Controlled and only IC carries the instance reason",()=>{
  const components=controlComponents(),names=new Map<string,string>(fixture.snapshot.workloads.map((row:{uid:string;name:string})=>[row.uid,row.name]))
  const html=renderToStaticMarkup(createElement(components.SchedulerWorkloadStates,{workloads:fixture.decision.workloads,names}))
  const rows=html.match(/<tr\b[^>]*>[\s\S]*?<\/tr>/g)!
  for(const service of ["plc-anomaly","smolvlm2-vqa"]){
    const row=rows.find(row=>row.includes(service))!
    assert.match(row,/Controlled/)
    assert.doesNotMatch(row,/UnconfirmedInstance|인스턴스 미확인/)
    assert.match(row,/미기록/)
  }
  const fixed=rows.find(row=>row.includes("image-classification-tight-slo"))!
  assert.match(fixed,/Fixed/)
  assert.match(fixed,/인스턴스 미확인/)
  assert.match(fixed,/<details>[\s\S]*containerd:/)
  const reasons=renderToStaticMarkup(createElement(components.SchedulerDecisionReasons,{reasons:fixture.decision.reasons}))
  assert.match(reasons,/인스턴스 미확인/)
  assert.match(renderToStaticMarkup(createElement(components.SchedulerWorkloadStates,{workloads:null,names})),/미기록/)
})

test("decision path budgets preserve configured zero, empirical fractions and absent source",()=>{
  const {SchedulerPathFeedback}=controlComponents()
  const pathFeedback:PathFeedback[]=[
    {workloadUID:"configured-uid",percentile:95,totalNanos:0,postNanos:0,sampleCount:0,source:"configured"},
    {workloadUID:"empirical-uid",percentile:99,totalNanos:40000000.5,postNanos:30000000.25,sampleCount:17,source:"empirical"},
    {workloadUID:"unknown-source-uid",percentile:90,totalNanos:7100000,postNanos:3200000,sampleCount:2},
  ]
  const names=new Map(pathFeedback.map(row=>[row.workloadUID,`service-${row.workloadUID}`]))
  const html=renderToStaticMarkup(createElement(SchedulerPathFeedback,{decision:{pathFeedback},names}))
  const rows=html.match(/<tr\b[^>]*>[\s\S]*?<\/tr>/g)!.slice(1)
  assert.equal(rows.length,pathFeedback.length)
  rows.forEach((row,index)=>{
    const feedback=pathFeedback[index]
    assert.ok(row.includes(names.get(feedback.workloadUID)!))
    assert.ok(row.includes(feedback.workloadUID))
    const cells=[...row.matchAll(/<td\b[^>]*>(.*?)<\/td>/g)].map(match=>match[1])
    assert.deepEqual(cells.slice(0,3),[feedback.percentile,feedback.totalNanos/1e6,feedback.postNanos/1e6].map(String))
    assert.equal(cells[4],String(feedback.sampleCount))
    if(feedback.source)assert.equal(cells[3],feedback.source)
    else {
      assert.ok(cells[3].length>0)
      assert.ok(!["0","configured","empirical"].includes(cells[3]))
    }
    assert.equal(cells.length,5)
  })
  for(const decision of [{},{pathFeedback:null},{pathFeedback:[]}]){
    assert.equal(renderToStaticMarkup(createElement(SchedulerPathFeedback,{decision,names})),"")
  }
})


test("rendered timeline shows only loaded-page transitions and does not fetch remaining pages",()=>{
  const params=new URLSearchParams({from:"2026-11-03T19:44:00Z",to:"2026-11-03T20:16:00Z",cursor:"50",limit:"3",timeline:"scope"})
  const repeated={...fixture,offset:51,decision:{...fixture.decision,at:"2026-11-03T19:48:16Z"}}
  const changed={...fixture,offset:52,at:"2026-11-03T19:48:26Z",decision:{...fixture.decision,at:"2026-11-03T19:48:26Z",workloads:fixture.decision.workloads.map((row:{uid:string})=>({uid:row.uid,state:"Controlled"}))}}
  const calls:string[]=[],wrap=({children}:{children?:ReactNode})=>createElement("div",{},children),none=()=>null
  const modules:Record<string,unknown>={
    "next/link":{default:"a"},"next/navigation":{usePathname:()=>"/records",useSearchParams:()=>params,useRouter:()=>({replace:()=>{}})},
    "@/lib/client":{useSession:()=>({config:{pageSize:3,maxPageSize:100,maxWindowHours:24}}),useChillQuery:(path:string,options?:{enabled?:boolean})=>{if(options?.enabled===false)return {};calls.push(path);return {data:path.startsWith("v1/workloads")?fixture.snapshot.workloads.map((metadata:unknown)=>({desired:{metadata}})):{events:[{...fixture,offset:50},repeated,changed],nextCursor:"53"},isSuccess:true}}},
    "@/lib/scheduler":scheduler,"@/lib/scheduler-history":history,"@/lib/live-display":display,"@/lib/date-time":dates,"@/lib/workload-identity":identity,"@/lib/labels":labels,
    "@/lib/live":{compareAt},
    "@/features/scheduler":{judgmentLabels:{Keep:"현재 구성 유지"}},"@/features/scheduler-settings":{},"./scheduler-transitions":{},
    "./scheduler-workload-states":controlComponents(),
    "@/components/read-region":{QueryGate:wrap,SourceJSON:none,SourceExport:none},
    "@/components/screen":{Screen:wrap,Rows:wrap,Row:none,RowDialog:none,Facts:none,Empty:wrap},
    "@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},
    "@/components/ui/sheet":{Sheet:none,SheetContent:wrap,SheetHeader:wrap,SheetTitle:wrap},
    "@/components/records-navigation":{RecordsNavigation:none,RecordsRange:none},
  }
  const exports:Record<string,ComponentType>={}
  const source=readFileSync(new URL("../features/scheduler-history.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
  const html=renderToStaticMarkup(createElement(exports.SchedulerHistoryPage))
  assert.equal((html.match(/<caption[^>]*>서비스별 자율 상태<\/caption>/g)??[]).length,2)
  assert.match(html,/현재 페이지/)
  assert.match(html,/이전 페이지와의 변화는 미확인/)
  assert.match(html,/현재 구성 유지 \(Keep\)/)
  assert.match(html,/Controlled/)
  assert.match(html,/Fixed/)
  assert.match(html,/인스턴스 미확인/)
  assert.match(html,/<details><summary[^>]*>[\s\S]*?<\/summary>[\s\S]*?containerd:/)
  assert.doesNotMatch(html,/<details[^>]*\bopen\b/)
  assert.equal((html.match(/>판단 근거<\/button>/g)??[]).length,2)
  const requests=calls.filter(path=>path.startsWith("v1/observations/scheduler"))
  assert.equal(requests.length,1)
  const request=new URL(requests[0],"https://dashboard.invalid")
  assert.equal(request.searchParams.get("view"),"compact")
  assert.equal(request.searchParams.get("cursor"),"50")
  assert.equal(request.searchParams.get("limit"),"3")
})
