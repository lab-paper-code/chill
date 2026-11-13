import assert from "node:assert/strict"
import { test } from "node:test"
import { compareRows, decisionControlPath, decisionRecordPath, decisionResult, headline, latencyRows, recordFacts, summaryFacts, tickPhases, type ControlObservation } from "./scheduler-decisions.ts"
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

test("result reads the target-keep marker for Feasible and Keep only", () => {
  assert.equal(decisionResult("Feasible", true), "Keep")
  assert.equal(decisionResult("Keep", false), "Feasible")
  assert.equal(decisionResult("Overload", undefined), "Overload")
  assert.equal(decisionResult("Overload", true), "Overload")
  assert.equal(decisionResult("Pending", undefined), "Pending")
})

test("headline from a record: server reasons first, then the keep plan's status or the energy difference", () => {
  const changed = decision("2026-11-13T15:00:00Z", plan("c1", false, 80, 45_000_000))
  const of = (d: SchedulerDecision) => headline(recordFacts(d))
  assert.deepEqual(of(changed), { text: "예상 에너지 20.0 J 절감" })
  assert.deepEqual(of({ ...changed, reasons: ["NoPlanMeetsSLO"] }), { reasons: ["NoPlanMeetsSLO"] })
  assert.deepEqual(of({ ...changed, keep: { ...changed.keep, status: "Overload" } }), { text: "유지안 과부하" })
  assert.deepEqual(of(decision("2026-11-13T15:00:00Z", plan("keep", true, 100, 40_000_000))), { text: "더 나은 후보 없음" })
  const idle = decision("2026-11-13T15:00:00Z", plan("keep", true, 0, null))
  assert.deepEqual(of({ ...idle, workloads: [{ uid: "u1", state: "Disabled", reason: "AutonomousControlOff" }] }), { text: "자동 제어 서비스 없음" })
  assert.equal(of(decision("2026-11-13T15:00:00Z", null)), null)
  assert.deepEqual(of(decision("2026-11-13T15:00:00Z", plan("c2", false, 120, 45_000_000))), { text: "예상 에너지 20.0 J 증가" })
})

test("headline from a summary row matches the record for the same decision", () => {
  const row = { at: "2026-11-13T15:00:00Z", status: "Feasible" }
  const of = (d: Parameters<typeof summaryFacts>[0]) => headline(summaryFacts(d))
  assert.deepEqual(of({ ...row, reasons: ["NoPlanMeetsSLO"] }), { reasons: ["NoPlanMeetsSLO"] })
  assert.deepEqual(of({ ...row, targetKeep: true, keepExpectedJoules: 100, targetExpectedJoules: 100 }), { text: "더 나은 후보 없음" })
  assert.deepEqual(of({ ...row, targetKeep: true, workloads: [{ uid: "u1", state: "Disabled", reason: "AutonomousControlOff" }] }), { text: "자동 제어 서비스 없음" })
  // The same change as the record test above: 100 J kept against 80 J.
  assert.deepEqual(of({ ...row, targetKeep: false, keepExpectedJoules: 100, targetExpectedJoules: 80 }), headline(recordFacts(decision(row.at, plan("c1", false, 80, 45_000_000)))))
  assert.deepEqual(of({ ...row, targetKeep: false, keepExpectedJoules: 12_000.5, targetExpectedJoules: 120.5 }), { text: "예상 에너지 11.9 kJ 절감" })
  assert.equal(of({ ...row, targetKeep: false, keepExpectedJoules: 100 }), null)
  assert.equal(of(row), null)
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
