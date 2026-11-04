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
  const modules={react:clock,"next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams("uid=selected"),useRouter:()=>({replace:()=>{}})},"@tanstack/react-query":{useQueryClient:()=>({})},"@/lib/client":{useChillQuery:query},"@/lib/evaluation-request":request,"@/lib/evaluation-author":author,"@/lib/evaluation-active":active,"@/lib/scheduler-settings":settings,"@/lib/labels":labels,"@/lib/scheduler":scheduler,"@/lib/experiments":experiments,"@/lib/experiment-comparison":comparison,"@/lib/measurements":measurements,"@/components/screen":{Screen:({children}:{children:React.ReactNode})=>React.createElement("main",null,children)},"@/components/read-region":{QueryGate:"div",SourceJSON:"pre"},"@/components/ui/button":{Button:"button"},"@/components/ui/tooltip":{TooltipProvider:"div",Tooltip:"div",TooltipTrigger:"span",TooltipContent:"aside"}}
  Object.assign(modules,{"@/features/experiments-conclusion":load("../features/experiments-conclusion.tsx",{"@/lib/client":{},"@/lib/experiments":experiments,"@/lib/experiment-comparison":comparison,"@/lib/measurements":measurements})})
  Object.assign(modules,{"@/features/evaluation-active":load("../features/evaluation-active.tsx",{"@/lib/evaluation-active":active})})
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

test("planned end replaces zero remaining with elapsed and reports only recorded progress",()=>{
  const entry={name:"arbitrary-run",uid:"selected",stageIndex:1,stagesTotal:1,stage:{id:"trial",goAt:"2026-11-03T00:00:00Z",plannedSeconds:120,injections:[]}}
  const {EvaluationActiveDetails}=load("../features/evaluation-active.tsx",{"@/lib/evaluation-active":active})
  const render=(stageProgress?:unknown)=>text(renderToStaticMarkup(React.createElement(EvaluationActiveDetails as React.ComponentType<{active:unknown;now:number;stageProgress?:unknown}>,{active:{data:{items:[entry]}},now:Date.parse("2026-11-03T00:03:10Z"),stageProgress})))
  assert.match(render(),/계획 시간 경과 · 종료 처리 단계 미기록/)
  assert.match(render(),/계획 종료 후 경과 \(초\)70/)
  assert.doesNotMatch(render(),/계획상 남은 시간|정상|종료 처리 중/)
  const progress={id:"trial",procedures:[{step:"Execute",startedAt:"2026-11-03T00:00:10Z",finishedAt:"2026-11-03T00:02:10Z"}],provisionalD1:{asOf:"2026-11-03T00:02:30Z"}}
  assert.match(render(progress),/진행 신호 없음 40초/)
  assert.match(render(progress),/종료 처리 신호 미수신/)
  assert.match(render({...progress,provisionalD1:{asOf:"2026-11-03T00:01:00Z"}}),/진행 신호 없음 60초/)
  assert.doesNotMatch(render({...progress,id:"another"}),/진행 신호 없음 \d+초/)
})

test("run detail distinguishes recorded post-load procedures and parent progress from absent signals",()=>{
  const entry:active.ActiveEvaluation={name:baseRun.name,uid:baseRun.uid,stageIndex:1,stagesTotal:1,stage:{id:"trial",goAt:baseRun.startedAt,plannedSeconds:120,injections:[]}}
  const stage={id:"trial",procedures:[{step:"stage",startedAt:baseRun.startedAt,finishedAt:null}],finishedAt:null}
  const run={...baseRun,phase:"Running",finishedAt:undefined,currentStage:"trial",currentProcedure:"stage",stages:[stage]}
  let screen=text(renderRun(run,undefined,[scenario],[entry]))
  assert.match(screen,/계획 시간 경과 · 종료 처리 신호 미수신/)
  assert.match(screen,/진행 신호 없음 150초/)
  screen=text(renderRun({...run,stages:[{...stage,procedures:[{...stage.procedures[0],finishedAt:"2026-11-03T00:02:00Z"},{step:"judge",startedAt:"2026-11-03T00:02:10Z",finishedAt:null}]}]},undefined,[scenario],[entry]))
  assert.match(screen,/종료 처리: 측정 판정 중.*시작/)
  assert.match(screen,/진행 신호 없음 20초/)
  assert.doesNotMatch(screen,/종료 처리 신호 미수신/)
  screen=text(renderRun({...run,stages:[{...stage,procedures:[stage.procedures[0],{step:"judge",startedAt:null,finishedAt:"2026-11-03T00:02:15Z"}]}]},undefined,[scenario],[entry]))
  assert.match(screen,/종료 처리: 측정 판정 종료 기록 \(시작 미기록 · 종료 09:02:15\)/)
  assert.match(screen,/진행 신호 없음 15초/)
  assert.doesNotMatch(screen,/종료 처리 신호 미수신/)
  screen=text(renderRun({...run,phase:"Finishing",procedures:[{step:"finish",startedAt:"2026-11-03T00:02:20Z",finishedAt:null}],stages:[{...stage,finishedAt:"2026-11-03T00:02:00Z",procedures:[{...stage.procedures[0],finishedAt:"2026-11-03T00:02:00Z"}]}]},undefined,[scenario],[entry]))
  assert.match(screen,/진행 신호 없음 10초/)
  screen=text(renderRun({...run,stages:[{id:"trial"}]},undefined,[scenario],[entry]))
  assert.match(screen,/계획 시간 경과 · 종료 처리 단계 미기록/)
  assert.doesNotMatch(screen,/종료 처리 신호 미수신/)
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

test("publish remains current after run cleanup ends until a publication is recorded",()=>{
  const run={...baseRun,currentProcedure:"publish",procedures:[
    {step:"prepare",startedAt:"2026-11-03T00:00:00Z",finishedAt:null},
    {step:"publish",startedAt:"2026-11-03T00:02:00Z",finishedAt:null},
  ]}
  const rows=(html:string)=>html.match(/<tr\b[^>]*>[\s\S]*?<\/tr>/g)??[]
  const history=rows(renderRun(run))
  const publish=history.find(row=>text(row).startsWith("게시"))??""
  assert.match(publish,/aria-current="step"/)
  assert.match(publish,/bg-primary\/10/)
  assert.match(text(publish),/현재 절차.*진행 중.*30초/)
  const prepare=history.find(row=>text(row).startsWith("준비"))??""
  assert.match(text(prepare),/종료 미기록.*미기록/)
  assert.doesNotMatch(prepare,/aria-current|진행 중|150초/)
  const published=rows(renderRun({...run,publicationID:"publication"})).find(row=>text(row).startsWith("게시"))??""
  assert.match(text(published),/종료 미기록.*미기록/)
  assert.doesNotMatch(published,/aria-current|현재 절차|진행 중|30초/)
})

test("prepare runs only while the run is unfinished, including preparation failure",()=>{
  const run={...baseRun,phase:"Running",finishedAt:undefined,currentProcedure:"prepare",procedures:[{step:"prepare",startedAt:"2026-11-03T00:02:00Z",finishedAt:null}]}
  const ongoing=(renderRun(run).match(/<tr\b[^>]*>[\s\S]*?<\/tr>/g)??[]).find(row=>text(row).startsWith("준비"))??""
  assert.match(ongoing,/aria-current="step"/)
  assert.match(text(ongoing),/현재 절차.*진행 중.*30초/)
  for(const stopped of [{...run,finishedAt:baseRun.finishedAt,runFailure:{reason:"PreparationFailed"}},{...run,phase:"Done"}]) {
    const row=(renderRun(stopped).match(/<tr\b[^>]*>[\s\S]*?<\/tr>/g)??[]).find(row=>text(row).startsWith("준비"))??""
    assert.match(text(row),/종료 미기록.*미기록/)
    assert.doesNotMatch(row,/aria-current|현재 절차|진행 중|30초/)
  }
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

test("active request projection renders provisional counts, server reachability and generator quality without a verdict",()=>{
  const provisionalD1={asOf:"2026-11-03T00:02:30Z",closedThrough:"2026-11-03T00:02:00Z",provisional:true,workloads:{camera:{offered:20,deadlineSuccess:8,requiredSuccessfulRank:19,outcomes:{completed:9,timeout:2,error:1,"unfinished-or-no-final":8},unfinishedOrUnsent:11,slo:{percentile:95,targetLatencyNanos:125000000},reachable:false,substages:[{id:"gentle",offered:20,deadlineSuccess:8,requiredSuccessfulRank:19}]}},generatorQuality:{camera:{issued:12,unissued:8,issuanceFraction:0.6,status:"PARTIAL",generatorQuality:{status:"OBSERVED",thresholdLabel:"EXCEEDS_REPORTED_THRESHOLD",policy:{latenessToleranceNS:5000000},exceeding:2,denominator:12}}}}
  const run={...baseRun,phase:"Running",finishedAt:undefined,stages:[{id:"trial",finishedAt:null,provisionalD1}]}
  const html=renderRun(run),screen=text(html)
  assert.match(screen,/실행 중 · 잠정/)
  assert.match(screen,/asOf.*집계 마감/)
  assert.match(screen,/성공 \/ 필요.*현재 요청/)
  assert.match(screen,/8 \/ 19.*20/)
  assert.match(screen,/결과 대기·미발행 포함 11/)
  assert.match(screen,/도달 불가/)
  assert.match(screen,/부분 발행/)
  assert.match(screen,/gentle.*8 \/ 19/)
  assert.match(screen,/허용 지연 초과/)
  const table=html.match(/<table[^>]*aria-label="서비스별 결과"[\s\S]*?<\/table>/)?.[0]??""
  assert.match(text(table),/진행 중/)
  assert.doesNotMatch(table,/FAIL|PASS|통과|실패/)
  assert.equal([...table.matchAll(/width:([\d.]+)%/g)].reduce((total,match)=>total+Number(match[1]),0),100,"overlapping pending aggregate must not double-count outcome segments")
  const reachable=structuredClone(run)
  reachable.stages[0].provisionalD1.workloads.camera.reachable=true
  assert.match(text(renderRun(reachable)),/도달 가능/)
})

test("provisional projection is omitted when absent or inactive, and query failure stays explicit",()=>{
  const running={...baseRun,phase:"Running",finishedAt:undefined}
  for(const run of [running,{...running,stages:[{id:"trial",provisionalD1:null}]},{...running,stages:[{id:"other",provisionalD1:{workloads:{camera:{offered:1}}}}]}]) {
    assert.doesNotMatch(renderRun(run),/실행 중 · 잠정|서비스별 결과/)
  }
  const html=renderRun(running,undefined,[scenario],[],{"v1/evaluations/arbitrary-run":{error:{status:500}}})
  assert.match(text(html),/조회 실패/)
  assert.doesNotMatch(html,/서비스별 결과/)
})

test("partial provisional records keep unreported values missing rather than making zero or a verdict",()=>{
  const projection={asOf:"2026-11-03T00:02:30Z",closedThrough:"2026-11-03T00:02:00Z",provisional:true,workloads:{camera:{offered:0,substages:[]}},generatorQuality:{}}
  const run={...baseRun,phase:"Running",finishedAt:undefined,stages:[{id:"trial",provisionalD1:projection}]}
  const html=renderRun(run)
  const table=html.match(/<table[^>]*aria-label="서비스별 결과"[\s\S]*?<\/table>/)?.[0]??""
  assert.match(text(table),/미기록 \/ 미기록/)
  assert.match(text(table),/결과 대기·미발행 포함 미기록/)
  assert.doesNotMatch(table,/도달 가능|도달 불가|width:/)
  assert.doesNotMatch(renderRun({...run,finishedAt:baseRun.finishedAt}),/실행 중 · 잠정/)
  assert.doesNotMatch(renderRun({...run,phase:"Done"}),/실행 중 · 잠정/)
})

const progressEntry:active.ActiveEvaluation={name:baseRun.name,uid:baseRun.uid,stageIndex:1,stagesTotal:1,stage:{id:"trial",goAt:baseRun.startedAt,plannedSeconds:300,injections:[]}}
const progressEvents=[
  {phase:"loadStarted",at:"2026-11-03T00:00:10Z"},
  {phase:"loadEnded",at:"2026-11-03T00:01:00Z"},
  {phase:"drainStarted",at:"2026-11-03T00:01:00Z"},
  {phase:"drainEnded",at:"2026-11-03T00:01:20Z",producerRowsMissing:{camera:2,sensor:0}},
  {phase:"flushStarted",at:"2026-11-03T00:01:20Z"},
  {phase:"collectStarted",at:"2026-11-03T00:01:25Z"},
  {phase:"collectEnded",at:"2026-11-03T00:02:00Z",bytes:4096},
  {phase:"judgeStarted",at:"2026-11-03T00:02:05Z"},
  {phase:"judgeEnded",at:"2026-11-03T00:02:10Z"},
]
function renderProgress(stage:Record<string,unknown>) {
  return renderRun({...baseRun,phase:"Running",finishedAt:undefined,stages:[{id:"trial",currentProcedure:"stage",...stage}]},undefined,[scenario],[progressEntry])
}
const activeDetails=(html:string)=>html.match(/<details\b[^>]*class="live-evaluation"[^>]*>[\s\S]*?<\/details>/)?.[0]??""

test("actual run detail follows recorded drain, flush, collection and judgment instead of the GO plan",()=>{
  for(const [count,label] of [[1,"부하 진행 중"],[3,"drain 진행 중"],[4,"flush · 시작 미기록"],[5,"flush 진행 중"],[6,"수집 진행 중"],[7,"판정 · 시작 미기록"],[8,"판정 진행 중"],[9,"판정 종료 기록"]] as const) {
    const html=renderProgress({currentProcedure:count>=8?"judge":"stage",progress:progressEvents.slice(0,count),lastProgressAt:"2026-11-03T00:02:20Z"}),details=activeDetails(html)
    assert.ok(text(details).includes(label),label)
    assert.match(details,/기록된 구간 경계/)
    assert.match(text(details),/부하 시작.*09:00:10/)
    assert.match(text(details),/진행 신호 없음 10초/)
    assert.doesNotMatch(html,/계획상 구간|계획 시간 경과|계획상 남은 시간|계획 종료 후 경과|<progress/)
    if(count>=4) {
      assert.match(text(details),/drain 종료 시 생성기 결과 행 부족.*camera: 2.*sensor: 0/)
      assert.doesNotMatch(details.match(/<tr[^>]*>[^]*?camera: 2[^]*?<\/tr>/)?.[0]??"",/text-destructive|text-warning/)
    }
    if(count>=7)assert.match(text(details),/저장된 원시 자료.*4,096 bytes/)
  }
})

test("recorded progress preserves missing boundaries and observation order without filling from other evidence",()=>{
  const html=renderProgress({currentProcedure:"judge",progress:[
    {phase:"loadEnded",at:"2026-11-03T00:01:00Z"},
    {phase:"collectStarted",at:"2026-11-03T00:00:40Z"},
    {phase:"judgeEnded",at:"2026-11-03T00:02:10Z"},
  ],procedures:[{step:"judge",startedAt:"2026-11-03T00:02:00Z",finishedAt:null}],loadJobs:[{name:"completed-job",uid:"completed",status:"Succeeded",startedAt:baseRun.startedAt,completedAt:"2026-11-03T00:01:10Z"}]})
  const details=activeDetails(html),rows=[...details.matchAll(/<tr\b[^>]*>[\s\S]*?<\/tr>/g)].map(match=>text(match[0]))
  assert.match(text(details),/판정 종료 기록/)
  assert.deepEqual(rows.slice(1).map(row=>row.split("09:")[0]),["부하 종료","수집 시작","판정 종료"])
  assert.doesNotMatch(details,/부하 시작|drain 시작|drain 종료|flush 시작|수집 종료|판정 시작|진행 중/)
  assert.ok(details.indexOf("09:01:00")<details.indexOf("09:00:40"),"observation order is not producer-clock order")
  assert.match(text(activeDetails(renderProgress({progress:[progressEvents[1]]}))),/drain · 시작 미기록/)
  assert.match(text(activeDetails(renderProgress({progress:[]}))),/구간 경계 미기록/)
  assert.doesNotMatch(renderProgress({progress:[]}),/계획상 구간|<progress/)
  const old=text(renderProgress({}))
  assert.match(old,/계획상 남은 시간/)
  assert.match(old,/계획상 구간 peak/)
  assert.doesNotMatch(old,/기록된 구간 경계|부하 Job/)
})

test("last progress uses the newest independent server signal and never classifies silence as stalled",()=>{
  const stage={id:"trial",lastProgressAt:"2026-11-03T00:02:20Z",provisionalD1:{asOf:"2026-11-03T00:02:10Z"},procedures:[{step:"stage",startedAt:baseRun.startedAt,finishedAt:null}]}
  assert.equal(active.evaluationLastProgressAt(stage,[]),Date.parse(stage.lastProgressAt))
  assert.equal(active.evaluationLastProgressAt({...stage,lastProgressAt:"invalid"},[]),Date.parse(stage.provisionalD1.asOf))
  assert.equal(active.evaluationLastProgressAt(stage,[{step:"finish",startedAt:"2026-11-03T00:02:25Z",finishedAt:null}]),Date.parse("2026-11-03T00:02:25Z"))
  assert.equal(active.evaluationLastProgressAt({...stage,lastProgressAt:baseRun.startedAt},[]),Date.parse(stage.provisionalD1.asOf))
  const details=activeDetails(renderProgress({progress:[progressEvents[0]],lastProgressAt:"2026-11-02T00:00:00Z"}))
  assert.match(text(details),/진행 신호 없음 86550초/)
  assert.doesNotMatch(details,/멈춤|정체|정상|실패|text-destructive|text-warning/)
})

test("run detail reads load Jobs including failure reasons, distinguishes empty and omitted projections",()=>{
  const jobs=[
    {name:"generator-active",uid:"job-active",status:"Active",startedAt:"2026-11-03T00:00:00Z"},
    {name:"generator-done",uid:"job-done",status:"Succeeded",startedAt:"2026-11-03T00:00:05Z",completedAt:"2026-11-03T00:01:30Z"},
    {name:"generator-failed",uid:"job-failed",status:"Failed",startedAt:"2026-11-03T00:00:10Z",reason:"BackoffLimitExceeded"},
  ]
  for(const run of [
    {...baseRun,phase:"Running",finishedAt:undefined,stages:[{id:"trial",loadJobs:jobs}]},
    {...baseRun,stages:[{id:"trial",loadJobs:jobs}]},
  ]) {
    const html=renderRun(run),table=html.match(/<table\b[^>]*aria-label="부하 Job"[\s\S]*?<\/table>/)?.[0]??"",screen=text(table)
    assert.match(screen,/이름.*상태.*시작.*완료.*실패 사유/)
    assert.match(screen,/generator-active.*진행 중.*9:00:00.*미기록/)
    assert.match(screen,/generator-done.*완료.*9:00:05.*9:01:30/)
    assert.match(screen,/generator-failed.*실패.*9:00:10.*미기록.*BackoffLimitExceeded/)
    assert.doesNotMatch(screen,/workload|shard|undefined/)
  }
  assert.match(text(renderProgress({loadJobs:[]})),/부하 Job 없음/)
  assert.doesNotMatch(renderProgress({}),/aria-label="부하 Job"/)
  assert.doesNotMatch(renderProgress({loadJobs:null}),/aria-label="부하 Job"/)
})

test("ended stages and parents leaving the stage retain a start observation without claiming ongoing work",()=>{
  for(const owner of [
    {phase:"Running",currentProcedure:"",stageFinishedAt:"2026-11-03T00:02:20Z"},
    {phase:"Finishing",currentProcedure:"finish",stageFinishedAt:"2026-11-03T00:02:20Z"},
    {phase:"Finishing",currentProcedure:"finish",stageFinishedAt:null},
    {phase:"Running",currentProcedure:"finish",stageFinishedAt:null},
    {phase:"Running",currentProcedure:"publish",stageFinishedAt:null},
    {phase:"Running",currentProcedure:"",currentStage:"next",stageFinishedAt:null},
  ]) {
    const html=renderRun({...baseRun,...owner,finishedAt:undefined,stages:[{id:"trial",currentProcedure:"judge",finishedAt:owner.stageFinishedAt,progress:progressEvents.slice(0,8)}]},undefined,[scenario],[progressEntry])
    const details=activeDetails(html),summary=text(details.match(/<summary>[\s\S]*?<\/summary>/)?.[0]??"")
    assert.match(summary,/마지막 관측: 판정 시작 09:02:05 · 종료 미기록/)
    assert.doesNotMatch(summary,/현재 구간|진행 중/)
    assert.doesNotMatch(details,/판정 종료/)
    assert.match(details,/기록된 구간 경계/)
  }
  assert.match(text(activeDetails(renderProgress({currentProcedure:"judge",finishedAt:null,progress:progressEvents.slice(0,8)}))),/현재 구간: 판정 진행 중/)
})

test("real parent wire omits its procedure during child stage and judge work while child cleanup cannot keep an old interval active",()=>{
  for(const currentProcedure of ["",undefined]) {
    for(const child of [
      {currentProcedure:"stage",progress:progressEvents.slice(0,3),expected:"현재 구간: drain 진행 중"},
      {currentProcedure:"judge",progress:progressEvents.slice(0,8),expected:"현재 구간: 판정 진행 중"},
      {currentProcedure:"judge",progress:progressEvents.slice(0,7),expected:"현재 구간: 판정 · 시작 미기록"},
      {currentProcedure:"judge",progress:progressEvents.slice(0,3),expected:"마지막 관측: drain 시작 09:01:00 · 종료 미기록"},
      {currentProcedure:"cleanup-jobs",progress:progressEvents.slice(0,8),expected:"마지막 관측: 판정 시작 09:02:05 · 종료 미기록"},
    ]) {
      const html=renderRun({...baseRun,phase:"Running",currentProcedure,finishedAt:undefined,stages:[{id:"trial",finishedAt:null,currentProcedure:child.currentProcedure,progress:child.progress}]},undefined,[scenario],[progressEntry])
      const summary=text(activeDetails(html).match(/<summary>[\s\S]*?<\/summary>/)?.[0]??"")
      assert.equal(summary,child.expected)
      assert.doesNotMatch(activeDetails(html),/판정 종료/)
    }
  }
})
