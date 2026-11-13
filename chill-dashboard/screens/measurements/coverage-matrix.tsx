"use client"

import * as React from "react"
import { cn } from "@/lib/utils"
import { coverageMeasurementStates, coverageMeasurementStatus, coverageProgress, coverageSupportReasons, deviceLabel } from "@/lib/measurement-coverage"
import { matrixColumns, type MatrixGroup } from "@/lib/measurements-coverage-matrix"
import { measurementStatus, type StatusTone } from "@/lib/status-labels"
import type { MeasurementCoverageCell as Cell } from "@/lib/read-types"
import { Table, TableBody, TableContent, TableHead, TableHeader, TableHeading, TableRow, TableCell } from "@/components/custom/blocks/TableBlock"
import { Button, Hint, Missing, StatusDot } from "@/components/kit"

export const STATUSES = Object.keys(coverageMeasurementStates)

// One legend entry per icon: states that share a tone share the icon.
const LEGEND: { tone: StatusTone; label: string }[] = [
  { tone: "ok", label: "측정 있음" },
  { tone: "progress", label: "진행" },
  { tone: "warn", label: "부족, 검토 필요" },
  { tone: "fail", label: "문제" },
  { tone: "idle", label: "미측정, 미확인" },
]

export const supportReason = (cell: Cell) =>
  coverageSupportReasons(cell).map(reason => reason.code === "ArtifactArchitectureMismatch" ? "아키텍처 불일치" : reason.code === "DeclarationMismatch" ? "실행 경로 선언 불일치" : reason.label ?? reason.code).join(", ")

export function CoverageMatrix({ rows, cells, status, onOpen, empty }: {
  rows: MatrixGroup[]; cells: Cell[]; status: string | null
  onOpen: (id: string) => void; empty: React.ReactNode
}) {
  // Columns come from the whole model, so filters never move them.
  const paths = React.useMemo(() => matrixColumns(cells), [cells])
  return (
    <Table>
      <TableHeading className="gap-2 px-3 py-2.5">
        <ul aria-label="범례" className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          {LEGEND.map(item => <li key={item.tone}><StatusDot tone={item.tone} label={item.label} showLabel /></li>)}
          <li className="flex items-center gap-1.5"><span aria-hidden="true">—</span>지원 안 함</li>
        </ul>
      </TableHeading>
      <TableContent label="측정 범위" style={{ minWidth: `${7 + paths.length * 7}rem` }}>
        <caption className="sr-only">측정 범위</caption>
        <TableHeader>
          <TableRow isHeader>
            <TableHead className="sticky left-0 z-[1] w-28 bg-[color-mix(in_oklab,var(--muted)_60%,var(--card))] sm:w-40">전력 모드</TableHead>
            {paths.map(path => <TableHead key={path.name}><Hint text={path.name} focusable>{path.label}</Hint></TableHead>)}
          </TableRow>
        </TableHeader>
        {rows.length === 0 ? (
          <TableBody><tr><td colSpan={paths.length + 1} className="p-0">{empty}</td></tr></TableBody>
        ) : rows.map(group => (
          <TableBody key={group.deviceClass}>
            <TableRow className="bg-muted/40 hover:bg-muted/40">
              <TableHead scope="rowgroup" colSpan={paths.length + 1} className="text-foreground">
                <span className="sticky left-3">{deviceLabel(group.deviceClass)}</span>
              </TableHead>
            </TableRow>
            {group.modes.map(row => (
              <TableRow key={row.mode}>
                <TableHead scope="row" className="sticky left-0 z-[1] bg-card font-normal text-foreground group-hover:bg-[color-mix(in_oklab,var(--muted)_50%,var(--card))]">{row.mode}</TableHead>
                {paths.map(path => {
                  const cell = row.cells.get(path.name)
                  return <TableCell key={path.name}>{cell ? <MatrixCell cell={cell} dim={!!status && coverageMeasurementStatus(cell) !== status} onOpen={onOpen} /> : <Missing reason="조합 없음" />}</TableCell>
                })}
              </TableRow>
            ))}
          </TableBody>
        ))}
      </TableContent>
    </Table>
  )
}

function MatrixCell({ cell, dim, onOpen }: { cell: Cell; dim: boolean; onOpen: (id: string) => void }) {
  const code = coverageMeasurementStatus(cell)
  if (code === "unsupported") return <Missing reason={supportReason(cell)} />
  const status = measurementStatus(code)
  const progress = coverageProgress(cell)
  const measured = progress ? (progress.target.length ? `${progress.measured.filter(batch => progress.target.includes(batch)).length}/${progress.target.length}` : progress.measured.length ? String(progress.measured.length) : "") : ""
  return (
    <Button variant="ghost" size="sm" className={cn("-ml-2 tabular-nums", dim && "opacity-40")} aria-label={`${status.label}${measured ? ` 배치 ${measured}` : ""}`} onClick={() => onOpen(cell.id)}>
      <StatusDot status={status} label={measured} showLabel />
    </Button>
  )
}
