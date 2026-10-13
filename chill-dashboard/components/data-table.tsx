"use client"

// Fork of shadcn-ui/ui dashboard-01 data-table; no demo records or drag/reorder.
import * as React from "react"
import { createColumnHelper, createPaginatedRowModel, createSortedRowModel, FlexRender, rowPaginationFeature, rowSortingFeature, tableFeatures, useTable, type RowData, type SortingState } from "@tanstack/react-table"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ArrowDown, ArrowUp, ArrowUpDown, ChevronLeft, ChevronRight, Search } from "lucide-react"
import { DetailsDialog } from "@/components/read-region"
import { stringifySchedulerJSON } from "@/lib/scheduler"

const features = tableFeatures({rowPaginationFeature,rowSortingFeature,paginatedRowModel:createPaginatedRowModel(),sortedRowModel:createSortedRowModel()})
export type DataColumn<T> = {key:string;label:string;value:(row:T)=>React.ReactNode;sortValue?:(row:T)=>string|number}
export function DataTable<T extends RowData>({data,columns}: {data:T[];columns:DataColumn<T>[]}) {
  const [search,setSearch] = React.useState("")
  const [sorting,setSorting] = React.useState<SortingState>([])
  const [pagination,setPagination] = React.useState({pageIndex:0,pageSize:10})
  const id = React.useId()
  const filtered = React.useMemo(() => data.filter(row => !search || stringifySchedulerJSON(row).toLocaleLowerCase().includes(search.toLocaleLowerCase())),[data,search])
  const helper = createColumnHelper<typeof features,T>()
  const table = useTable({features,data:filtered,columns:helper.columns(columns.map(column => helper.accessor(row => column.sortValue?.(row) ?? String(column.value(row)),{id:column.key,header:column.label,enableSorting:!!column.sortValue}))),state:{sorting,pagination},onSortingChange:setSorting,onPaginationChange:setPagination})
  return <div className="min-w-0 space-y-3">
    {data.length > 1 && <label htmlFor={id} className="relative block max-w-sm"><span className="sr-only">목록 검색</span><Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" /><Input id={id} className="pl-9" placeholder="이름이나 내용으로 찾기" value={search} onChange={event => {setSearch(event.target.value);setPagination(current => ({...current,pageIndex:0}))}} /></label>}
    <div className="overflow-x-auto rounded-xl border"><Table><TableHeader className="bg-muted/70">{table.getHeaderGroups().map(group => <TableRow key={group.id} className="hover:bg-transparent">{group.headers.map(header => <TableHead key={header.id} className="h-11 px-4 text-[0.8125rem] font-semibold text-muted-foreground">{header.column.getCanSort() ? <button type="button" className="inline-flex items-center gap-1.5 hover:text-foreground" onClick={header.column.getToggleSortingHandler()} aria-label={`${header.column.columnDef.header} 정렬`}><FlexRender header={header} />{header.column.getIsSorted() === "asc" ? <ArrowUp className="size-3.5" aria-hidden="true" /> : header.column.getIsSorted() === "desc" ? <ArrowDown className="size-3.5" aria-hidden="true" /> : <ArrowUpDown className="size-3.5 opacity-60" aria-hidden="true" />}</button> : <FlexRender header={header} />}</TableHead>)}</TableRow>)}</TableHeader><TableBody>{table.getRowModel().rows.length ? table.getRowModel().rows.map(row => <TableRow key={row.id}>{row.getAllCells().map((cell, index) => <TableCell key={cell.id} className="px-4 py-3 align-top whitespace-normal">{columns[index].value(row.original)}</TableCell>)}</TableRow>) : <TableRow className="hover:bg-transparent"><TableCell colSpan={columns.length} className="px-4 py-8 text-center text-muted-foreground">표시할 항목이 없어요</TableCell></TableRow>}</TableBody></Table></div>
    <div className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground"><span>{filtered.length}개</span>{table.getPageCount() > 1 && <><label className="flex items-center gap-2">한 번에 보기 <select value={pagination.pageSize} onChange={event => table.setPageSize(Number(event.target.value))}>{[10,25,50,100,200].map(size => <option key={size}>{size}</option>)}</select></label><span className="ml-auto flex items-center gap-1"><Button variant="ghost" size="icon" aria-label="이전 목록" disabled={!table.getCanPreviousPage()} onClick={() => table.previousPage()}><ChevronLeft /></Button><span className="min-w-14 text-center text-foreground">{pagination.pageIndex+1} / {table.getPageCount()}</span><Button variant="ghost" size="icon" aria-label="다음 목록" disabled={!table.getCanNextPage()} onClick={() => table.nextPage()}><ChevronRight /></Button></span></>}<DetailsDialog label="목록 안내" title="목록 조회 범위"><p>검색과 정렬은 이번에 조회한 자료 안에서만 이루어져요. 다른 구간은 조회 조건의 다음 버튼으로 불러올 수 있어요.</p></DetailsDialog></div>
  </div>
}
