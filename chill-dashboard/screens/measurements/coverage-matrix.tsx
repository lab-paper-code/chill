"use client"

import * as React from "react"
import { coverageMeasurementStatus } from "@/lib/measurement-coverage"
import { deviceClassLabel, powerModeLabel } from "@/lib/display-labels"
import { cellMeasured, cellReasons, matrixColumns, type MatrixGroup } from "@/lib/measurements-coverage-matrix"
import { coverageMeasurementStates, measurementStatus, type StatusTone } from "@/lib/status-labels"
import type { MeasurementCoverageCell as Cell } from "@/lib/read-types"
import { Matrix, MatrixCell, Missing, StatusDot } from "@/components/kit"

export const STATUSES = Object.keys(coverageMeasurementStates)

// One legend entry per icon: states that share a tone share the icon.
const LEGEND: { tone: StatusTone; label: string }[] = [
  { tone: "ok", label: coverageMeasurementStates.usable },
  { tone: "progress", label: coverageMeasurementStates.running },
  { tone: "warn", label: `${coverageMeasurementStates.insufficient}, ${coverageMeasurementStates.review}` },
  { tone: "fail", label: coverageMeasurementStates.problem },
  { tone: "idle", label: "미측정, 미확인" },
]

export function CoverageMatrix({ rows, cells, status, onOpen, empty }: {
  rows: MatrixGroup[]; cells: Cell[]; status: string | null
  onOpen: (id: string) => void; empty: React.ReactNode
}) {
  // Columns come from the whole model, so filters never move them.
  const paths = React.useMemo(() => matrixColumns(cells), [cells])
  return (
    <Matrix
      label="측정 범위"
      rowHeader="전력 모드"
      columns={paths.map(path => ({ key: path.name, label: path.label, hint: path.name }))}
      groups={rows.map(group => ({
        key: group.deviceClass,
        label: deviceClassLabel(group.deviceClass),
        rows: group.modes.map(row => ({
          key: row.mode,
          label: powerModeLabel(row.mode),
          cells: Object.fromEntries([...row.cells].map(([path, cell]) => [path, <CellButton key={path} cell={cell} dim={!!status && coverageMeasurementStatus(cell) !== status} onOpen={onOpen} />])),
        })),
      }))}
      legend={
        <ul aria-label="범례" className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          {LEGEND.map(item => <li key={item.tone}><StatusDot tone={item.tone} label={item.label} showLabel /></li>)}
          <li className="flex items-center gap-1.5"><span aria-hidden="true">—</span>지원 안 함</li>
        </ul>
      }
      missingReason="조합 없음"
      empty={empty}
    />
  )
}

function CellButton({ cell, dim, onOpen }: { cell: Cell; dim: boolean; onOpen: (id: string) => void }) {
  const code = coverageMeasurementStatus(cell)
  if (code === "unsupported") return <Missing reason={`지원 안 함: ${cellReasons(cell).join(", ")}`} />
  const status = measurementStatus(code)
  const measured = cellMeasured(cell)
  return (
    <MatrixCell label={`${status.label}${measured ? ` 배치 ${measured}` : ""}`} dim={dim} onClick={() => onOpen(cell.id)}>
      <StatusDot status={status} label={measured} showLabel />
    </MatrixCell>
  )
}
