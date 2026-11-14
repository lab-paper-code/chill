import assert from "node:assert/strict"
import test from "node:test"
import { admissionPolicy, codeMessage, evaluationVerdict, experimentVerdict, schedulerReason, serviceReadiness, sessionOutcomeStatus, sessionParameterStatus } from "./status-labels.ts"

test("one verdict word in lists and details", () => {
  assert.equal(experimentVerdict("PASS").label, "목표 충족")
  assert.equal(experimentVerdict("FAIL").label, "목표 미달")
  assert.equal(evaluationVerdict({ status: "True" }).label, "목표 충족")
  assert.equal(evaluationVerdict({ status: "False", reason: "SLOFailed" }).label, "목표 미달")
})

test("unknown codes read as a Korean fallback with the code kept; no code is the empty mark", () => {
  assert.deepEqual(serviceReadiness("Mystery"), { label: "기타", tone: "idle", code: "Mystery" })
  assert.equal(serviceReadiness(undefined).label, "—")
  assert.deepEqual(schedulerReason("Mystery:detail"), { label: "기타 사유", code: "Mystery:detail" })
  assert.equal(admissionPolicy("Reject"), "거부")
  assert.equal(admissionPolicy("Delay"), "지연 허용")
})

test("code messages are terse phrases without internal names", () => {
  assert.equal(codeMessage("EnergyObjectivePolicyMismatch"), "성공 회차 수가 에너지 정책과 다름")
  assert.equal(codeMessage("FutureCode"), undefined)
  for (const code of ["AppliedDefinitionUnavailable", "RestartDrain", "DashboardRelayUnavailable", "GatewayOverloaded"]) {
    assert.doesNotMatch(codeMessage(code)!, /ChillServer|요\.?$|니다|SLO/)
  }
})

test("every session outcome has a Korean word, an unknown one keeps its code", () => {
  const words = { CapacityLimit: "용량 한계에서 종료", NotServable: "서빙 불가", NotMeasured: "측정하지 않음", Failed: "실패", ChillServerRestart: "서버 재시작으로 중단", SessionSettingsMissing: "설정 없음", MeterUndeclared: "전력계 미선언", SessionUnresolved: "준비 정보 해석 실패" }
  for (const [code, label] of Object.entries(words)) assert.equal(sessionOutcomeStatus(code).label, label)
  assert.deepEqual(sessionOutcomeStatus("FutureOutcome"), { label: "기타", tone: "idle", code: "FutureOutcome" })
  assert.equal(sessionParameterStatus("NotIdentified").label, "구분 안 됨")
})
