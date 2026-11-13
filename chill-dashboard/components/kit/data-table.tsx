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
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/custom/blocks/TooltipBlock"
import { Missing } from "./display"
import { EmptyState } from "./query"

const features = tableFeatures({ rowPaginationFeature, rowSortingFeature, sortFns, paginatedRowModel: createPaginatedRowModel(), sortedRowModel: createSortedRowModel() })

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
  className?: string
}

/** One line per cell; the full text appears in a tooltip only when it is cut off. */
function Clip({ children, numeric }: { children: React.ReactNode; numeric?: boolean }) {
  const [full, setFull] = React.useState<string | null>(null)
  const measure = (element: HTMLElement) => setFull(element.scrollWidth > element.clientWidth ? element.textContent : null)
  return (
    <Tooltip>
      <TooltipTrigger
        className={cn("block min-w-0 truncate", numeric && "tabular-nums")}
        onMouseEnter={event => measure(event.currentTarget)}
        onFocus={event => measure(event.currentTarget)}
      >
        {children}
      </TooltipTrigger>
      {full && <TooltipContent className="max-w-sm whitespace-normal break-all">{full}</TooltipContent>}
    </Tooltip>
  )
}

function csvText(value: Scalar) {
  if (value == null) return ""
  const text = String(value)
  return /[",\n\r]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text
}

/** Sortable, paged table with Avara table markup and TanStack table state. */
export function DataTable<T extends RowData>({ label, rows, columns, rowKey, title, toolbar, actions, rowHref, onRowClick, selectedKey, defaultSort, sort, onSortChange, pageSize = 25, paging, download, empty, busy, className }: DataTableProps<T>) {
  const router = useRouter()
  const [localSorting, setLocalSorting] = React.useState<SortingState>(defaultSort ? [{ id: defaultSort.key, desc: defaultSort.desc }] : [])
  const [pagination, setPagination] = React.useState<PaginationState>({ pageIndex: 0, pageSize: pageSize || Math.max(rows.length, 1) })
  const controlled = sort !== undefined
  const sorting: SortingState = controlled ? (sort ? [{ id: sort.key, desc: sort.desc }] : []) : localSorting
  const helper = createColumnHelper<typeof features, T>()
  const table = useTable({
    features,
    data: rows,
    columns: helper.columns(columns.map(column => helper.accessor(row => column.sortValue?.(row) ?? undefined, { id: column.key, header: column.header, enableSorting: !!column.sortValue, sortUndefined: "last" }))),
    getRowId: row => rowKey(row),
    manualPagination: !!paging || pageSize === 0,
    enableMultiSort: false,
    manualSorting: !!paging,
    onSortingChange: update => {
      const next = typeof update === "function" ? update(sorting) : update
      const value = next[0] ? { key: next[0].id, desc: next[0].desc } : null
      if (controlled) onSortChange?.(value)
      else { setLocalSorting(next); onSortChange?.(value) }
      setPagination(current => ({ ...current, pageIndex: 0 }))
    },
    onPaginationChange: update => setPagination(current => (typeof update === "function" ? update(current) : update)),
  })
  const visible = table.getRowModel().rows
  const clickable = !!rowHref || !!onRowClick
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
    <Table className={cn("min-w-0", className)} size="md">
      {(title || toolbar || actions || download) && (
        <TableHeading title={title} className="gap-2 px-3 py-2.5">
          {toolbar && <div className="mr-auto flex min-w-0 flex-1 flex-wrap items-center gap-2">{toolbar}</div>}
          {actions}
          {download && <Button size="sm" variant="ghost" iconLeft={<Download />} onClick={exportCsv} disabled={!rows.length}>내려받기</Button>}
        </TableHeading>
      )}
      <TableContent label={label} className="table-fixed" style={{ minWidth: `${Math.max(columns.length * 112, 320)}px` }}>
        <caption className="sr-only">{label}</caption>
        <colgroup>{columns.map(column => <col key={column.key} style={column.width ? { width: column.width } : undefined} />)}</colgroup>
        <TableHeader>
          {table.getHeaderGroups().map(group => (
            <TableRow key={group.id} isHeader>
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
                    className={cn(column.numeric && "[&>button]:flex-row-reverse")}
                  >
                    <span>{column.header}{column.unit && <span className="font-normal"> ({column.unit})</span>}</span>
                  </TableHead>
                )
              })}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody className={cn(busy && "opacity-60 transition-opacity")}>
          {visible.length === 0 ? (
            <tr><td colSpan={columns.length} className="p-0">{empty ?? <EmptyState compact title="항목 없음" />}</td></tr>
          ) : visible.map(row => {
            const key = row.id
            return (
              <TableRow
                key={key}
                selected={selectedKey === key}
                tabIndex={onRowClick && !rowHref ? 0 : undefined}
                aria-selected={selectedKey != null ? selectedKey === key : undefined}
                onClick={clickable ? event => {
                  if ((event.target as HTMLElement).closest("a,button,input,label,select,textarea")) return
                  activate(row.original)
                } : undefined}
                onKeyDown={onRowClick && !rowHref ? event => {
                  if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return
                  event.preventDefault()
                  activate(row.original)
                } : undefined}
                className={cn(clickable && "cursor-pointer")}
              >
                {columns.map((column, index) => {
                  const content = column.cell(row.original)
                  const value = content == null || content === "" ? <Missing reason={column.missingReason} /> : content
                  return (
                    <TableCell key={column.key} align={column.numeric ? "right" : "left"} className="max-w-0">
                      {index === 0 && rowHref ? (
                        <Link href={rowHref(row.original)} className="block truncate font-medium text-foreground hover:underline">{value}</Link>
                      ) : (
                        <Clip numeric={column.numeric}>{value}</Clip>
                      )}
                    </TableCell>
                  )
                })}
              </TableRow>
            )
          })}
        </TableBody>
      </TableContent>
      {paging?.mode === "offset" ? (
        <TableFooter className="px-3 py-2 text-xs text-muted-foreground">
          <span className="tabular-nums">{rows.length ? `${paging.offset + 1}-${paging.offset + rows.length}` : "0"}{paging.total != null && ` / ${paging.total}`}</span>
          <div className="flex items-center gap-1">
            <Button size="sm" variant="ghost" disabled={paging.offset === 0} onClick={() => paging.onOffsetChange(Math.max(0, paging.offset - paging.limit))}>이전</Button>
            <Button size="sm" variant="ghost" disabled={paging.total != null ? paging.offset + rows.length >= paging.total : rows.length < paging.limit} onClick={() => paging.onOffsetChange(paging.offset + paging.limit)}>다음</Button>
          </div>
        </TableFooter>
      ) : paging?.mode === "cursor" ? (
        <TableFooter className="justify-end px-3 py-2">
          <div className="flex items-center gap-1">
            <Button size="sm" variant="ghost" disabled={!paging.hasPrevious} onClick={paging.onPrevious}>이전</Button>
            <Button size="sm" variant="ghost" disabled={!paging.hasNext} onClick={paging.onNext}>다음</Button>
          </div>
        </TableFooter>
      ) : pageSize > 0 && pageCount > 1 ? (
        <TableFooter className="px-3 py-2 text-xs text-muted-foreground">
          <span className="tabular-nums">{`${pagination.pageIndex * pagination.pageSize + 1}-${Math.min((pagination.pageIndex + 1) * pagination.pageSize, rows.length)} / ${rows.length}`}</span>
          <Pagination className="mx-0 w-auto md:justify-end" currentPage={pagination.pageIndex + 1} totalPages={pageCount} siblingCount={1} onPageChange={page => table.setPageIndex(Math.min(Math.max(page, 1), pageCount) - 1)} />
        </TableFooter>
      ) : null}
    </Table>
  )
}
