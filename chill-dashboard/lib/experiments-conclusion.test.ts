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
const modules:Record<string,unknown>={"@/lib/client":{},"@/components/ui/button":{},"@/lib/experiments":experiments,"@/lib/measurements":measurements,"lucide-react":{TriangleAlert:()=>null}}
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

test("result rows separate missing comparisons from absolute values and preserve actual comparison query errors",()=>{
  const legacy={...run,comparisons:undefined}
  const html=renderToStaticMarkup(createElement(components.ExperimentResultRow,{run:legacy,runs:[legacy],pending:false,error:false}))
  assert.match(html,/비교・정책・같은 부하 미기록/)
  assert.match(html,/aria-label="단계별 원값"/)
  assert.doesNotMatch(html,/에너지 Δ|최악 성공률 Δ/)
  assert.doesNotMatch(html,/>0<\/td>/)
  assert.match(html,/href="\/experiments\/results\?a=comparison"/)
  const failed=renderToStaticMarkup(createElement(components.ExperimentResultRow,{run,runs:[run],pending:false,error:true}))
  assert.match(failed,/<tr[^>]*><th[^>]*>조회 실패<\/th>/)
  assert.doesNotMatch(failed,/대상 없음<\/th>/)
})

test("R17b: preparation-failure publications retain execution, measurement and cleanup without a decodable definition",()=>{
  const failed=experiments.decodeExperiment(JSON.stringify({...run,stages:[],comparisons:undefined,definition:{schema:"evaluation-definition.v1alpha1",run:{name:"failed",uid:"failed-run"},spec:{stages:[{id:"never-created"}]},loadScenarios:[],workloads:[]},outcome:{execution:{status:"Failed",reason:"IdentityMismatch"},measurement:{verdict:"NOT_EVALUATED",reason:"IdentityMismatch"},cleanup:{status:"Complete",reason:"NoControlEffects"}}}))
  assert.equal(failed.definition,undefined)
  const html=renderToStaticMarkup(createElement(components.ExperimentResultRow,{run:failed,runs:[failed],pending:false,error:false}))
  for(const value of ["실행","측정","정리","실패","미평가","완료"])assert.ok(html.includes(value),value)
  assert.match(html,/실험 판정 3축/)
})

test("R19: list facts are collapsed and unnamed publications retain their real ID",()=>{
  const legacy={...run,id:"control-222-223",comparisons:undefined,facts:{question:"Frozen question",stages:[{id:"candidate",mode:"autonomous",loadScenarioRef:{uid:"load-source"}}]}}
  const html=renderToStaticMarkup(createElement(components.ExperimentResultRow,{run:legacy,runs:[legacy],pending:false,error:false}))
  assert.match(html,/>control-222-223<\/a>/)
  assert.match(html,/<details[^>]*><summary[^>]*>질문·출처<\/summary>/)
  assert.doesNotMatch(html,/<details[^>]*open[^>]*><summary[^>]*>질문·출처/)
  assert.doesNotMatch(html,/aria-label="측정 규칙"|aria-label="계획 부하"/)
  assert.match(html,/자동 운영/)
})

test("R19: frozen workload definitions remain visible without inventing KPI identity",()=>{
  const publication={...run,stages:[{...stage("candidate"),kpis:{requests:{camera:{workloadUID:null,definitionID:null,completedLatencyNS:{p95:2000000000}}}}}],definition:{workloads:[{name:"camera",uid:"frozen-camera",generation:1,slo:{percentile:95,targetLatencyNanos:1000000000}}],spec:{stages:[]},loadScenarios:[]}} as unknown as Experiment
  const html=renderToStaticMarkup(createElement(components.ExperimentAbsoluteValues,{run:publication}))
  assert.match(html,/정의 출처 SLO/)
  assert.match(html,/frozen-camera/)
  assert.match(html,/1,000/)
  assert.match(html,/적용 SLO 미확인/)
  assert.ok(html.indexOf("기한 내 성공률")<html.indexOf('aria-label="측정 규칙"'))
  assert.equal(experiments.frozenP95SLO(experiments.experimentRows(publication.stages[0])[0],publication),null)
})

test("R19: projected list load references are not mislabeled as missing publication data",()=>{
  const publication={...run,facts:{stages:[{id:"candidate",loadScenarioRef:{uid:"known-load"}}]}}
  const html=renderToStaticMarkup(createElement(components.ExperimentMetadata,{run:publication,stage:publication.stages[0]}))
  assert.match(html,/목록 미제공/)
  assert.doesNotMatch(html,/<td colSpan="5">미기록<\/td>/)
})
