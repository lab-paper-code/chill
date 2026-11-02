import assert from "node:assert/strict"
import test from "node:test"
import { restoreMeasurementSummary, parseMeasurementJSON } from "./measurement-summary.ts"

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
