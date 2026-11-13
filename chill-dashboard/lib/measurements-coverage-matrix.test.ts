import assert from "node:assert/strict"
import test from "node:test"
import { cellReasons, matrixColumns, matrixRows, measuredText, pathLabel, requestPhase } from "./measurements-coverage-matrix.ts"
import type { MeasurementCoverageCell } from "./read-types.ts"

const cell = (deviceClass: string, powerMode: string, executionPath: string, displayStatus: string, verdict = "Compatible") => ({
  id: `m/${deviceClass}/${executionPath}/${powerMode}`, model: { name: "m", uid: "m" }, deviceClass: { name: deviceClass, uid: deviceClass }, executionPath, powerMode,
  support: { verdict, node: null, reasons: [], diagnostics: [] }, assessment: { displayStatus, reasons: [] }, variants: [], batchProgress: null, failureSummary: [],
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

test("cell reasons lead with support, then failures and assessment, in Korean and deduplicated", () => {
  const unknown = { ...cell("a", "x", "cuda", "unknown", "Unknown"), support: { verdict: "Unknown", node: null, diagnostics: [], reasons: [{ code: "RuntimeDeclarationMissing", message: "no trusted execution declaration" }] } } as unknown as MeasurementCoverageCell
  assert.deepEqual(cellReasons(unknown), ["실행 환경 선언 없음"])
  const problem = { ...cell("a", "x", "cpu", "problem"), failureSummary: [{ code: "RuntimeFailed", label: "실행 오류" }, { code: "RuntimeFailed", label: "실행 오류" }], assessment: { displayStatus: "problem", reasons: [{ code: "InvalidEvidence", message: "unsupported schema" }, { code: "NewCode", message: "x" }] } } as unknown as MeasurementCoverageCell
  assert.deepEqual(cellReasons(problem), ["실행 오류", "측정 근거 검증 실패", "기타 사유"])
  const unsupported = { ...cell("a", "x", "cpu-arm64", "unsupported", "Incompatible"), support: { verdict: "Incompatible", node: null, diagnostics: [], reasons: [{ code: "ArtifactArchitectureMismatch", message: "m" }] } } as unknown as MeasurementCoverageCell
  assert.deepEqual(cellReasons(unsupported), ["아키텍처 불일치"])
})

test("measured batches count only targets; requests without a phase wait", () => {
  assert.equal(measuredText(null), "")
  assert.equal(measuredText({ basis: "requested", target: [1, 2, 4, 8], measured: [1, 2, 16], criteriaMet: [], missing: [], inProgress: [], unknown: [] }), "2/4")
  assert.equal(measuredText({ basis: "requested", target: [], measured: [1, 2], criteriaMet: [], missing: [], inProgress: [], unknown: [] }), "2")
  assert.equal(requestPhase(undefined).label, "대기")
  assert.equal(requestPhase("Failed").tone, "fail")
})
