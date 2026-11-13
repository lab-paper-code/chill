import assert from "node:assert/strict"
import test from "node:test"
import { actuatorText, chartRows, deviceRole, energyParts, measurementHolds, quantityBytes } from "./devices-view.ts"
import type { ProfilingRun } from "./read-types.ts"

test("measuring devices name the request and run that hold them", () => {
  const run = (name: string, request: string, node: string, created: string) => ({
    namespace: "ns", name, uid: `${name}-uid`, resourceVersion: "1", generation: 1, creationTimestamp: created, spec: { targetNode: node }, status: {},
    ownerReferences: [{ apiVersion: "edge.dacs.io/v1alpha1", kind: "BatchEnergyOptimization", name: request, uid: `${request}-uid`, controller: true }],
  }) as ProfilingRun
  const runs = [run("r-b1", "req-a", "n1", "2026-11-13T10:00:00Z"), run("r-b2", "req-a", "n1", "2026-11-13T11:00:00Z"), run("r-x", "req-b", "n2", "2026-11-13T09:00:00Z")]
  const lease = (node: string, holder: string) => ({ owner: holder, nodes: [{ uid: node, name: node }], workloadUIDs: [], podUIDs: [], instances: [], reason: `measurement-lease/${holder}: profiling run in progress`, explanation: "" })
  const holds = measurementHolds(["n1", "n2", "n3"], [lease("n1", "req-a-uid"), lease("n3", "gone")], runs)
  assert.deepEqual(holds.get("n1"), { holder: "req-a-uid", request: { name: "req-a", uid: "req-a-uid" }, run: { name: "r-b2", uid: "r-b2-uid" } })
  assert.deepEqual(holds.get("n2"), { holder: null, request: { name: "req-b", uid: "req-b-uid" }, run: { name: "r-x", uid: "r-x-uid" } })
  assert.deepEqual(holds.get("n3"), { holder: "gone", request: null, run: null })
})

test("device role: exclusion first, then measurement, service, available", () => {
  assert.equal(deviceRole(["서비스 배치", "측정 중"], true)?.label, "제외")
  assert.equal(deviceRole(["서비스 배치", "측정 중"], false)?.label, "측정")
  assert.equal(deviceRole(["서비스 배치"], false)?.label, "서비스")
  assert.equal(deviceRole(["가용"], false)?.label, "가용")
  assert.equal(deviceRole(["제외, 기타"], false), null)
})

test("quantities become bytes; anything else is absent", () => {
  assert.equal(quantityBytes("64Gi"), 64 * 2 ** 30)
  assert.equal(quantityBytes("4G"), 4e9)
  assert.equal(quantityBytes("1024"), 1024)
  assert.equal(quantityBytes("1.5Mi"), 1.5 * 2 ** 20)
  assert.equal(quantityBytes("64GB"), null)
  assert.equal(quantityBytes(undefined), null)
})

test("actuators and energy units", () => {
  assert.deepEqual(actuatorText({ kind: "NvidiaLockedGPUClock", nvidiaLockedGPUClock: { minMHz: 900, maxMHz: 1500 } }), { method: "GPU 클럭 고정", setting: "900–1500 MHz" })
  assert.deepEqual(actuatorText({ kind: "JetsonNVPModel", jetsonNVPModel: { id: 0 } }).setting, "모드 0")
  assert.deepEqual(energyParts(12_345), { value: 12.345, unit: "kJ" })
  assert.deepEqual(energyParts(null), { value: null, unit: "J" })
})

test("chart rows break the line where time is missing, keep measured zero", () => {
  const point = (from: number, to: number, watts: number | null) => ({ from, to, wholeWatts: watts, activeWatts: 0, nodes: {} })
  const rows = chartRows([point(0, 5, 0), point(5, 10, 3), point(20, 21, 4)], p => ({ w: p.wholeWatts }))
  assert.deepEqual(rows, [{ t: 0, w: 0 }, { t: 5, w: 3 }, { t: 10, w: null }, { t: 20, w: 4 }])
})
