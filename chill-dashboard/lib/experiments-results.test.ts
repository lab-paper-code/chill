import assert from "node:assert/strict"
import test from "node:test"
import { decodeExperiment } from "./experiments.ts"
import { baselineChoices, energyDelta, failedServices, partRows, serviceSummaries } from "./experiments-results.ts"

const request = (success: number, p95: number) => ({
  offered: 100, deadlineSuccessFraction: success, goodputRequestsPerSecond: 1.5, D1: success < 0.95 ? "FAIL" : "OBSERVED_THRESHOLD_MET",
  completedLatencyNS: { p50: 1e6, p95, p99: 3e6 }, slo: { percentile: 95, targetLatencyNanos: 2e6 },
  substages: [{ id: "a", durationSeconds: 60, rate: 1, completedLatencyNS: { p95 }, onTimeSuccessRate: success }, { id: "b", durationSeconds: 40, rate: 2, onTimeSuccessRate: 1 }],
})
const stage = (id: string, from: string, to: string, joules: number, success: number) => ({ id, from, to, kpis: { power: { energyStatus: "MEASURED", wholePoolJoules: joules }, requests: { svc: request(success, 1.5e6) } } })
const run = decodeExperiment(JSON.stringify({ id: "r", from: "", to: "", status: "", sources: [], stages: [
  stage("x", "2026-01-01T00:00:00Z", "2026-01-01T00:01:40Z", 1000, 0.9),
  stage("y", "2026-01-01T00:02:00Z", "2026-01-01T00:03:40Z", 800, 1),
] }))

test("results read model keeps server values and only converts or subtracts them", () => {
  const [x, y] = run.stages
  assert.deepEqual(failedServices(x), ["svc"])
  assert.deepEqual(failedServices(y), [])
  const [summary] = serviceSummaries(x, run)
  assert.equal(summary.latency.p95, 1.5)
  assert.deepEqual(summary.target, { percentile: 95, ms: 2 })
  assert.equal(summary.targetQuantile, 1.5)
  assert.deepEqual(energyDelta(x, y), { joules: 200, percent: 25 })
  const rows = partRows(run, "svc")
  assert.equal(rows.length, 4)
  assert.equal(rows[1].from - rows[0].from, 60_000)
  assert.equal(rows[0].joules, null)
  assert.deepEqual(baselineChoices(run, [run], x).map(item => item.label), ["y"])
})
