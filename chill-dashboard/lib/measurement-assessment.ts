import type { MeasurementAssessment } from "./read-types.ts"

export function measurementSchedulerUsed(assessment?:MeasurementAssessment|null) {
  return assessment?.scope?.schedulerUsed??null
}

export function measurementQuality(assessments:(MeasurementAssessment|null|undefined)[]) {
  const failed=assessments.some(assessment=>assessment?.checklist?.checks?.some(check=>check.result==="fail"))
  const passed=assessments.length>0&&assessments.every(assessment=>assessment?.checklist?.criteriaMet===true&&assessment.checklist.checks.length>0&&assessment.checklist.checks.every(check=>check.result==="pass"))
  const checks=assessments.flatMap(assessment=>assessment?.checklist?.checks??[])
  const noEvidence=assessments.length>0&&assessments.every(assessment=>assessment?.evidence?.available===false)&&checks.length===0
  const policyUnset=checks.some(check=>check.result==="unknown"&&check.reason==="PolicyUnset")
  return {label:failed?"기준 미충족":passed?"기준 충족":noEvidence?"측정 근거 없음":policyUnset?"정책 미설정":"판정 불가",state:failed?"fail":passed?"pass":"unknown"}
}
