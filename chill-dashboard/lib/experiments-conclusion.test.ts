import * as comparisonHelpers from "./experiment-comparison.ts"
import * as routeHelpers from "./dashboard-routes.ts"
import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement, type ComponentType } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as experiments from "./experiments.ts"
import * as measurements from "./measurements.ts"
import type { Experiment, ExperimentStage } from "./experiments.ts"

const require=createRequire(import.meta.url)
const compiled=transpileModule(readFileSync(new URL("../features/experiments-conclusion.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
const components:Record<string,ComponentType<{run:Experiment;runs?:Experiment[];pending?:boolean;error?:boolean;stage?:ExperimentStage}>>={}
const modules:Record<string,unknown>={"@/lib/client":{},"@/components/ui/button":{},"@/lib/experiment-comparison":comparisonHelpers,"@/lib/dashboard-routes":routeHelpers,"@/lib/experiments":experiments,"@/lib/measurements":measurements,"lucide-react":{TriangleAlert:()=>null}}
new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),components)
const stage=(id:string)=>({id,from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:01:00Z",kpis:{}})
const run:Experiment={schemaVersion:"serving-experiment.v1alpha1",id:"comparison",from:"2026-10-30T00:00:00Z",to:"2026-10-30T00:01:00Z",status:"FAIL",sources:[],stages:[stage("candidate"),stage("baseline"),stage("unpaired")],comparisons:[{left:{stageID:"candidate"},right:{stageID:"baseline"},reason:"조건 확인"}]}

test("comparison limits stay in a body table rather than an unbounded tooltip",()=>{
  const html=renderToStaticMarkup(createElement(components.ExperimentLimits,{run}))
  assert.doesNotMatch(html,/role="tooltip"|\bw-max\b/)
  assert.match(html,/<table[^>]+aria-label="비교 제한 원인"/)
  assert.match(html,/<th[^>]+scope="row"[^>]*>규칙 미기록<\/th><td[^>]*>1<\/td>/)
  assert.match(html,/<th[^>]+scope="row"[^>]*>대응 없음<\/th><td[^>]*>1<\/td>/)
})

test("comparison coverage counts are visible table cells, including observed zero",()=>{
  const html=renderToStaticMarkup(createElement(components.ExperimentCoverage,{run}))
  assert.match(html,/<table[^>]+aria-label="비교 구간 수"/)
  for(const [label,count] of [["일치",0],["참고",1],["대응 없음",1]])assert.match(html,new RegExp(`<th[^>]+scope="row"[^>]*>${label}</th><td[^>]*>${count}</td>`))
  assert.doesNotMatch(html,/title=/)
})

test("legacy publications do not invent comparison counts",()=>{
  const legacy={...run,comparisons:undefined}
  for(const name of ["ExperimentLimits","ExperimentCoverage"])assert.equal(renderToStaticMarkup(createElement(components[name],{run:legacy})),"")
})

test("IA: publication rows are one schema, without nested stage or service tables",()=>{
  for(const publication of [run,{...run,comparisons:undefined},{...run,comparisons:[]}]) {
    const html=renderToStaticMarkup(createElement(components.ExperimentResultRow,{run:publication,runs:[publication],pending:false,error:false}))
    assert.equal((html.match(/<tr\b/g)??[]).length,1)
    assert.doesNotMatch(html,/<table|<details|단계별 원값|에너지 Δ|최악成功率|SLO 개선|SLO 악화/)
    assert.match(html,/href="\/experiments\/results\?a=comparison/)
  }
  const failed=renderToStaticMarkup(createElement(components.ExperimentResultRow,{run,runs:[run],pending:false,error:true}))
  assert.match(failed,/조회 실패/)
  assert.doesNotMatch(failed,/대상 없음/)
})

test("R17b: preparation-failure publications retain execution, measurement and cleanup without a decodable definition",()=>{
  const failed=experiments.decodeExperiment(JSON.stringify({...run,stages:[],comparisons:undefined,definition:{schema:"evaluation-definition.v1alpha1",run:{name:"failed",uid:"failed-run"},spec:{stages:[{id:"never-created"}]},loadScenarios:[],workloads:[]},outcome:{execution:{status:"Failed",reason:"IdentityMismatch"},measurement:{verdict:"NOT_EVALUATED",reason:"IdentityMismatch"},cleanup:{status:"Complete",reason:"NoControlEffects"}}}))
  assert.equal(failed.definition,undefined)
  const html=renderToStaticMarkup(createElement(components.ExperimentResultRow,{run:failed,runs:[failed],pending:false,error:false}))
  for(const value of ["실행","측정","정리","실패","미평가","완료"])assert.ok(html.includes(value),value)
  assert.equal((html.match(/<tr\b/g)??[]).length,1)
})

test("IA: list uses recorded policies but leaves UID and source evidence in detail",()=>{
  const legacy={...run,id:"control-222-223",comparisons:undefined,facts:{question:"Frozen question",stages:[{id:"candidate",settingsPatchKeys:["maxReplicas","future.setting"],loadScenarioRef:{uid:"load-source"}}]}}
  const html=renderToStaticMarkup(createElement(components.ExperimentResultRow,{run:legacy,runs:[legacy],pending:false,error:false}))
  assert.match(html,/>control-222-223<\/a>/)
  assert.doesNotMatch(html,/<details|Frozen question|load-source|aria-label="측정 규칙"|aria-label="계획 부하"/)
  assert.match(html,/복제 수 상한/)
  assert.match(html,/future\.setting/)
})

test("R19: frozen workload definitions remain visible without inventing KPI identity",()=>{
  const publication={...run,stages:[{...stage("candidate"),kpis:{requests:{camera:{workloadUID:null,definitionID:null,completedLatencyNS:{p95:2000000000}}}}}],definition:{workloads:[{name:"camera",uid:"frozen-camera",generation:1,slo:{percentile:95,targetLatencyNanos:1000000000}}],spec:{stages:[]},loadScenarios:[]}} as unknown as Experiment
  const html=renderToStaticMarkup(createElement(components.ExperimentMetadata,{run:publication,stage:publication.stages[0]}))
  assert.match(html,/정의 출처 SLO/)
  assert.match(html,/frozen-camera/)
  assert.match(html,/1,000/)
  assert.equal(comparisonHelpers.frozenP95SLO(comparisonHelpers.experimentRows(publication.stages[0])[0],publication),null)
})

test("R19: projected list load references are not mislabeled as missing publication data",()=>{
  const publication={...run,facts:{stages:[{id:"candidate",settingsPatchKeys:null,loadScenarioRef:{uid:"known-load"}}]}}
  const html=renderToStaticMarkup(createElement(components.ExperimentMetadata,{run:publication,stage:publication.stages[0]}))
  assert.match(html,/목록 미제공/)
  assert.doesNotMatch(html,/<td colSpan="5">미기록<\/td>/)
})

test("R19 review: definition sources retain p90 and p99 without applying them as p95",()=>{
  const publication={...run,stages:[{...stage("candidate"),kpis:{requests:{camera:{workloadUID:"camera",definitionID:"definition"}}}}],definition:{workloads:[{name:"camera",uid:"camera",generation:1,slo:{percentile:90,targetLatencyNanos:1000000000}},{name:"other",uid:"other",generation:1,slo:{percentile:99,targetLatencyNanos:7034000000}}],spec:{stages:[]},loadScenarios:[]}} as unknown as Experiment
  const html=renderToStaticMarkup(createElement(components.ExperimentMetadata,{run:publication,stage:publication.stages[0]}))
  const source=html.match(/<table aria-label="정의 출처 SLO"[\s\S]*?<\/table>/)?.[0]??""
  assert.match(source,/>분위수<\/th>/)
  assert.match(source,/>목표 지연 \(ms\)<\/th>/)
  assert.match(source,/>90<\/td><td[^>]*>1,000<\/td>/)
  assert.match(source,/>99<\/td><td[^>]*>7,034<\/td>/)
  assert.doesNotMatch(source,/미기록|SLO p95/)
  assert.equal(comparisonHelpers.frozenP95SLO(comparisonHelpers.experimentRows(publication.stages[0])[0],publication),null)
})

test("control323: selected stage metadata exposes recorded setting changes, empty and absent facts",()=>{
  for(const [keys,expected] of [[["maxReplicas","controls.routing","minimumDemand.camera"],"복제 수 상한, 요청 분배, 최소 수요 · camera"],[[],"변경 없음"],[null,"미기록"],[undefined,"미기록"]] as const) {
    const publication={...run,facts:{stages:[{id:"candidate",settingsPatchKeys:keys===undefined?undefined:keys===null?null:[...keys]}]}}
    const html=renderToStaticMarkup(createElement(components.ExperimentMetadata,{run:publication,stage:publication.stages[0]}))
    const changes=html.match(/<table aria-label="단계 변경 설정"[\s\S]*?<\/table>/)?.[0]??""
    assert.ok(changes.includes(">변경 설정</th>")&&changes.includes(`>${expected}</td>`),html)
  }
})

test("control323: published facts follow real pages by ID and preserve absent facts and page failures",async()=>{
  for(const outcome of ["keys","null","missing","denied","stalled"]) {
    const paths:string[]=[],signal=new AbortController().signal
    let queryFn:({signal}:{signal:AbortSignal})=>Promise<Experiment["facts"]|null>=async()=>null
    const error=new Error("denied")
    const dependencies={...modules,
      "@tanstack/react-query":{useQuery:(options:{queryFn:typeof queryFn})=>{queryFn=options.queryFn;return {}}},
      "@/lib/client":{useSession:()=>({token:"fixture"}),decodeResponse:(body:string,decoder:(body:string)=>unknown)=>decoder(body),relay:async(path:string,_token:string,actual:AbortSignal)=>{
        paths.push(path);assert.equal(actual,signal)
        const offset=Number(new URL(path,"https://local.invalid").searchParams.get("offset"))
        if(offset&&outcome==="denied")throw error
        const items=offset?[{...run,id:outcome==="missing"?"other":run.id,facts:{stages:[{id:"candidate",settingsPatchKeys:outcome==="null"?null:[]}]}}]:Array.from({length:5},(_,index)=>({...run,id:`other-${index}`}))
        return {body:JSON.stringify({items,page:{offset,limit:5,total:6,...(!offset?{nextOffset:5}:outcome==="stalled"?{nextOffset:5}:{})}})}
      }},
    }
    const exports={} as {usePublishedExperimentFacts:(id:string)=>unknown}
    new Function("require","exports",compiled)((name:string)=>dependencies[name as keyof typeof dependencies]??require(name),exports)
    exports.usePublishedExperimentFacts(run.id)
    if(outcome==="denied")await assert.rejects(queryFn({signal}),error)
    else if(outcome==="stalled")await assert.rejects(queryFn({signal}),/페이지/)
    else {
      const facts=await queryFn({signal})
      if(outcome==="missing")assert.equal(facts,null)
      else assert.deepEqual(facts?.stages?.[0].settingsPatchKeys,outcome==="null"?null:[])
    }
    assert.equal(paths.length,2)
    for(const path of paths)assert.equal(new URL(path,"https://local.invalid").searchParams.get("limit"),"5")
  }
})

test("published operator results expose service failures, declared SLO, outcomes, parts and energy without rejudging",()=>{
  const stage={id:"measured",from:run.from,to:run.to,kpis:{
    status:"FAIL",wholePoolJoules:null,
    components:{slo:{status:"FAIL",workloads:{camera:"FAIL",sensor:"OBSERVED_THRESHOLD_MET"}},loadFidelity:{camera:{status:"PARTIAL",issuanceFraction:.8,issued:80,unissued:20,generatorQuality:{exceeding:2,denominator:80,thresholdLabel:"WITHIN_REPORTED_THRESHOLD",policy:{latenessToleranceNS:50000000}}},sensor:{status:"OBSERVED",issuanceFraction:1,unissued:0}},drain:{complete:false,reasons:["ProducerEOFWithoutCompleteDrain"]}},
    requests:{camera:{workloadUID:"camera",definitionID:"camera/1/model/1/preprocessor/1",identitySource:{store:"evidence",key:"unlinked-snapshot"},offered:100,deadlineSuccess:15,deadlineSuccessFraction:.15,D1:"OBSERVED_THRESHOLD_MET",outcomes:{completed:40,timeout:10,error:29,"unfinished-or-no-final":1},completedLatencyNS:{p50:1200000000,p95:1950000000},substages:[{id:"low",offered:40,deadlineSuccess:12},{id:"high",offered:60,deadlineSuccess:3}]},sensor:{workloadUID:"sensor",offered:10,deadlineSuccess:10,D1:"OBSERVED_THRESHOLD_MET",outcomes:{completed:10},substages:[{id:"low",offered:4,deadlineSuccess:4},{id:"high",offered:6,deadlineSuccess:6}]}},
    power:{energyStatus:"UNAVAILABLE",nodes:[{node:{name:"worker-a"}}],energyCoverage:{maximumGapSeconds:9.121},reasons:["Declared Node worker-a: partial/unavailable source or coverage below reporting policy"]},
  }}
  const publication={...run,comparisons:[],stages:[stage],outcome:{measurement:{verdict:"FAIL"}},definition:{workloads:[{name:"camera",uid:"camera",generation:1,model:{uid:"model",generation:1},preprocessor:{uid:"preprocessor",generation:1},slo:{percentile:95,targetLatencyNanos:1000000000}},{name:"sensor",uid:"sensor",slo:{percentile:99,targetLatencyNanos:36000000}}],spec:{stages:[]},loadScenarios:[]}} as unknown as Experiment
  const verdict=renderToStaticMarkup(createElement(components.ExperimentVerdict,{run:publication}))
  assert.match(verdict,/FAIL.*SLO 미달: camera/)
  assert.doesNotMatch(verdict,/SLO 미달: camera, sensor/)
  const html=renderToStaticMarkup(createElement(components.ExperimentStageResults,{run:publication,stage}))
  const matrix=html.match(/<table[^>]+aria-label="서비스별 결과"[\s\S]*?<\/table>/)?.[0]??""
  assert.equal((matrix.match(/<tbody>[\s\S]*?<\/tbody>/)?.[0].match(/<tr\b/g)??[]).length,2)
  for(const value of ["선언 SLO","p95","1,000","p99","36","적용 미확인","15 / 100","15.00%","1,950","1,200","완료 40","시간 초과 10","오류 29","미완료 1","미발행 20","80.00%","부분 발행"])assert.ok(matrix.includes(value),value)
  const sensor=matrix.match(/<tr[^>]*>[\s\S]*?sensor[\s\S]*?<\/tr>/g)?.at(-1)??""
  assert.ok(sensor.includes("시간 초과 미기록"))
  assert.ok(sensor.includes("오류 미기록"))
  assert.ok(sensor.includes("미완료 미기록"))
  const parts=html.match(/<table[^>]+aria-label="구간별 성공률"[\s\S]*?<\/table>/)?.[0]??""
  for(const value of ["low","high","12 / 40","3 / 60","30.00%","5.00%"])assert.ok(parts.includes(value),value)
  for(const value of ["UNAVAILABLE","1/1","최대 공백 9.1 s","배출 미완료","생성기 종료 후 요청 배출 미완료"])assert.ok(html.includes(value),value)
  assert.doesNotMatch(html,/0 J/)
  assert.ok(html.indexOf('aria-label="서비스별 결과"')<html.indexOf('aria-label="구간별 성공률"'))
  assert.ok(html.indexOf('aria-label="구간별 성공률"')<html.indexOf('aria-label="에너지·배출"'))
})

test("missing publication outcomes and zero offered remain unrecorded, never a passing zero",()=>{
  const stage={id:"empty",from:run.from,to:run.to,kpis:{requests:{camera:{offered:0,deadlineSuccess:0}}}}
  const html=renderToStaticMarkup(createElement(components.ExperimentStageResults,{run:{...run,stages:[stage]},stage}))
  assert.match(html,/0 \/ 0/)
  assert.doesNotMatch(html,/0\.00%|통과|완료 0|오류 0|미완료 0|미발행 0/)
  assert.match(html,/완료 미기록/)
})

test("review: declared and identity-linked applied SLO retain sub-ms precision",()=>{
  const source={store:"archive",key:"snapshot"}
  const measured={...stage("candidate"),kpis:{requests:{camera:{workloadUID:"camera",definitionID:"camera/1/model/1/preprocessor/1",identitySource:source}}}}
  const publication={...run,stages:[measured],sources:[source],definition:{workloads:[{name:"camera",uid:"camera",generation:1,model:{uid:"model",generation:1},preprocessor:{uid:"preprocessor",generation:1},slo:{percentile:95,targetLatencyNanos:400000}}],spec:{stages:[]},loadScenarios:[]}} as unknown as Experiment
  const html=renderToStaticMarkup(createElement(components.ExperimentStageResults,{run:publication,stage:measured}))
  assert.match(html,/p95 · 0\.40 ms/)
  assert.match(html,/적용 확인: p95 · 0\.40 ms/)
  assert.doesNotMatch(html,/p95 · 0 ms/)
})

test("pool mean power uses published joules and measured interval, not planned duration",()=>{
  const measured={...stage("candidate"),kpis:{wholePoolJoules:6000,power:{energyStatus:"MEASURED",wholePoolJoules:6000}}}
  const publication={...run,comparisons:[],stages:[measured],definition:{workloads:[],spec:{stages:[{id:"candidate",loadScenarioRef:{uid:"planned"}}]},loadScenarios:[{uid:"planned",spec:{parts:[{id:"planned",durationSeconds:300,rates:{camera:1}}]}}]}} as unknown as Experiment
  const html=renderToStaticMarkup(createElement(components.ExperimentStageResults,{run:publication,stage:measured}))
  assert.match(html,/풀 평균 전력 100(?:\.0)? W/)
  assert.doesNotMatch(html,/풀 평균 전력 20(?:\.0)? W/)
})

test("pool mean power preserves observed zero and missing energy operands",()=>{
  for(const joules of [0,null,undefined])for(const nested of [false,true]) {
    const energy=joules===undefined?{}:{wholePoolJoules:joules}
    const measured={...stage("candidate"),kpis:{...(!nested?energy:{}),power:{energyStatus:"MEASURED",...(nested?energy:{})}}}
    const html=renderToStaticMarkup(createElement(components.ExperimentStageResults,{run:{...run,stages:[measured]},stage:measured}))
    assert.match(html,joules===0?/풀 평균 전력 0(?:\.0)? W/:/풀 평균 전력 미기록/)
  }
})

test("pool mean power never substitutes a plan or zero for missing or invalid measured timestamps",()=>{
  for(const interval of [{from:undefined},{to:undefined},{from:null},{to:null},{from:"invalid"},{to:"invalid"},{to:run.from},{from:run.to,to:run.from}]) {
    const measured={...stage("candidate"),...interval,kpis:{wholePoolJoules:6000,power:{energyStatus:"MEASURED",wholePoolJoules:6000}}} as unknown as ExperimentStage
    const html=renderToStaticMarkup(createElement(components.ExperimentStageResults,{run:{...run,stages:[measured]},stage:measured}))
    assert.match(html,/풀 평균 전력 미기록/)
    assert.doesNotMatch(html,/풀 평균 전력 (?:0|100)(?:\.0)? W/)
  }
})

test("final and provisional SLOs preserve fractional percentiles and large wire nanoseconds",()=>{
  const Component=components.ExperimentStageResults as ComponentType<Record<string,unknown>>
  for(const targetLatencyNanos of [400000,9007199254740993n,undefined]) {
    const slo={percentile:99.5,targetLatencyNanos}
    const measured={...stage("fractional"),kpis:{requests:{camera:{}}}}
    const publication={...run,stages:[measured],definition:{workloads:[{name:"camera",slo}],spec:{stages:[]},loadScenarios:[]}}
    for(const props of [{run:publication,stage:measured},{provisional:{workloads:{camera:{slo}}}}]) {
      const html=renderToStaticMarkup(createElement(Component,props))
      assert.match(html,/p99\.5 · /)
      assert.doesNotMatch(html,/p100 · /)
      assert.ok(html.includes(targetLatencyNanos===undefined?"p99.5 · 미기록 ms":`p99.5 · ${measurements.formatMeasurementDuration(targetLatencyNanos)}`))
    }
  }
})
