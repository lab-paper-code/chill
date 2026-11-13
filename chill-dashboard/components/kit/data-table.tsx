"use client"

import * as React from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"
import { Download } from "lucide-react"
import { createColumnHelper, createPaginatedRowModel, createSortedRowModel, rowPaginationFeature, rowSortingFeature, sortFns, tableFeatures, useTable, type PaginationState, type RowData, type SortingState } from "@tanstack/react-table"
import { cn } from "@/lib/utils"
import { Table, TableBody, TableCell, TableContent, TableFooter, TableHead, TableHeader, TableHeading, TableRow } from "@/components/custom/blocks/TableBlock"
import { Pagination } from "@/components/custom/blocks/PaginationBlock"
import Button from "@/components/custom/blocks/Button"
import { Clip, Hint, Missing } from "./display"
import { MoreMenu, type MoreAction } from "./page"
import { EmptyState } from "./query"

const features = tableFeatures({ rowPaginationFeature, rowSortingFeature, sortFns, paginatedRowModel: createPaginatedRowModel(), sortedRowModel: createSortedRowModel() })

// A pinned first column paints the row's own background (hover, selected) over the cells scrolling under it.
const pinHead = "sticky left-0 z-[1] bg-[color-mix(in_oklab,var(--muted)_60%,var(--card))] shadow-[inset_-1px_0_var(--border)]"
const pinCell = "sticky left-0 z-[1] bg-card shadow-[inset_-1px_0_var(--border)] group-hover:bg-[color-mix(in_oklab,var(--muted)_50%,var(--card))] group-data-[selected=true]:bg-[color-mix(in_oklab,var(--primary)_12%,var(--card))]"
// Default: pinned only on phones, where the table scrolls sideways.
const phonePinHead = "max-sm:sticky max-sm:left-0 max-sm:z-[1] max-sm:bg-[color-mix(in_oklab,var(--muted)_60%,var(--card))] max-sm:shadow-[inset_-1px_0_var(--border)]"
const phonePinCell = "max-sm:sticky max-sm:left-0 max-sm:z-[1] max-sm:bg-card max-sm:shadow-[inset_-1px_0_var(--border)] max-sm:group-hover:bg-[color-mix(in_oklab,var(--muted)_50%,var(--card))] max-sm:group-data-[selected=true]:bg-[color-mix(in_oklab,var(--primary)_12%,var(--card))]"
// Narrow containers (under 30rem, e.g. phones): each row becomes a block of label/value lines
// (responsive-table guide). The labels are the cells' `data-label`; sortable headers stay as a row of
// sort buttons, the others are hidden. Container queries, so a narrow column on a desktop stacks too.
const stackTable = "@max-[30rem]/dt:block @max-[30rem]/dt:min-w-0!"
const stackHeadRow = "@max-[30rem]/dt:flex @max-[30rem]/dt:flex-wrap @max-[30rem]/dt:gap-x-1 @max-[30rem]/dt:px-2"
const stackHeadSortable = "@max-[30rem]/dt:block @max-[30rem]/dt:shrink-0 @max-[30rem]/dt:px-1! @max-[30rem]/dt:[&>button]:mx-0"
const stackHeadPlain = "@max-[30rem]/dt:hidden"
const stackRow = "@max-[30rem]/dt:block @max-[30rem]/dt:py-2"
const stackCell = "@max-[30rem]/dt:flex @max-[30rem]/dt:max-w-none @max-[30rem]/dt:items-baseline @max-[30rem]/dt:justify-between @max-[30rem]/dt:gap-3 @max-[30rem]/dt:py-1 @max-[30rem]/dt:px-4! @max-[30rem]/dt:before:shrink-0 @max-[30rem]/dt:before:text-xs @max-[30rem]/dt:before:text-muted-foreground @max-[30rem]/dt:before:content-[attr(data-label)]"
const stackTitleCell = "@max-[30rem]/dt:block @max-[30rem]/dt:max-w-none @max-[30rem]/dt:py-1 @max-[30rem]/dt:px-4! @max-[30rem]/dt:font-medium"

