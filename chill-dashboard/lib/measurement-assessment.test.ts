import assert from "node:assert/strict"
import test from "node:test"
import { measurementUsage, measurementEvidenceChecks, assessmentReasons } from "./measurement-assessment.ts"
import type { MeasurementAssessment, ResultRow } from "./read-types.ts"

const assessment:MeasurementAssessment={status:"criteria_met",displayStatus:"usable",label:"사용 가능",reasons:[],execution:null,evidence:{available:true,accepted:true,reasons:[]},scope:{schedulerUsed:false,reused:false,facts:[]},checklist:{criteriaMet:true,version:"result-checklist.v1",checks:[{code:"FrozenPowerWindowPolicy",result:"unknown",message:"unknown",sourceIds:[]}]}}
test("scheduler use is an independent fact, never inferred from an accepted result",()=>{
  assert.equal(measurementUsage(assessment),"스케줄러 미사용")
  assert.equal(measurementUsage({...assessment,scope:{...assessment.scope,schedulerUsed:true}}),"스케줄러 사용 중")
  assert.equal(measurementUsage({...assessment,scope:{...assessment.scope,schedulerUsed:null}}),"스케줄러 사용 여부 미확인")
})
test("coverage and detail evidence name uncertainty, whole-node power and dataset without granting approval",()=>{
  const row:ResultRow={id:"row",rowKind:"direct",profileId:"profile",sourceProfileId:null,batch:1,condition:{activeProtocol:{dataset:{name:"images"}}},confidence:{level:.95,lower:.8,upper:1.2,halfWidth:.2},incrementalJoulesPerItem:1,powerSource:{kind:"wall"},p95Milliseconds:10,latencySampleCount:100,idleWatts:20,activeWatts:30,totalJoulesPerItem:2,activeTrialCount:3,idleTrialCount:3,completedItems:100,completedCalls:100,assessment,sources:[],missingReasons:[],reuse:null}
  const notes=measurementEvidenceChecks(assessment,[row])
  assert.equal(notes.length,4)
  assert.deepEqual(notes[0],{label:"오차 반폭",value:20,unit:"%"})
  assert.equal(notes[1].label,"노드 전체 전력")
  assert.equal(notes[2].value,"images")
  assert.deepEqual(measurementEvidenceChecks({...assessment,evidence:{...assessment.evidence,available:false},checklist:{...assessment.checklist,checks:[]}}),[])
})
test("projection reasons remain available even when no checklist was provided and duplicate summaries collapse",()=>{
  const reasons=[{code:"NativeExecutionError",message:"NativeExecutionError; UnknownRequestExecution",sourceIds:[]},{code:"NativeExecutionError",message:"NativeExecutionError; UnknownRequestExecution",sourceIds:[]}]
  assert.equal(assessmentReasons({...assessment,reasons,checklist:{...assessment.checklist,checks:[]}}).length,1)
  assert.match(assessmentReasons({...assessment,reasons})[0],/[가-힣]/)
})
test("R11: incomplete power sources preserve failed and unknown source checks",()=>{
  for(const result of ["fail","unknown"] as const) {
    const checks=measurementEvidenceChecks({...assessment,checklist:{...assessment.checklist,checks:[{code:"PowerSource",result,message:"source",sourceIds:[]}]}})
    assert.ok(checks.some(check=>check.value===(result==="fail"?"불일치":"미확인")))
  }
})
