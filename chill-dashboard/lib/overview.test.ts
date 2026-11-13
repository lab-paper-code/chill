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

test("rows keep name order and classify by server window, recent failures and recent traffic", () => {
  const slo = { windowSeconds: 30, percentile: 95, targetMs: 200, samples: 10, withinTarget: 10 }
  const metrics = [
    frame(0, { a: metric(5, 5, 0), b: metric(4, 3, 1) }),
    // second 1 is missing: a gap, never a zero
    frame(50, { a: metric(5, 4, 0, { slo: { ...slo, quantileMs: 250, met: false } }), b: metric(0, 0, 0), c: metric(0, 0, 0) }),
  ]
  const rows = overviewRows(snapshot([workload("c", "zeta"), workload("a", "alpha"), workload("b", "beta")]), metrics)
  assert.deepEqual(rows.map(row => [row.name, row.state, row.reason]), [["alpha", "attention", "목표 초과"], ["beta", "active", null], ["zeta", "idle", null]])
  assert.equal(rows[0].success, 9 / 9)
  assert.equal(rows[1].success, 3 / 4)
  assert.equal(rows[2].success, null)
  assert.equal(rows[0].trend.length, 60)
  assert.equal(rows[0].trend[59], 5)
  assert.equal(rows[0].trend[58], null)
  assert.equal(rows[2].received, 0)
})

test("failures in the last ten buckets and closed readiness while admitted need attention", () => {
  const rows = overviewRows(snapshot([workload("a", "a"), workload("b", "b", { readiness: { ready: false, reason: "Preparing", observedAt: "2026-11-14T00:00:00Z" } })]),
    [frame(10, { a: metric(3, 2, 1) }), frame(11, { a: metric(3, 3, 0) })])
  assert.equal(rows[0].reason, "최근 10초 실패 1건")
  assert.equal(rows[1].state, "attention")
})

test("changes: non-keep decisions, failed actions, settings newest first; cluster events counted", () => {
  const { items, cluster } = changeItems([
    { type: "event", at: "2026-11-14T00:00:01Z", kind: "decision", decision: { at: "2026-11-14T00:00:01Z", state: "Keep", reason: "", explanation: "" } },
    { type: "event", at: "2026-11-14T00:00:02Z", kind: "decision", decision: { at: "2026-11-14T00:00:02Z", state: "Feasible", reason: "", explanation: "" } },
    { type: "event", at: "2026-11-14T00:00:03Z", kind: "cluster", cluster: { kind: "issue", at: "2026-11-14T00:00:03Z", event: "raised" } },
  ], [{ at: "2026-11-14T00:00:04Z", settings: { actorUsername: "op", actorUID: "u" } }])
  assert.deepEqual(items.map(item => item.label), ["설정 변경", "판단 변경"])
  assert.equal(cluster, 1)
})
