// Coverage matrix layout: rows are device class groups and power modes, columns execution paths.
import { coverageMeasurementStatus, coverageProgress, coverageSupportReasons } from "./measurement-coverage.ts"
import { executionPathLabel } from "./measurements.ts"
import { measurementPhaseStatus, type StatusInfo } from "./status-labels.ts"
import type { BatchProgress, MeasurementCoverageCell as Cell } from "./read-types.ts"

const ARCHITECTURES: Record<string, string> = { arm64: "ARM 64비트", amd64: "x86 64비트" }
export const architectureLabel = (architecture: string) => ARCHITECTURES[architecture] ?? architecture.toUpperCase()

// Server reason codes of coverage cells, compatibility checks and assessments; missing from lib/status-labels.ts.
const REASONS: Record<string, string> = {
  ArtifactArchitectureMismatch: "아키텍처 불일치",
  DeclarationMismatch: "실행 경로 선언 불일치",
  RuntimeDeclarationMissing: "실행 환경 선언 없음",
  AcceptedEvidenceUnavailable: "검증된 측정 근거 없음",
  InvalidEvidence: "측정 근거 검증 실패",
  PlannedConditionUnconfirmed: "실행 조건 미확인",
  TargetBatchMissing: "목표 배치 부족",
}
/** Korean word of a reason; the server label wins, an unknown code never shows raw. */
export const reasonLabel = (reason: { code: string; label?: string }) => reason.label ?? REASONS[reason.code] ?? "기타 사유"

/** Why a cell has its status: support reasons first, then failure classes, then assessment reasons. */
export function cellReasons(cell: Cell): string[] {
  const support = coverageMeasurementStatus(cell) === "unsupported" ? coverageSupportReasons(cell) : cell.support.verdict === "Compatible" ? [] : cell.support.reasons
  return [...new Set([...support.map(reasonLabel), ...cell.failureSummary.map(reasonLabel), ...cell.assessment.reasons.map(reasonLabel)])]
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

/** "CPU ARM 64비트 (firstfloor-ort-cpu-arm64)" → "CPU ARM 64비트"; unknown backends keep the path name. */
export function pathLabel(path: string) {
  const label = executionPathLabel(path)
  return label.endsWith(")") ? label.slice(0, label.lastIndexOf(" (")) : path
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
