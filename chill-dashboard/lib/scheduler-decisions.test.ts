import assert from "node:assert/strict"
import { test } from "node:test"
import { compareRows, decisionControlPath, decisionRecordPath, decisionResult, latencyRows, tickPhases, type ControlObservation } from "./scheduler-decisions.ts"
import type { SchedulerCandidate, SchedulerDecision } from "./read-types.ts"

const plan = (id: string, keep: boolean, joules: number | null, expected: number | null): SchedulerCandidate =>
  ({ id, keep, actions: null, status: "Feasible", reasons: null, expectedJoules: joules, worstExceedance: 0, slo: [{ uid: "u1", definition: "d", samples: 1, percentile: 95, expectedNanos: expected, targetNanos: 50_000_000, normalizedExceedance: 0, predictedOutcomes: null }] })
const decision = (at: string, target: SchedulerCandidate | null, keepJ = 100): SchedulerDecision =>
  ({ at, status: "Feasible", keep: plan("keep", true, keepJ, 40_000_000), target, reasons: null, workloads: null, references: null } as unknown as SchedulerDecision)

test("record and control paths select the exact decision instant", () => {
  const record = new URLSearchParams(decisionRecordPath("2026-11-13T15:06:11.467Z")!.split("?")[1])
  assert.equal(record.get("from"), "2026-11-13T15:06:11.467000000Z")
  assert.equal(record.get("to"), "2026-11-13T15:06:11.467000001Z")
  assert.equal(decisionRecordPath("bad"), null)
  const control = new URLSearchParams(decisionControlPath("2026-11-13T15:06:11Z", 10_000_000_000n)!.split("?")[1])
  assert.equal(control.get("filter"), "other")
  assert.equal(control.get("to"), "2026-11-13T15:06:22.000Z")
})

test("result reads the target-keep marker before the status", () => {
  assert.equal(decisionResult("Feasible", true), "Keep")
  assert.equal(decisionResult("Keep", false), "Feasible")
  assert.equal(decisionResult("Overload", undefined), "Overload")
})

test("latency and comparison use the selected plan's server values", () => {
  const changed = decision("2026-11-13T15:00:00Z", plan("c1", false, 80, 45_000_000))
  assert.deepEqual(latencyRows(changed), [{ uid: "u1", percentile: 95, targetMs: 50, keepMs: 40, changeMs: 45 }])
  const kept = decision("2026-11-13T14:00:00Z", plan("keep", true, 90, 40_000_000), 90)
  const rows = compareRows(kept, changed, () => "svc", code => code ?? "")
  assert.deepEqual(rows.find(row => row.key === "status"), { key: "status", label: "결과", a: "Keep", b: "Feasible" })
  assert.deepEqual(rows.find(row => row.key === "changeJ"), { key: "changeJ", label: "변경 예상 에너지", unit: "J", digits: 1, a: null, b: 80 })
  assert.deepEqual(rows.find(row => row.key === "latency:u1"), { key: "latency:u1", label: "svc 예상 지연", unit: "ms", digits: 1, a: 40, b: 45 })
})

test("tick phases keep order and drop zero parts", () => {
  const control = { lockWaitNanos: 653, captureNanos: 200, refreshListNanos: 150, windowNanos: 0, sourceProbeNanos: 0, restNanos: 50, evaluateNanos: 10, publishNanos: 0, recordNanos: 5, launchNanos: 0, evaluation: { preparationNanos: 10, searchNanos: 0, paybackNanos: 0, ratesNanos: 0, projectionNanos: 0 } } as unknown as ControlObservation
  assert.deepEqual(tickPhases(control).map(row => row.key), ["lock", "capture", "capture.refresh", "capture.rest", "evaluate", "evaluate.preparation", "record"])
})
