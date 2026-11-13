import assert from "node:assert/strict"
import test from "node:test"
import { actuatorText, chartRows, deviceRole, energyParts, quantityBytes } from "./devices-view.ts"

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
