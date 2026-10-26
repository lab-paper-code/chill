import assert from "node:assert/strict"
import test from "node:test"
import { explain } from "./labels.ts"

test("energy policy mismatch explains trial counts and requests a new measurement name", () => {
  assert.deepEqual(explain("EnergyObjectivePolicyMismatch"), [
    "배치별 성공한 측정 회차 수가 에너지 정책과 달라 측정을 끝냈어요.",
    "새 이름의 측정으로 다시 측정해 주세요.",
  ])
})