type Scalar = string | number | bigint | null | undefined
export type Column<T> = {
  key: string
  header: string
  /** Shown in the header as "지연 (ms)"; cells then carry bare numbers. */
  unit?: string
  /** Right-aligned tabular numbers. */
  numeric?: boolean
  /** Cell content. Return null/undefined for an absent value ("—"). */
  cell: (row: T) => React.ReactNode
  /** Makes the column sortable; also the CSV value unless `csv` is given. */
  sortValue?: (row: T) => Scalar
  csv?: (row: T) => Scalar
  /** Reason shown on "—" cells of this column. */
  missingReason?: string
  /** Reason for one row's "—"; falls back to `missingReason`. */
  missing?: (row: T) => string | undefined
  /** CSS width, e.g. "28%" or "8rem". Columns without one share the rest. */
  width?: string
}

export type ServerPaging =
  | { mode: "offset"; offset: number; limit: number; total?: number | null; onOffsetChange: (offset: number) => void }
  | { mode: "cursor"; hasPrevious: boolean; hasNext: boolean; onPrevious: () => void; onNext: () => void }

export type SortState = { key: string; desc: boolean }

export type DataTableProps<T> = {
  /** Accessible name of the table (also the scroll region's label). */
  label: string
  rows: T[]
  columns: Column<T>[]
  rowKey: (row: T) => string
  /** Visible heading inside the table card. */
  title?: React.ReactNode
  /** Filters on the left of the heading row, e.g. a FilterBar. */
  toolbar?: React.ReactNode
  /** Buttons on the right of the heading row. */
  actions?: React.ReactNode
  /** Row navigates to a page: the first cell becomes a link and the whole row is clickable. */
  rowHref?: (row: T) => string
  /** Row opens something in place, e.g. a DetailDrawer. */
  onRowClick?: (row: T) => void
  selectedKey?: string | null
  /** Per-row menu in a last, narrow column (not one of the seven). Named "동작 <first cell>". */
  rowActions?: (row: T) => MoreAction[]
  /** Initial client sort; ignored when `sort` is controlled. */
  defaultSort?: SortState
  /** Controlled sort (required for server-sorted data, together with onSortChange). */
  sort?: SortState | null
  onSortChange?: (sort: SortState | null) => void
  /** Client paging page size. 0 shows every row. */
  pageSize?: number
  /** Server paging; rows are then one page and are not sorted or sliced here. */
  paging?: ServerPaging
  /** Adds "내려받기": CSV of the rows currently loaded. */
  download?: { filename: string }
  empty?: React.ReactNode
  /** Dims the rows while a refetch is in flight. */
  busy?: boolean
  /** "compact": tighter cells and no minimum width, for tables in dialogs and drawers. */
  density?: "default" | "compact"
  /** Minimum table width in px before it scrolls sideways; default 104 px per column (0 with compact). */
  minWidth?: number
  /** Keeps the first column in view while the rest scrolls, at every width (default: phones, with `narrow="scroll"`). */
  stickyFirst?: boolean
  /**
   * Below a 30rem container: "stack" (default) turns each row into label/value lines, the first cell as its
   * title; "scroll" keeps the columns and scrolls sideways with the first column pinned (default with compact).
   */
  narrow?: "stack" | "scroll"
  className?: string
}

