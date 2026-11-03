import assert from "node:assert/strict"
import test from "node:test"
import { restoreMeasurementSummary, parseMeasurementJSON } from "./measurement-summary.ts"
import { readFileSync, readdirSync } from "node:fs"
import { join } from "node:path"
import type { MeasurementCoverageCell } from "./read-types.ts"

test("measurement summaries reject exponential DAGs and excessive expanded depth",()=>{
  const envelope=(values:unknown[],body:unknown="~"+(values.length-1))=>({readProjection:{version:"measurement-summary.v1",values,shapes:[]},body})
  const bomb:unknown[]=[0]
  for(let i=0;i<30;i++)bomb.push([`~${i}`,`~${i}`])
  assert.throws(()=>restoreMeasurementSummary(envelope(bomb)),/확장 한도/)
  const deep:unknown[]=[0]
  for(let i=0;i<64;i++)deep.push([`~${i}`])
  assert.throws(()=>restoreMeasurementSummary(envelope(deep)),/확장 한도/)
  let inline:unknown=0
  for(let i=0;i<10000;i++)inline=[inline]
  assert.throws(()=>restoreMeasurementSummary(envelope([],inline)),/확장 한도/)
})

test("expanded node budget includes repeated body references and unused dictionary entries",()=>{
  const values:unknown[]=[Array(1000).fill(0)]
  const readProjection={version:"measurement-summary.v1",values,shapes:[]}
  assert.throws(()=>restoreMeasurementSummary({readProjection,items:Array(2000).fill("~0")}),/확장 한도/)
  for(let i=0;i<30;i++)values.push([`~${i}`,`~${i}`])
  assert.throws(()=>restoreMeasurementSummary({readProjection,items:[]}),/확장 한도/)
})

// Same opt-in captured-GET convention as the Go projection round-trip test.
test("captured operational summaries pass expansion limits",{skip:!process.env.CHILL_MEASUREMENT_SUMMARY_DIR},()=>{
  const dir=process.env.CHILL_MEASUREMENT_SUMMARY_DIR!
  const files=readdirSync(dir).filter(name=>name.endsWith(".json"))
  assert.ok(files.length>=3,"supply coverage, BEO and profiles summary responses")
  for(const file of files) {
    const restored=parseMeasurementJSON(readFileSync(join(dir,file),"utf8")) as Record<string,unknown>
    assert.ok(restored&&typeof restored==="object",file)
    assert.equal("readProjection" in restored,false,file)
  }
})

test("measurement summaries share sources and retain checks, uncertainty, zero, integers and pages",()=>{
  const response={readProjection:{version:"measurement-summary.v1",shapes:[["code","message","result","sourceIds"],["checks","criteriaMet"]],values:["store/evidence/long-source.json",["~0"],{$o:[0,"PowerSource","전력 출처 미확인","unknown","~1"]},{$o:[1,["~2"],null]},"~123"]},assessment:"~3",secondAssessment:"~3",literal:"~4",condition:{cpu:0},completedItems:18446744073709551615n,page:{offset:100,limit:50,total:259,nextOffset:150}}
  const restored=restoreMeasurementSummary(response) as {assessment:{checks:{code:string;message:string;result:string;sourceIds:string[]}[];criteriaMet:null};secondAssessment:unknown;literal:string;condition:{cpu:number};completedItems:bigint;page:unknown}
  assert.deepEqual(restored.assessment,{checks:[{code:"PowerSource",message:"전력 출처 미확인",result:"unknown",sourceIds:["store/evidence/long-source.json"]}],criteriaMet:null})
  assert.equal(restored.secondAssessment,restored.assessment)
  assert.equal(restored.literal,"~123")
  assert.equal(restored.condition.cpu,0)
  assert.equal(restored.completedItems,18446744073709551615n)
  assert.deepEqual(restored.page,response.page)
  assert.equal("readProjection" in restored,false)
})

test("coverage summaries preserve explicit current measurements and null without inferring from history",()=>{
  const current={id:"latest-failed",model:"model",deviceClass:"class",executionPath:"cpu",powerMode:"MAXN",measuredAt:"2026-11-03T01:02:03Z"}
  const response={readProjection:{version:"measurement-summary.v1",shapes:[["id","model","deviceClass","executionPath","powerMode","measuredAt"]],values:[{$o:[0,current.id,current.model,current.deviceClass,current.executionPath,current.powerMode,current.measuredAt]}]},cells:[{currentMeasurement:"~0",assessment:{displayStatus:"problem"},variants:[{profileIds:["older-success"]}]},{currentMeasurement:null,assessment:{displayStatus:"insufficient"},variants:[{profileIds:["other-profile"]}]}]}
  const restored=parseMeasurementJSON(JSON.stringify(response)) as {cells:Pick<MeasurementCoverageCell,"currentMeasurement">[]}
  assert.deepEqual(restored.cells[0].currentMeasurement,current)
  assert.equal(restored.cells[1].currentMeasurement,null)
  assert.deepEqual(parseMeasurementJSON(JSON.stringify({cells:[{currentMeasurement:current},{currentMeasurement:null}]})),{cells:[{currentMeasurement:current},{currentMeasurement:null}]})
})

test("coverage summary diagnostics preserve strings and empty lists without altering verdicts",()=>{
  const diagnostics=["Last runtime entry selected."]
  const response={readProjection:{version:"measurement-summary.v1",shapes:[],values:[diagnostics]},cells:[{support:{verdict:"Compatible",reasons:[],diagnostics:"~0"}},{support:{verdict:"Unknown",reasons:[],diagnostics:[]}}]}
  const restored=parseMeasurementJSON(JSON.stringify(response)) as {cells:{support:{verdict:string;diagnostics:string[]}}[]}
  assert.deepEqual(restored.cells[0].support,{verdict:"Compatible",reasons:[],diagnostics})
  assert.deepEqual(restored.cells[1].support,{verdict:"Unknown",reasons:[],diagnostics:[]})
})

test("invalid and forward references fail rather than inventing evidence",()=>{
  for(const values of [["literal",{source:"~2"}],[{$o:[5,"unknown"]}],[{$o:[0]}]]) {
    assert.throws(()=>restoreMeasurementSummary({readProjection:{version:"measurement-summary.v1",shapes:[["result"]],values},assessment:"~0"}),/미확인/)
  }
  assert.throws(()=>restoreMeasurementSummary({readProjection:{version:"next",values:[],shapes:[]}}),/미지원/)
  const full={assessment:{checklist:{checks:[]}}}
  assert.equal(restoreMeasurementSummary(full),full)
})

test("full precision reads remain unchanged without a summary envelope",()=>{
  assert.deepEqual(parseMeasurementJSON('{"completedItems":18446744073709551615}'),{completedItems:18446744073709551615n})
})

test("path component references restore exact strings and cannot reference joined strings or objects",()=>{
  const readProjection={version:"measurement-summary.v1",shapes:[],values:["","근거","~123",{$s:["~0","~1","~2","~0"]}]}
  const result=restoreMeasurementSummary({readProjection,path:"~3"}) as {path:string}
  assert.equal(result.path,"/근거/~123/")
  for(const parts of [["~4"],["~3"],[0],["literal"]]) {
    assert.throws(()=>restoreMeasurementSummary({readProjection:{...readProjection,values:[...readProjection.values,{$s:parts}]},path:"~4"}),/문자열 참조 미확인/)
  }
  assert.throws(()=>restoreMeasurementSummary({readProjection:{version:"measurement-summary.v1",shapes:[],values:["x".repeat(1024),{$s:Array(8193).fill("~0")}]},path:"~1"}),/확장 한도/)
})
