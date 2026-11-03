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
import * as settings from "./scheduler-settings.ts"
import * as labels from "./labels.ts"

const scenario={name:"unrelated-load",uid:"load-uid",spec:{workloads:[{name:"camera"},{name:"sensor"}],arrivalDistribution:{camera:"poisson",sensor:"constant"},parts:[{id:"gentle",durationSeconds:120,rates:{camera:4.25,sensor:0}},{id:"peak",durationSeconds:180,rates:{camera:8.5}}]}}
const baseRun={name:"arbitrary-run",uid:"selected",phase:"Done",currentStage:"trial",startedAt:"2026-11-03T00:00:00Z",finishedAt:"2026-11-03T00:06:00Z",spec:{question:"A separately registered question",stages:[{id:"trial",loadScenarioRef:{name:scenario.name,uid:scenario.uid}}]},conditions:[{type:"PoolAcquired",status:"False",reason:"Returned",lastTransitionTime:"2026-11-03T00:06:00Z"},{type:"ExecutionComplete",status:"True",reason:"Complete"},{type:"MeasurementPassed",status:"False",reason:"SLOFailed"},{type:"CleanupComplete",status:"True",reason:"ProcedureComplete"},{type:"Published",status:"True",reason:"Complete"}]}
function renderRun(run:Record<string,unknown>,publication?:Record<string,unknown>,loads=[scenario],entries:active.ActiveEvaluation[]=[]) {
  const clock={...React,useState:(initial:unknown)=>React.useState(initial===0?Date.parse("2026-11-03T00:02:30Z"):initial)}
  const modules={react:clock,"next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams("uid=selected"),useRouter:()=>({replace:()=>{}})},"@tanstack/react-query":{useQueryClient:()=>({})},"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/evaluations/active"?{items:entries}:path.startsWith("v1/experiments/")?publication:path==="v1/evaluations/load-scenarios"?{items:loads}:run})},"@/lib/evaluation-request":request,"@/lib/evaluation-author":author,"@/lib/evaluation-active":active,"@/lib/scheduler-settings":settings,"@/lib/labels":labels,"@/lib/scheduler":scheduler,"@/lib/experiments":experiments,"@/lib/experiment-comparison":comparison,"@/lib/measurements":measurements,"@/components/screen":{Screen:({children}:{children:React.ReactNode})=>React.createElement("main",null,children)},"@/components/read-region":{QueryGate:"div",SourceJSON:"pre"},"@/components/ui/button":{Button:"button"},"@/components/ui/tooltip":{TooltipProvider:"div",Tooltip:"div",TooltipTrigger:"span",TooltipContent:"aside"},"@/features/evaluation-active":{EvaluationActive:()=>null}}
  Object.assign(modules["@/features/evaluation-active"],{EvaluationActiveDetails:()=>null})
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

test("post-load active stage explicitly reports postprocessing rather than continuing load",()=>{
  const entry={name:"arbitrary-run",uid:"selected",stageIndex:1,stagesTotal:1,stage:{id:"trial",goAt:"2026-11-03T00:00:00Z",plannedSeconds:120,injections:[]}}
  const {EvaluationActive}=load("../features/evaluation-active.tsx",{"@/lib/client":{useChillQuery:()=>({data:{items:[entry]}})},"@/lib/evaluation-active":active})
  const html=renderToStaticMarkup(React.createElement(EvaluationActive as React.ComponentType<{now:number}>,{now:Date.parse("2026-11-03T00:02:00Z")}))
  assert.match(text(html),/부하 종료 · 후처리 중/)
  assert.match(text(html),/GO 이후 경과.*120/)
  assert.doesNotMatch(html,/<summary[^>]*>부하 진행|aria-label="trial 부하 진행"/)
})

test("operator reads service distributions and labeled run and stage setting changes without JSON",()=>{
  const run={...baseRun,spec:{...baseRun.spec,settingsPatch:{maxReplicas:7,controls:{routing:false}},stages:[{...baseRun.spec.stages[0],settingsPatch:{controlIntervalNanos:2000000000}}]}}
  const html=renderRun(run).replace(/<pre\b[^>]*>[\s\S]*?<\/pre>/g,""),screen=text(html)
  assert.match(screen,/도착 분포.*camera.*푸아송/)
  assert.match(screen,/sensor.*일정/)
  assert.match(screen,/변경 설정.*복제 수 상한.*7/)
  assert.match(screen,/요청 분배.*비활성/)
  assert.match(screen,/운영 판단 주기.*2/)
  assert.match(text(renderRun(baseRun)),/변경 설정.*변경 없음/)
})

test("current part follows cumulative durations only with matching run identity and server GO and plan",()=>{
  const run={...baseRun,phase:"Running",finishedAt:undefined}
  const entry:active.ActiveEvaluation={name:baseRun.name,uid:baseRun.uid,stageIndex:1,stagesTotal:1,stage:{id:"trial",goAt:baseRun.startedAt,plannedSeconds:300,injections:[]}}
  const current=(entries:active.ActiveEvaluation[])=>renderRun(run,undefined,[scenario],entries).match(/<tr\b[^>]*aria-current="step"[^>]*>[\s\S]*?<\/tr>/g)??[]
  assert.equal(current([entry]).length,1)
  assert.match(current([entry])[0]??"",/peak/)
  assert.doesNotMatch(current([entry])[0]??"",/gentle/)
  assert.equal(current([{...entry,uid:"replacement"}]).length,0)
  assert.match(current([{...entry,stage:{...entry.stage!,goAt:"2026-11-03T00:01:00Z"}}])[0]??"",/gentle/)
  assert.equal(current([{...entry,stage:{...entry.stage!,id:"other-stage"}}]).length,0)
  assert.equal(current([{...entry,name:"other"}]).length,0)
  assert.equal(current([{...entry,stage:{...entry.stage!,goAt:undefined}}]).length,0)
  assert.equal(current([{...entry,stage:{...entry.stage!,plannedSeconds:undefined as unknown as number}}]).length,0)
  assert.equal(current([{...entry,stage:{...entry.stage!,plannedSeconds:120}}]).length,0)
})

test("published plan settings and distributions stay frozen and unfamiliar distributions remain visible",()=>{
  const frozenSpec={...baseRun.spec,settingsPatch:{maxReplicas:3},stages:[{...baseRun.spec.stages[0],settingsPatch:{maxInstances:5}}]}
  const frozenLoad={...scenario,spec:{...scenario.spec,arrivalDistribution:{camera:"poisson",sensor:"future-arrivals"}}}
  const publication={id:"publication",status:"PASS",definition:{run:{name:baseRun.name,uid:baseRun.uid},spec:frozenSpec,loadScenarios:[frozenLoad]},stages:[]}
  const screen=text(renderRun({...baseRun,publicationID:"publication",spec:{...baseRun.spec,settingsPatch:{maxReplicas:99}}},publication))
  assert.match(screen,/복제 수 상한.*3/)
  assert.match(screen,/인스턴스 수 상한.*5/)
  assert.match(screen,/sensor.*future-arrivals/)
  assert.doesNotMatch(screen,/복제 수 상한99/)
})

test("new-server procedure histories preserve recorded order, ongoing timing and child stage identity",()=>{
  const html=renderRun({...baseRun,phase:"Running",finishedAt:undefined,currentProcedure:"stage",procedures:[
    {step:"prepare",startedAt:"2026-11-03T00:00:00Z",finishedAt:"2026-11-03T00:00:05Z",reason:"Prepared"},
    {step:"stage",startedAt:"2026-11-03T00:00:05Z",finishedAt:null},
  ],stages:[
    {id:"recorded-first",name:"child-b",uid:"child-b-uid",currentProcedure:"judge",startedAt:"2026-11-03T00:01:00Z",finishedAt:null,goAt:"2026-11-03T00:01:10Z",conditions:[],measurementVerdict:"FAIL",procedures:[{step:"judge",startedAt:"2026-11-03T00:02:00Z",finishedAt:null,reason:"SLOFailed",measurementVerdict:"FAIL"}]},
    {id:"trial",name:"child-a",uid:"child-a-uid",startedAt:"2026-11-03T00:00:10Z",finishedAt:"2026-11-03T00:00:50Z",conditions:[],procedures:[{step:"finish",startedAt:"2026-11-03T00:00:45Z",finishedAt:"2026-11-03T00:00:50Z",reason:"Complete"}]},
  ]}),screen=text(html)
  assert.match(screen,/실행 절차.*Prepared/)
  assert.match(html,/<tr\b[^>]*aria-current="step"[^>]*>[\s\S]*?현재 절차[\s\S]*?145초/)
  assert.match(screen,/진행 중/)
  assert.match(screen,/실행 단계 recorded-first.*SLO 미충족.*FAIL/)
  assert.ok(screen.indexOf("실행 단계 recorded-first")<screen.indexOf("실행 단계 trial"))
  assert.match(screen,/실행 단계 trial.*5초.*Complete/)
  assert.doesNotMatch(screen,/조건별 최신 상태/)
})

test("absent or null procedure histories retain old-server conditions without inventing a timeline",()=>{
  for(const history of [{},{procedures:null,stages:null}]) {
    const html=renderRun({...baseRun,...history}),screen=text(html)
    assert.match(screen,/조건별 최신 상태.*풀 확보.*반환 완료/)
    assert.doesNotMatch(screen,/실행 절차|현재 절차/)
  }
  assert.match(text(renderRun({...baseRun,procedures:[],stages:[]})),/절차 기록 없음/)
})
