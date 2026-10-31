import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement, type ComponentType } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as experiments from "./experiments.ts"
import * as measurements from "./measurements.ts"
import type { Experiment } from "./experiments.ts"

const require=createRequire(import.meta.url)
const compiled=transpileModule(readFileSync(new URL("../features/experiments-conclusion.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
const components:Record<string,ComponentType<{run:Experiment}>>={}
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
