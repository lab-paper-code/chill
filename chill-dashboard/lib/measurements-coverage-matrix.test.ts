import assert from "node:assert/strict"
import test from "node:test"
import { matrixColumns, matrixRows, pathLabel } from "./measurements-coverage-matrix.ts"
import type { MeasurementCoverageCell } from "./read-types.ts"

const cell = (deviceClass: string, powerMode: string, executionPath: string, displayStatus: string, verdict = "Compatible") => ({
  id: `m/${deviceClass}/${executionPath}/${powerMode}`, model: { name: "m", uid: "m" }, deviceClass: { name: deviceClass, uid: deviceClass }, executionPath, powerMode,
  support: { verdict, node: null, reasons: [], diagnostics: [] }, assessment: { displayStatus }, variants: [], batchProgress: null,
}) as unknown as MeasurementCoverageCell

test("path labels drop the raw name and keep colliding paths distinct", () => {
  assert.equal(pathLabel("firstfloor-ort-cpu-int8-arm64"), "CPU 정수 8비트 ARM 64비트")
  assert.equal(pathLabel("custom-path"), "custom-path")
  const cells = [cell("a", "x", "ort-cuda-rnn-arm64", "usable"), cell("a", "x", "ort-cuda-vlm-arm64", "usable"), cell("a", "x", "firstfloor-rknnlite", "usable")]
  assert.deepEqual(matrixColumns(cells).map(column => column.label), ["NPU", "ort-cuda-rnn-arm64", "ort-cuda-vlm-arm64"])
})

test("rows group by class in natural power-mode order and the status filter keeps matching rows only", () => {
  const cells = [
    cell("orin", "MODE_15W", "cpu", "problem"), cell("orin", "MODE_7W", "cpu", "usable"), cell("orin", "MODE_7W", "cuda", "running"),
    cell("nano", "5W", "cpu", "unmeasured"),
  ]
  assert.deepEqual(matrixRows(cells, null, null).map(group => [group.deviceClass, group.modes.map(row => row.mode)]), [["nano", ["5W"]], ["orin", ["MODE_7W", "MODE_15W"]]])
  assert.deepEqual(matrixRows(cells, null, "running").map(group => [group.deviceClass, group.modes.map(row => [...row.cells.keys()])]), [["orin", [["cpu", "cuda"]]]])
  assert.deepEqual(matrixRows(cells, "nano", "usable"), [])
})
