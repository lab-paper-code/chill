// Coverage matrix layout: rows are device class groups and power modes, columns execution paths.
import { coverageMeasurementStatus } from "./measurement-coverage.ts"
import { executionPathLabel } from "./measurements.ts"
import type { MeasurementCoverageCell as Cell } from "./read-types.ts"

/** "CPU · ARM 64비트 (firstfloor-ort-cpu-arm64)" → "CPU ARM 64비트"; unknown backends keep the path name. */
export function pathLabel(path: string) {
  const label = executionPathLabel(path)
  return label.endsWith(")") ? label.slice(0, label.lastIndexOf(" (")).replaceAll(" · ", " ") : path
}

/** Matrix columns of a model's cells; paths whose short labels collide keep their names. */
export function matrixColumns(cells: Cell[]) {
  const names = [...new Set(cells.map(cell => cell.executionPath))].sort()
  const labels = names.map(pathLabel)
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
