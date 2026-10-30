import assert from "node:assert/strict"
import test from "node:test"
import { explain } from "./labels.ts"

test("energy policy mismatch explains trial counts and requests a new measurement name", () => {
  assert.deepEqual(explain("EnergyObjectivePolicyMismatch"), [
    "배치별 성공한 측정 회차 수가 에너지 정책과 달라 측정을 끝냈어요.",
    "새 이름의 측정으로 다시 측정해 주세요.",
  ])
})

test("unavailable measurement and scheduler GET services do not imply missing data or unsupported features", () => {
  assert.deepEqual(explain("MeasurementUnavailable"), ["지금은 정보를 확인할 수 없어요.", "측정 조회 서비스 연결을 확인해 주세요."])
  assert.deepEqual(explain("SchedulerUnavailable"), ["지금은 정보를 확인할 수 없어요.", "스케줄러 조회 서비스 연결을 확인해 주세요."])
})
