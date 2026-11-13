import assert from "node:assert/strict"
import { test } from "node:test"
import { planRows, provisionalRows, runSteps, settingRows, stageSteps, type Run, type RunStage } from "./experiments-run.ts"

const t = (minute: number) => `2026-11-11T21:${String(minute).padStart(2, "0")}:00Z`
const stage = (over: Partial<RunStage>): RunStage => ({ id: "a", name: "s0", uid: "u0", ...over })

test("run steps follow the planned stages and mark failed procedures", () => {
  const run: Run = {
    name: "r", uid: "u", phase: "Running", spec: { stages: [{ id: "a" }, { id: "b" }] },
    procedures: [{ step: "prepare", startedAt: t(0), finishedAt: t(1), reason: "ProcedureComplete" }],
    stages: [stage({ startedAt: t(1), finishedAt: t(5), procedures: [{ step: "stage", startedAt: t(1), finishedAt: t(4), reason: "ExecutionLost" }] }), stage({ id: "b", startedAt: t(5) })],
  }
  assert.deepEqual(runSteps(run).map(s => [s.label, s.state]), [["준비", "done"], ["a", "failed"], ["b", "running"], ["정리", "waiting"], ["게시", "waiting"]])
  assert.equal(runSteps({ ...run, phase: "Done", finishedAt: t(9) })[2].state, "stalled")
})

test("stage steps use boundaries, close them when the load procedure ended, and fall back to the judge procedure", () => {
  const running = stage({ procedures: [{ step: "factor", startedAt: t(0), finishedAt: t(1) }, { step: "stage", startedAt: t(1), finishedAt: null }], progress: [{ phase: "loadStarted", at: t(2) }, { phase: "loadEnded", at: t(3) }, { phase: "drainStarted", at: t(3) }] })
  assert.deepEqual(stageSteps(running, true).map(s => s.state), ["done", "done", "running", "waiting", "waiting", "waiting", "waiting"])
  const ended = stage({ procedures: [{ step: "stage", startedAt: t(1), finishedAt: t(4) }, { step: "judge", startedAt: t(4), finishedAt: t(5), reason: "ProcedureComplete" }] })
  const steps = stageSteps(ended, false)
  assert.deepEqual(steps.map(s => s.state), ["waiting", "done", "waiting", "waiting", "waiting", "done", "waiting"])
  assert.equal(steps[1].at, t(1))
})

test("provisional rows pick the latency at the service's SLO percentile", () => {
  const rows = provisionalRows({ workloads: { b: { onTimeSuccessRate: 0.99, goodputRequestsPerSecond: 3, offered: 10, slo: { percentile: 99, targetLatencyNanos: 1_000_000_000 }, completedLatencyNS: { p95: 5e8, p99: 9e8 } }, a: {} } })
  assert.deepEqual(rows.map(r => r.name), ["a", "b"])
  assert.equal(rows[0].latencyMs, null)
  assert.deepEqual([rows[1].latencyMs, rows[1].targetMs, rows[1].percentile], [900, 1000, 99])
})

test("plan rows list parts per stage and keep a stage whose scenario is missing", () => {
  const spec = { stages: [{ id: "a", loadScenarioRef: { name: "s", uid: "1" } }, { id: "b", loadScenarioRef: { name: "gone", uid: "2" } }] }
  const { rows, services } = planRows(spec, [{ name: "s", uid: "1", spec: { parts: [{ id: "p1", durationSeconds: 60, rates: { y: 2, x: 1 } }, { id: "p2", durationSeconds: 30, rates: { x: 3 } }] } }])
  assert.deepEqual(rows.map(r => [r.stage, r.part, r.seconds]), [["a", "p1", 60], ["a", "p2", 30], ["b", null, null]])
  assert.deepEqual(services, ["x", "y"])
})

test("setting rows name services instead of UIDs and align values per scope", () => {
  const name = (uid: string) => ({ u1: "svc" })[uid] ?? "미확인 서비스"
  const rows = settingRows([{ controlIntervalNanos: 10_000_000_000, controls: { power: false }, minimumDemand: { u1: 20 } }, { autonomousWorkloads: ["u1"], unknownKey: 3 }], name)
  const byKey = Object.fromEntries(rows.map(r => [r.key, r]))
  assert.deepEqual(byKey.controlIntervalNanos.values, ["10.00 s", null])
  assert.equal(byKey["controls.power"].values[0], "꺼짐")
  assert.equal(byKey["minimumDemand.u1"].label, "최소 수요 svc")
  assert.deepEqual(byKey.autonomousWorkloads.values, [null, "svc"])
  assert.equal(byKey.unknownKey.label, null)
  const all = settingRows([{ controls: { placement: true, resources: true, replicas: true, routing: true, batching: true, power: true, membership: true } }], name)
  assert.deepEqual(all.map(r => [r.key, r.values[0]]), [["controls", "모두 켜짐"]])
})
