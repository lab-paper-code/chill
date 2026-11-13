import assert from "node:assert/strict"
import test from "node:test"
import { loadCheckLabel, scenarioProblems, scenarioSeconds, scenarioSteps } from "./experiments-scenario.ts"

const spec = {
  workloads: [{ name: "a", uid: "u" }],
  parts: [{ id: "p1", durationSeconds: 60, rates: { a: 2 } }, { id: "p2", durationSeconds: 30, rates: { a: 0 } }],
  arrivalDistribution: { a: "poisson" }, seeds: { a: "18446744073709551615" }, datasetRefs: { a: { name: "d", uid: "x" } },
}

test("steps hold each rate until the next part", () => {
  assert.equal(scenarioSeconds(spec), 90)
  assert.deepEqual(scenarioSteps(spec), [{ t: 0, s0: 2 }, { t: 60, s0: 0 }, { t: 90, s0: 0 }])
})

test("problems", () => {
  assert.deepEqual(scenarioProblems("n", spec, false), [])
  assert.deepEqual(scenarioProblems("", spec, false), ["이름 필요"])
  assert.deepEqual(scenarioProblems("n", { ...spec, seeds: { a: "18446744073709551616" } }, false), ["난수 씨앗 필요"])
  assert.deepEqual(scenarioProblems("n", { ...spec, parts: [{ id: "p", durationSeconds: 0, rates: {} }] }, false), ["구간 길이 필요", "요청률 필요"])
  assert.equal(loadCheckLabel("workload:a"), "서비스 a")
})
