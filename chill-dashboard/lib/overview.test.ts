import assert from "node:assert/strict"
import test from "node:test"
import { changeItems, overviewRows } from "./overview.ts"
import type { LiveMetric, LiveMetrics, LiveSnapshot, LiveWorkload } from "./live.ts"

const workload = (uid: string, name: string, extra: Partial<LiveWorkload> = {}): LiveWorkload => ({
  uid, name, model: "m", definitionID: "d", slo: { percentile: 95, targetLatencyNanos: 2e8 }, autonomous: true, admission: true, pending: 0,
  readiness: { ready: true, reason: "Ready", observedAt: "2026-11-14T00:00:00Z" }, ...extra,
})
const metric = (received: number, completed: number, failed: number, extra: Partial<LiveMetric> = {}): LiveMetric =>
  ({ received, completed, failed, withinSLO: completed, latencySamples: completed, p50ms: 1, p90ms: 1, p95ms: 1, p99ms: 1, ...extra })
const frame = (second: number, workloads: Record<string, LiveMetric>): LiveMetrics => ({
  type: "metrics", seq: second, at: "", bucketAt: new Date(Date.UTC(2026, 10, 14, 0, 0, second)).toISOString(),
  chill_obslog_queue_length: 0, chill_obslog_oldest_wait_seconds: 0, workloads, nodes: {}, pool: { wholeWatts: null, activeWatts: null },
})
const snapshot = (workloads: LiveWorkload[]): LiveSnapshot => ({ workloads, nodes: [], instances: [], routing: {}, decision: null, actions: [], reservations: [] })

test("rows keep name order and classify by the server window and recent traffic", () => {
  const slo = { windowSeconds: 30, percentile: 95, targetMs: 200, samples: 10, withinTarget: 10 }
  const metrics = [
    frame(0, { a: metric(5, 5, 0), b: metric(4, 3, 1) }),
    // second 1 is missing: a gap, never a zero
    frame(50, { a: metric(5, 4, 0, { slo: { ...slo, quantileMs: 250, met: false } }), b: metric(0, 0, 0), c: metric(0, 0, 0) }),
  ]
  const rows = overviewRows(snapshot([workload("c", "zeta"), workload("a", "alpha"), workload("b", "beta")]), metrics)
  assert.deepEqual(rows.map(row => [row.name, row.state, row.label]), [["alpha", "attention", "목표 초과"], ["beta", "active", "가동"], ["zeta", "idle", "유휴"]])
  assert.equal(rows[0].success, 9 / 9)
  assert.equal(rows[1].success, 3 / 4)
  assert.equal(rows[2].success, null)
  assert.equal(rows[0].trend.length, 60)
  assert.equal(rows[0].trend[59], 5)
  assert.equal(rows[0].trend[58], null)
  assert.equal(rows[2].received, 0)
})

test("isolated failures stay active; only failures, or closed readiness while admitted, need attention; a change shows 변경 중", () => {
  const rows = overviewRows({
    ...snapshot([workload("a", "a"), workload("b", "b", { readiness: { ready: false, reason: "Preparing", observedAt: "2026-11-14T00:00:00Z" } }), workload("c", "c"), workload("d", "d")]),
    actions: [{ id: "x", kind: "prepare", node: { uid: "n", name: "n" }, workloadUID: "d", status: "running", startedAt: "2026-11-14T00:00:00Z", expectedSeconds: null, expectedSource: null }],
  }, [frame(10, { a: metric(3, 2, 1), c: metric(2, 0, 2), d: metric(1, 1, 0) }), frame(11, { a: metric(3, 3, 0), c: metric(1, 0, 1), d: metric(1, 1, 0) })])
  assert.deepEqual(rows.map(row => [row.state, row.label, row.tone]), [
    ["active", "가동", "ok"], ["attention", "실행 준비 중", "warn"], ["attention", "실패 3건", "fail"], ["active", "변경 중", "progress"],
  ])
})

test("changes: non-keep decisions, failed actions, settings newest first; cluster events counted", () => {
  const { items, cluster } = changeItems([
    { type: "event", at: "2026-11-14T00:00:01Z", kind: "decision", decision: { at: "2026-11-14T00:00:01Z", state: "Keep", reason: "", explanation: "" } },
    { type: "event", at: "2026-11-14T00:00:02Z", kind: "decision", decision: { at: "2026-11-14T00:00:02Z", state: "Feasible", reason: "", explanation: "" } },
    { type: "event", at: "2026-11-14T00:00:00Z", kind: "decision", decision: { at: "2026-11-14T00:00:00Z", state: "Overload", reason: "NoPlanMeetsSLO", explanation: "" } },
    { type: "event", at: "2026-11-14T00:00:03Z", kind: "cluster", cluster: { kind: "issue", at: "2026-11-14T00:00:03Z", event: "raised" } },
  ], [{ at: "2026-11-14T00:00:04Z", settings: { actorUsername: "op", actorUID: "u" } }])
  assert.deepEqual(items.map(item => item.title), ["설정 변경", "구성 변경", "판단 과부하"])
  assert.deepEqual(items.at(-1), { key: "decision:2026-11-14T00:00:00Z", tone: "fail", title: "판단 과부하", secondary: "목표 충족 구성 없음", at: "2026-11-14T00:00:00Z", href: "/scheduler/decisions?at=2026-11-14T00%3A00%3A00Z" })
  assert.equal(cluster, 1)
})
