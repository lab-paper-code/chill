// Coverage matrix layout: rows are device class groups and power modes, columns execution paths.
import { coverageMeasurementStatus, coverageProgress, coverageSupportReasons } from "./measurement-coverage.ts"
import { executionPathLabel } from "./display-labels.ts"
import { coverageReason, measurementPhaseStatus, type StatusInfo } from "./status-labels.ts"
import type { BatchProgress, MeasurementCoverageCell as Cell } from "./read-types.ts"

/** Why a cell has its status: support reasons first, then failure classes, then assessment reasons. */
export function cellReasons(cell: Cell): string[] {
  const support = coverageMeasurementStatus(cell) === "unsupported" ? coverageSupportReasons(cell) : cell.support.verdict === "Compatible" ? [] : cell.support.reasons
  return [...new Set([...support.map(coverageReason), ...cell.failureSummary.map(coverageReason), ...cell.assessment.reasons.map(coverageReason)])]
}

/** "measured target / target" (or the measured count without a target); "" without progress. */
export function measuredText(progress: BatchProgress | null) {
  if (!progress) return ""
  if (progress.target.length) return `${progress.measured.filter(batch => progress.target.includes(batch)).length}/${progress.target.length}`
  return progress.measured.length ? String(progress.measured.length) : ""
}
export const cellMeasured = (cell: Cell) => measuredText(coverageProgress(cell))

/** A request the controller has not picked up yet has no phase. */
export const requestPhase = (phase: string | null | undefined): StatusInfo => phase ? measurementPhaseStatus(phase) : { label: "대기", tone: "idle" }

/** Matrix columns of a model's cells; paths whose short labels collide keep their names. */
export function matrixColumns(cells: Cell[]) {
  const names = [...new Set(cells.map(cell => cell.executionPath))].sort()
  const labels = names.map(name => executionPathLabel(name))
  return names.map((name, index) => ({ name, label: labels.indexOf(labels[index]) === labels.lastIndexOf(labels[index]) ? labels[index] : name }))
}

export type MatrixGroup = { deviceClass: string; modes: { mode: string; cells: Map<string, Cell> }[] }

const natural = (a: string, b: string) => a.localeCompare(b, "en", { numeric: true })

/** Device class groups and power-mode rows in natural order; a status filter keeps rows with at least one matching cell. */
export function matrixRows(cells: Cell[], deviceClass: string | null, status: string | null): MatrixGroup[] {
  const groups = new Map<string, Map<string, Map<string, Cell>>>()
  for (const cell of cells) {
    if (deviceClass && cell.deviceClass.name !== deviceClass) continue
    const modes = groups.get(cell.deviceClass.name) ?? new Map<string, Map<string, Cell>>()
    groups.set(cell.deviceClass.name, modes)
    const row = modes.get(cell.powerMode) ?? new Map<string, Cell>()
    modes.set(cell.powerMode, row)
    row.set(cell.executionPath, cell)
  }
  return [...groups].sort(([a], [b]) => natural(a, b)).flatMap(([name, modes]) => {
    const rows = [...modes].sort(([a], [b]) => natural(a, b))
      .filter(([, row]) => !status || [...row.values()].some(cell => coverageMeasurementStatus(cell) === status))
      .map(([mode, row]) => ({ mode, cells: row }))
    return rows.length ? [{ deviceClass: name, modes: rows }] : []
  })
}
