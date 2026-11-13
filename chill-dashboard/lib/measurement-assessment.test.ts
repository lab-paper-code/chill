import assert from "node:assert/strict"
import test from "node:test"
import type { MeasurementAssessment } from "./read-types.ts"
import * as assessmentHelpers from "./measurement-assessment.ts"

const assessment:MeasurementAssessment={status:"criteria_met",displayStatus:"usable",label:"사용 가능",reasons:[],execution:null,evidence:{available:true,accepted:true,reasons:[]},scope:{schedulerUsed:false,reused:false,facts:[]},checklist:{criteriaMet:true,version:"result-checklist.v1",checks:[{code:"FrozenPowerWindowPolicy",result:"unknown",message:"unknown",sourceIds:[]}]}}
test("R18 policy unset is distinct from unknown, failure and absent measurement",()=>{
  const withCheck=(result:"unknown"|"fail",reason?:string)=>({...assessment,checklist:{...assessment.checklist,criteriaMet:false,checks:[{code:"FrozenPowerWindowPolicy",result,reason,message:"power policy",sourceIds:[]}]}})
  assert.equal(assessmentHelpers.measurementQuality([withCheck("unknown","PolicyUnset")]).label,"정책 미설정")
  assert.equal(assessmentHelpers.measurementQuality([withCheck("unknown")]).label,"판정 불가")
  assert.equal(assessmentHelpers.measurementQuality([withCheck("fail","PolicyUnset")]).label,"기준 미충족")
  assert.equal(assessmentHelpers.measurementQuality([{...assessment,evidence:{available:false,accepted:false,reasons:[]},checklist:{...assessment.checklist,checks:[]}}]).label,"측정 근거 없음")
})