function csvText(value: Scalar) {
  if (value == null) return ""
  const text = String(value)
  return /[",\n\r]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text
}

/** Sortable, paged table with Avara table markup and TanStack table state. */
export function DataTable<T extends RowData>({ label, rows, columns, rowKey, title, toolbar, actions, rowHref, onRowClick, selectedKey, rowActions, defaultSort, sort, onSortChange, pageSize = 25, paging, download, empty, busy, density = "default", minWidth, stickyFirst = false, narrow, className }: DataTableProps<T>) {
  const router = useRouter()
  const id = React.useId()
  const [localSorting, setLocalSorting] = React.useState<SortingState>(defaultSort ? [{ id: defaultSort.key, desc: defaultSort.desc }] : [])
  const [pageIndex, setPageIndex] = React.useState(0)
  const controlled = sort !== undefined
  const sorting: SortingState = controlled ? (sort ? [{ id: sort.key, desc: sort.desc }] : []) : localSorting
  const clientPaging = !paging && pageSize > 0
  // Live data replaces `rows` every tick; the page stays where the user put it, clamped when rows shrink.
  const pagination: PaginationState = clientPaging
    ? { pageIndex: Math.min(pageIndex, Math.max(Math.ceil(rows.length / pageSize) - 1, 0)), pageSize }
    : { pageIndex: 0, pageSize: Math.max(rows.length, 1) }
  const helper = createColumnHelper<typeof features, T>()
  const table = useTable({
    features,
    data: rows,
    columns: helper.columns(columns.map(column => helper.accessor(row => column.sortValue?.(row) ?? undefined, { id: column.key, header: column.header, enableSorting: !!column.sortValue, sortUndefined: "last" }))),
    getRowId: row => rowKey(row),
    state: { sorting, pagination },
    manualPagination: !clientPaging,
    autoResetPageIndex: false,
    enableMultiSort: false,
    // First activation sorts ascending for every type; repeats toggle.
    sortDescFirst: false,
    manualSorting: !!paging,
    onSortingChange: update => {
      const next = typeof update === "function" ? update(sorting) : update
      const value = next[0] ? { key: next[0].id, desc: next[0].desc } : null
      if (controlled) onSortChange?.(value)
      else { setLocalSorting(next); onSortChange?.(value) }
      setPageIndex(0)
    },
    onPaginationChange: update => setPageIndex((typeof update === "function" ? update(pagination) : update).pageIndex),
  })
  const visible = table.getRowModel().rows
  const clickable = !!rowHref || !!onRowClick
  const compact = density === "compact"
  const width = minWidth ?? (compact ? 0 : Math.max(columns.length * 104, 320))
  const stack = (narrow ?? (compact ? "scroll" : "stack")) === "stack"
  const pinFirst = stickyFirst ? pinCell : compact || stack ? undefined : phonePinCell
  const pinFirstHead = stickyFirst ? pinHead : compact || stack ? undefined : phonePinHead
  const pad = compact ? "px-2 first:pl-3 last:pr-3" : "first:pl-4 last:pr-4"
  const span = columns.length + (rowActions ? 1 : 0)
  const activate = (row: T) => {
    if (rowHref) router.push(rowHref(row))
    else onRowClick?.(row)
  }
  const exportCsv = () => {
    const head = columns.map(column => csvText(column.unit ? `${column.header} (${column.unit})` : column.header)).join(",")
    const body = rows.map(row => columns.map(column => csvText((column.csv ?? column.sortValue)?.(row))).join(","))
    const blob = new Blob(["\uFEFF" + [head, ...body].join("\r\n")], { type: "text/csv;charset=utf-8" })
    const url = URL.createObjectURL(blob)
    const link = Object.assign(document.createElement("a"), { href: url, download: `${download!.filename}.csv` })
    link.click()
    URL.revokeObjectURL(url)
  }
  const pageCount = table.getPageCount()
  return (
    <Table className={cn("@container/dt min-w-0", className)} size="md">
      {(title || toolbar || actions || download) && (
        <TableHeading title={title} className={cn("gap-2 py-2.5", compact ? "px-3" : "px-4")}>
          {toolbar && <div className="mr-auto flex min-w-0 flex-1 flex-wrap items-center gap-2">{toolbar}</div>}
          {actions}
          {download && <Button size="sm" variant="ghost" iconLeft={<Download />} onClick={exportCsv} disabled={!rows.length}>내려받기</Button>}
        </TableHeading>
      )}
      <TableContent label={label} className={cn("table-fixed", stack && stackTable)} style={width ? { minWidth: `${width}px` } : undefined}>
        <caption className="sr-only">{label}</caption>
        <colgroup>
          {columns.map(column => <col key={column.key} style={column.width ? { width: column.width } : undefined} />)}
          {rowActions && <col style={{ width: "3rem" }} />}
        </colgroup>
        <TableHeader className={cn(stack && !columns.some(column => column.sortValue) && stackHeadPlain, stack && "@max-[30rem]/dt:block")}>
          {table.getHeaderGroups().map(group => (
            <TableRow key={group.id} isHeader className={cn(stack && stackHeadRow)}>
              {group.headers.map((header, index) => {
                const column = columns[index]
                const direction = header.column.getIsSorted()
                return (
                  <TableHead
                    key={header.id}
                    align={column.numeric ? "right" : "left"}
                    sortable={header.column.getCanSort()}
                    sortDirection={direction || null}
                    onSort={() => header.column.toggleSorting()}
                    className={cn(pad, "[&>button]:max-w-full", column.numeric && "[&>button]:flex-row-reverse", index === 0 && pinFirstHead, stack && (header.column.getCanSort() ? stackHeadSortable : stackHeadPlain))}
                  >
                    <Clip>{column.header}{column.unit && <span className="font-normal"> ({column.unit})</span>}</Clip>
                  </TableHead>
                )
              })}
              {rowActions && <TableHead className={cn(pad, stack && stackHeadPlain)}><span id={`${id}-act`} className="sr-only">동작</span></TableHead>}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody className={cn(busy && "opacity-60 transition-opacity", stack && "@max-[30rem]/dt:block")}>
          {visible.length === 0 ? (
            <tr className={cn(stack && "@max-[30rem]/dt:block")}><td colSpan={span} className={cn("p-0", stack && "@max-[30rem]/dt:block")}>{empty ?? <EmptyState compact title="항목 없음" />}</td></tr>
          ) : visible.map((row, rowIndex) => {
            const key = row.id
            const cellId = `${id}-r${rowIndex}`
            return (
              <TableRow
                key={key}
                selected={selectedKey === key}
                tabIndex={onRowClick && !rowHref ? 0 : undefined}
                aria-selected={selectedKey != null ? selectedKey === key : undefined}
                onClick={clickable ? event => {
                  // Clicks in controls, and in portalled menus opened from the row, do not open the row.
                  const target = event.target as HTMLElement
                  if (!event.currentTarget.contains(target) || target.closest("a,button,input,label,select,textarea")) return
                  activate(row.original)
                } : undefined}
                onKeyDown={onRowClick && !rowHref ? event => {
                  if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return
                  event.preventDefault()
                  activate(row.original)
                } : undefined}
                className={cn(clickable && "cursor-pointer", stack && stackRow)}
              >
                {columns.map((column, index) => {
                  const content = column.cell(row.original)
                  const value = content == null || content === "" ? <Missing reason={column.missing?.(row.original) ?? column.missingReason} /> : content
                  return (
                    <TableCell key={column.key} id={index === 0 ? cellId : undefined} data-label={column.unit ? `${column.header} (${column.unit})` : column.header} align={column.numeric ? "right" : "left"} className={cn("max-w-0", pad, index === 0 && pinFirst, stack && (index === 0 ? stackTitleCell : stackCell))}>
                      {index === 0 && rowHref ? (
                        <Link href={rowHref(row.original)} className="block truncate font-medium text-foreground hover:underline">{value}</Link>
                      ) : (
                        <Clip className={cn(column.numeric && "tabular-nums")}>{value}</Clip>
                      )}
                    </TableCell>
                  )
                })}
                {rowActions && (
                  <TableCell align="right" data-label="동작" className={cn("py-0", pad, stack && stackCell)}>
                    <MoreMenu items={rowActions(row.original)} aria-labelledby={`${id}-act ${cellId}`} triggerClassName="size-7 border-transparent bg-transparent" />
                  </TableCell>
                )}
              </TableRow>
            )
          })}
        </TableBody>
      </TableContent>
      {paging?.mode === "offset" ? (
        <TableFooter className="px-4 py-2 text-xs text-muted-foreground">
          <span className="tabular-nums">{rows.length ? `${paging.offset + 1}-${paging.offset + rows.length}` : "0"}{paging.total != null && ` / ${paging.total}`}</span>
          <div className="flex items-center gap-1">
            <Button size="sm" variant="ghost" disabled={paging.offset === 0} onClick={() => paging.onOffsetChange(Math.max(0, paging.offset - paging.limit))}>이전</Button>
            <Button size="sm" variant="ghost" disabled={paging.total != null ? paging.offset + rows.length >= paging.total : rows.length < paging.limit} onClick={() => paging.onOffsetChange(paging.offset + paging.limit)}>다음</Button>
          </div>
        </TableFooter>
      ) : paging?.mode === "cursor" ? (
        <TableFooter className="justify-end px-4 py-2">
          <div className="flex items-center gap-1">
            <Button size="sm" variant="ghost" disabled={!paging.hasPrevious} onClick={paging.onPrevious}>이전</Button>
            <Button size="sm" variant="ghost" disabled={!paging.hasNext} onClick={paging.onNext}>다음</Button>
          </div>
        </TableFooter>
      ) : clientPaging && pageCount > 1 ? (
        <TableFooter className="px-4 py-2 text-xs text-muted-foreground">
          <span className="tabular-nums">{`${pagination.pageIndex * pagination.pageSize + 1}-${Math.min((pagination.pageIndex + 1) * pagination.pageSize, rows.length)} / ${rows.length}`}</span>
          <Pagination className="mx-0 w-auto md:justify-end" currentPage={pagination.pageIndex + 1} totalPages={pageCount} siblingCount={1} onPageChange={page => table.setPageIndex(Math.min(Math.max(page, 1), pageCount) - 1)} />
        </TableFooter>
      ) : null}
    </Table>
  )
}

export type MatrixColumn = { key: string; label: string; /** Tooltip on the column header, e.g. the full name. */ hint?: string }
export type MatrixRow = { key: string; label: React.ReactNode; /** By column key; absent cells show "—". */ cells: Record<string, React.ReactNode> }
export type MatrixGroup = { key: string; /** Group heading row; omit for an ungrouped block. */ label?: React.ReactNode; rows: MatrixRow[] }

/** Rows by columns with optional row groups and a row-header column pinned while the rest scrolls. */
export function Matrix({ label, rowHeader, columns, groups, legend, empty, missingReason }: {
  label: string
  /** Header of the row-header column. */ rowHeader: string
  columns: MatrixColumn[]
  groups: MatrixGroup[]
  /** Shown above the grid, e.g. a list of StatusDot entries. */ legend?: React.ReactNode
  empty?: React.ReactNode
  missingReason?: string
}) {
  return (
    <Table className="min-w-0">
      {legend && <TableHeading className="gap-2 px-4 py-2.5">{legend}</TableHeading>}
      <TableContent label={label} className="table-fixed" style={{ minWidth: `${8 + columns.length * 7}rem` }}>
        <caption className="sr-only">{label}</caption>
        <colgroup><col className="w-28 sm:w-40" />{columns.map(column => <col key={column.key} />)}</colgroup>
        <TableHeader>
          <TableRow isHeader>
            <TableHead className={cn(pinHead, "pl-4")}>{rowHeader}</TableHead>
            {columns.map(column => <TableHead key={column.key}>{column.hint ? <Hint text={column.hint} focusable>{column.label}</Hint> : <Clip>{column.label}</Clip>}</TableHead>)}
          </TableRow>
        </TableHeader>
        {groups.length === 0 ? (
          <TableBody><tr><td colSpan={columns.length + 1} className="p-0">{empty ?? <EmptyState compact title="항목 없음" />}</td></tr></TableBody>
        ) : groups.map(group => (
          <TableBody key={group.key}>
            {group.label != null && (
              <TableRow className="bg-muted/40 hover:bg-muted/40">
                <TableHead scope="rowgroup" colSpan={columns.length + 1} className="pl-4 text-sm text-foreground"><span className="sticky left-4">{group.label}</span></TableHead>
              </TableRow>
            )}
            {group.rows.map(row => (
              <TableRow key={row.key}>
                <TableHead scope="row" className={cn(pinCell, "max-w-0 pl-4 text-sm font-normal text-foreground")}><Clip>{row.label}</Clip></TableHead>
                {columns.map(column => <TableCell key={column.key}>{row.cells[column.key] ?? <Missing reason={missingReason} />}</TableCell>)}
              </TableRow>
            ))}
          </TableBody>
        ))}
      </TableContent>
    </Table>
  )
}

/** A pressable matrix cell; `label` is its full accessible name. */
export function MatrixCell({ label, onClick, dim = false, children }: { label: string; onClick: () => void; /** Fades cells outside the current filter. */ dim?: boolean; children: React.ReactNode }) {
  return <Button variant="ghost" size="sm" className={cn("-ml-2 max-w-full tabular-nums", dim && "opacity-40")} aria-label={label} onClick={onClick}>{children}</Button>
}
