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
function renderRun(run:Record<string,unknown>,publication?:Record<string,unknown>,loads=[scenario],entries:active.ActiveEvaluation[]=[],queries:Record<string,Record<string,unknown>>={}) {
  const clock={...React,useState:(initial:unknown)=>React.useState(initial===0?Date.parse("2026-11-03T00:02:30Z"):initial)}
  const query=(path:string)=>queries[path]??{data:path==="v1/evaluations/active"?{items:entries}:path.startsWith("v1/experiments/")?publication:path==="v1/evaluations/load-scenarios"?{items:loads}:run}
  const modules={react:clock,"next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams("uid=selected"),useRouter:()=>({replace:()=>{}})},"@tanstack/react-query":{useQueryClient:()=>({})},"@/lib/client":{useChillQuery:query},"@/lib/evaluation-request":request,"@/lib/evaluation-author":author,"@/lib/evaluation-active":active,"@/lib/scheduler-settings":settings,"@/lib/labels":labels,"@/lib/scheduler":scheduler,"@/lib/experiments":experiments,"@/lib/experiment-comparison":comparison,"@/lib/measurements":measurements,"@/components/screen":{Screen:({children}:{children:React.ReactNode})=>React.createElement("main",null,children)},"@/components/read-region":{QueryGate:"div",SourceJSON:"pre"},"@/components/ui/button":{Button:"button"},"@/components/ui/tooltip":{TooltipProvider:"div",Tooltip:"div",TooltipTrigger:"span",TooltipContent:"aside"},"@/features/evaluation-active":{EvaluationActiveDetails:()=>null}}
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
  const {EvaluationActiveDetails}=load("../features/evaluation-active.tsx",{"@/lib/evaluation-active":active})
  const render=(uid:string)=>renderToStaticMarkup(React.createElement(EvaluationActiveDetails as React.ComponentType<{active:unknown;uid:string;now:number}>,{active:{data:{items:[entry]}},uid,now:Date.parse("2026-11-03T00:02:00Z")}))
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

test("GO plus planned duration never claims load completion or postprocessing",()=>{
  const entry={name:"arbitrary-run",uid:"selected",stageIndex:1,stagesTotal:1,stage:{id:"trial",goAt:"2026-11-03T00:00:00Z",plannedSeconds:120,injections:[]}}
  const {EvaluationActiveDetails}=load("../features/evaluation-active.tsx",{"@/lib/evaluation-active":active})
  const html=renderToStaticMarkup(React.createElement(EvaluationActiveDetails as React.ComponentType<{active:unknown;now:number}>,{active:{data:{items:[entry]}},now:Date.parse("2026-11-03T00:02:00Z")}))
  assert.match(text(html),/계획 시간 경과/)
  assert.match(text(html),/GO 이후 경과.*120/)
  assert.doesNotMatch(html,/부하 종료|후처리 중|부하 진행/)
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
  assert.match(text(renderRun(run,undefined,[scenario],[entry])),/계획상 구간 peak/)
  assert.doesNotMatch(text(renderRun(run,undefined,[scenario],[entry])),/현재 부하/)
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
  assert.match(screen,/조건별 최신 상태.*풀 확보.*반환 완료/)
})

test("absent or null procedure histories retain old-server conditions without inventing a timeline",()=>{
  for(const history of [{},{procedures:null,stages:null}]) {
    const html=renderRun({...baseRun,...history}),screen=text(html)
    assert.match(screen,/조건별 최신 상태.*풀 확보.*반환 완료/)
    assert.doesNotMatch(screen,/실행 절차|현재 절차/)
  }
  assert.match(text(renderRun({...baseRun,procedures:[],stages:[]})),/절차 기록 없음/)
})

test("missing procedure times remain missing and only an unfinished owner's current procedure runs to now",()=>{
  const procedures=[
    {step:"prepare",startedAt:"2026-11-03T00:00:00Z",finishedAt:null},
    {step:"stage",startedAt:"2026-11-03T00:02:00Z",finishedAt:null},
    {step:"judge",startedAt:null,finishedAt:"2026-11-03T00:02:20Z"},
  ]
  const run={...baseRun,phase:"Running",finishedAt:undefined,currentProcedure:"stage",procedures}
  const rows=(html:string)=>[...html.matchAll(/<tr\b[^>]*>[\s\S]*?<\/tr>/g)].map(match=>text(match[0]))
  const history=rows(renderRun(run))
  assert.match(history.find(row=>row.startsWith("준비"))??"",/종료 미기록.*미기록/)
  assert.doesNotMatch(history.find(row=>row.startsWith("준비"))??"",/진행 중|150초/)
  assert.match(history.find(row=>row.startsWith("부하 실행"))??"",/현재 절차.*진행 중.*30초/)
  assert.match(history.find(row=>row.startsWith("측정 판정"))??"",/시작 미기록.*미기록/)
  assert.doesNotMatch(history.find(row=>row.startsWith("측정 판정"))??"",/초|1970/)
  assert.doesNotMatch(text(renderRun(run)),/1970/)
  for(const finished of [{...run,finishedAt:baseRun.finishedAt},{...run,phase:"Done"}]) {
    const row=rows(renderRun(finished)).find(row=>row.startsWith("부하 실행"))??""
    assert.match(row,/종료 미기록.*미기록/)
    assert.doesNotMatch(row,/현재 절차|진행 중|30초/)
  }
  const child={id:"child",uid:"child-uid",name:"child",finishedAt:baseRun.finishedAt,currentProcedure:"stage",procedures}
  const childRows=rows(renderRun({...run,procedures:[],stages:[child]}))
  assert.doesNotMatch(childRows.find(row=>row.startsWith("부하 실행"))??"",/현재 절차|진행 중|30초/)
})

test("procedure histories retain latest run and child conditions with unknown reasons and transition times",()=>{
  const conditions=[{type:"PoolAcquired",status:"False",reason:"Returned",lastTransitionTime:"2026-11-03T00:06:00Z"},{type:"ExecutionComplete",status:"False",reason:"FutureExecutionFailure",lastTransitionTime:"2026-11-03T00:05:00Z"},{type:"CleanupComplete",status:"False",reason:"FutureCleanupFailure",lastTransitionTime:"2026-11-03T00:05:30Z"}]
  const html=renderRun({...baseRun,procedures:[],conditions,stages:[{id:"child",name:"child",uid:"child-uid",procedures:[],conditions:[{type:"ExecutionComplete",status:"False",reason:"ChildExecutionFailure",lastTransitionTime:"2026-11-03T00:04:00Z"}]}]})
  assert.match(text(html),/풀 확보.*반환 완료/)
  for(const reason of ["FutureExecutionFailure","FutureCleanupFailure","ChildExecutionFailure"])assert.match(text(html),new RegExp(reason))
  for(const condition of [...conditions,{lastTransitionTime:"2026-11-03T00:04:00Z"}])assert.ok(html.includes(`dateTime="${condition.lastTransitionTime}"`))
})

test("unacquired frozen definitions never become empty plans or unchanged settings or current live data",()=>{
  const run={...baseRun,publicationID:"publication",spec:{...baseRun.spec,settingsPatch:{maxReplicas:99}}}
  const cases=[
    {query:{isPending:true},label:/조회 중/},
    {query:{error:{status:503,message:"offline"}},label:/조회 실패/},
    {query:{data:{id:"publication",status:"PASS"}},label:/미기록/},
    {query:{data:{id:"publication",definition:{run:{name:run.name,uid:"other"},spec:run.spec,loadScenarios:[scenario]}}},label:/미기록/},
    {query:{data:{id:"other",definition:{run:{name:run.name,uid:run.uid},spec:run.spec,loadScenarios:[scenario]}}},label:/미기록/},
  ]
  for(const {query,label} of cases) {
    const screen=text(renderRun(run,undefined,[scenario],[],{"v1/experiments/publication":query}))
    assert.match(screen,/실행 단계 (조회 중|조회 실패|미기록)/)
    assert.match(screen,label)
    assert.doesNotMatch(screen,/실행 단계 0개|변경 없음|복제 수 상한99|gentle|푸아송/)
  }
})

test("publisher preparation failure preserves recorded spec and confirmed empty snapshot collections through decoding",()=>{
  const spec={...baseRun.spec,loadGenerator:{image:"generator:recorded",sourceSHA:"recorded-commit",nodeName:"generator",resources:{},shards:{camera:1},barrier:{maxStartSkewNanos:0,minimumReadySamples:1}},stages:baseRun.spec.stages.map(stage=>({...stage,row:"recorded-row"}))}
  const publication=experiments.decodeExperiment(JSON.stringify({schemaVersion:"serving-experiment.v1alpha1",id:"publication",from:baseRun.startedAt,to:baseRun.finishedAt,status:"NOT_EVALUATED",definition:{schema:"evaluation-definition.v1alpha1",run:{name:baseRun.name,uid:baseRun.uid},spec,loadScenarios:[],workloads:[]},stages:[],sources:[],outcome:{execution:{status:"Failed",reason:"PreparationFailed"},measurement:{verdict:"NOT_EVALUATED"}}}))
  const screen=text(renderRun({...baseRun,publicationID:"publication",spec:{...baseRun.spec,question:"current live question",settingsPatch:{maxReplicas:99}}},publication))
  assert.match(screen,/등록 질문 A separately registered question/)
  assert.match(screen,/실행 단계 1개/)
  assert.match(screen,/워크로드 미기록.*부하 구간 미기록/)
  assert.match(screen,/변경 설정.*변경 없음/)
  assert.doesNotMatch(screen,/current live question|복제 수 상한99|실행 단계 0개/)
})

test("load catalog pending and errors are source states rather than recorded empty distributions",()=>{
  for(const query of [{isPending:true},{error:{status:503}}]) {
    const screen=text(renderRun(baseRun,undefined,[],[],{"v1/evaluations/load-scenarios":query}))
    assert.match(screen,/부하 계획 조회 (중|실패)/)
    assert.doesNotMatch(screen,/gentle|푸아송/)
  }
})

test("pending phase does not invent waiting conditions without condition evidence",()=>{
  const screen=text(renderRun({...baseRun,phase:"Pending",conditions:[],startedAt:undefined,finishedAt:undefined}))
  assert.match(screen,/조건별 최신 상태.*풀 확보.*미기록/)
  assert.doesNotMatch(screen,/풀 확보대기|실행대기|정리대기/)
})

test("finished phase without a finish timestamp never accumulates wall-clock duration",()=>{
  const screen=text(renderRun({...baseRun,phase:"Done",finishedAt:undefined}))
  assert.match(screen,/전체 실행 미기록/)
  assert.doesNotMatch(screen,/실행 경과 150초|전체 실행 150초/)
})

test("additional recorded stage condition types preserve their state reason and transition timestamp",()=>{
  const conditions=[{type:"StageCleanupComplete",status:"False",reason:"ChildCleanupFailure",lastTransitionTime:"2026-11-03T00:04:00Z"},{type:"FutureCondition",status:"Unknown",reason:"FutureReason",lastTransitionTime:"2026-11-03T00:04:30Z"}]
  const html=renderRun({...baseRun,procedures:[],stages:[{id:"child",name:"child",uid:"child-uid",procedures:[],conditions}]})
  assert.match(text(html),/StageCleanupComplete.*미완료.*ChildCleanupFailure/)
  assert.match(text(html),/FutureCondition.*미확인.*FutureReason/)
  for(const condition of conditions)assert.ok(html.includes(`dateTime="${condition.lastTransitionTime}"`))
})
