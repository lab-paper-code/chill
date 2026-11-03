import assert from "node:assert/strict"
import test from "node:test"
import * as React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { load } from "./evaluation-author-test-helpers.ts"
import * as request from "./evaluation-request.ts"
import * as author from "./evaluation-author.ts"
import * as scheduler from "./scheduler.ts"
import * as active from "./evaluation-active.ts"
import * as experiments from "./experiments.ts"
import * as comparison from "./experiment-comparison.ts"
import * as measurements from "./measurements.ts"

const scenario={name:"unrelated-load",uid:"load-uid",spec:{workloads:[{name:"camera"},{name:"sensor"}],parts:[{id:"gentle",durationSeconds:120,rates:{camera:4.25,sensor:0}},{id:"peak",durationSeconds:180,rates:{camera:8.5}}]}}
const baseRun={name:"arbitrary-run",uid:"selected",phase:"Done",currentStage:"trial",startedAt:"2026-11-03T00:00:00Z",finishedAt:"2026-11-03T00:06:00Z",spec:{question:"A separately registered question",stages:[{id:"trial",loadScenarioRef:{name:scenario.name,uid:scenario.uid}}]},conditions:[{type:"PoolAcquired",status:"False",reason:"Returned",lastTransitionTime:"2026-11-03T00:06:00Z"},{type:"ExecutionComplete",status:"True",reason:"Complete"},{type:"MeasurementPassed",status:"False",reason:"SLOFailed"},{type:"CleanupComplete",status:"True",reason:"ProcedureComplete"},{type:"Published",status:"True",reason:"Complete"}]}
function renderRun(run:Record<string,unknown>,publication?:Record<string,unknown>,loads=[scenario]) {
  const modules={react:React,"next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams("uid=selected"),useRouter:()=>({replace:()=>{}})},"@tanstack/react-query":{useQueryClient:()=>({})},"@/lib/client":{useChillQuery:(path:string)=>({data:path.startsWith("v1/experiments/")?publication:path==="v1/evaluations/load-scenarios"?{items:loads}:run})},"@/lib/evaluation-request":request,"@/lib/evaluation-author":author,"@/lib/scheduler":scheduler,"@/lib/experiments":experiments,"@/lib/experiment-comparison":comparison,"@/lib/measurements":measurements,"@/components/screen":{Screen:({children}:{children:React.ReactNode})=>React.createElement("main",null,children)},"@/components/read-region":{QueryGate:"div",SourceJSON:"pre"},"@/components/ui/button":{Button:"button"},"@/components/ui/tooltip":{TooltipProvider:"div",Tooltip:"div",TooltipTrigger:"span",TooltipContent:"aside"},"@/features/evaluation-active":{EvaluationActive:()=>null}}
  const {EvaluationRunPage}=load("../features/evaluation-request.tsx",modules)
  return renderToStaticMarkup(React.createElement(EvaluationRunPage as React.ComponentType<{name:string}>,{name:"arbitrary-run"}))
}
const text=(html:string)=>html.replace(/<[^>]+>/g,"")

test("run detail describes referenced load without interpreting the run name or treating a missing rate as zero",()=>{
  const html=renderRun(baseRun),screen=text(html)
  assert.match(screen,/워크로드 2개/)
  assert.match(screen,/300.*초/)
  assert.match(screen,/A separately registered question/)
  assert.match(screen,/gentle.*120.*4\.25.*0/)
  assert.match(screen,/peak.*180.*8\.5.*미기록/)
  assert.match(screen,/풀 확보.*반환 완료/)
  assert.match(screen,/2026.*11.*04|2026.*11.*03/)
  assert.doesNotMatch(screen,/원문 확인|반환 관측|현재 단계/)
  assert.match(html,/<details><summary[^>]*>평가 대상 CHILL/)
  assert.match(html,/<details><summary[^>]*>조건·정의 원문/)
})

test("finished run uses publisher D1 failures and its frozen load instead of current same-name scenarios",()=>{
  const publication={id:"publication",status:"FAIL",definition:{run:{name:baseRun.name,uid:baseRun.uid},spec:baseRun.spec,loadScenarios:[scenario]},stages:[{id:"trial",kpis:{requests:{camera:{D1:"FAIL"},sensor:{D1:"OBSERVED_THRESHOLD_MET"}}}}],outcome:{measurement:{verdict:"FAIL",reason:"SLOFailed"}}}
  const html=renderRun({...baseRun,publicationID:"publication"},publication,[{...scenario,spec:{...scenario.spec,parts:[]}}]),screen=text(html)
  assert.match(screen,/SLO 미충족.*camera/)
  assert.doesNotMatch(html.match(/<h2\b[^>]*>[\s\S]*?<\/h2>/)?.[0]??"",/sensor/)
  assert.match(screen,/300.*초/)
  assert.match(screen,/결과 보기/)
})

test("same-name replacement load never supplies the selected run's missing plan",()=>{
  const screen=text(renderRun(baseRun,undefined,[{...scenario,uid:"replacement"}]))
  assert.match(screen,/부하 계획.*미기록/)
  assert.doesNotMatch(screen,/gentle|4\.25/)
})

test("active stage shows GO-based elapsed, remaining and a progress bar only for the selected UID",()=>{
  const entry:active.ActiveEvaluation={name:"arbitrary-run",uid:"selected",stageIndex:1,stagesTotal:2,stage:{id:"trial",goAt:"2026-11-03T00:00:00Z",plannedSeconds:300,injections:[]}}
  const modules={"@/lib/client":{useChillQuery:()=>({data:{items:[entry]}})},"@/lib/evaluation-active":active}
  const {EvaluationActive}=load("../features/evaluation-active.tsx",modules)
  const render=(uid:string)=>renderToStaticMarkup(React.createElement(EvaluationActive as React.ComponentType<{uid:string;now:number}>,{uid,now:Date.parse("2026-11-03T00:02:00Z")}))
  let html=render("selected")
  assert.match(text(html),/현재 단계.*trial/)
  assert.match(text(html),/경과.*120.*남은.*180/)
  assert.match(html,/<progress[^>]*value="120"[^>]*max="300"|<progress[^>]*max="300"[^>]*value="120"/)
  assert.doesNotMatch(render("replacement"),/<progress/)
  if(entry.stage)delete entry.stage.goAt
  html=render("selected")
  assert.doesNotMatch(html,/<progress/)
  assert.match(text(html),/미기록/)
})
