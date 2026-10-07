"use client"

// Fork of shadcn-ui/ui dashboard-01 data-table; no demo records or drag/reorder.
import * as React from "react"
import { createColumnHelper, createPaginatedRowModel, createSortedRowModel, FlexRender, rowPaginationFeature, rowSortingFeature, tableFeatures, useTable, type RowData, type SortingState } from "@tanstack/react-table"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { DetailsDialog } from "@/components/read-region"

const features = tableFeatures({rowPaginationFeature,rowSortingFeature,paginatedRowModel:createPaginatedRowModel(),sortedRowModel:createSortedRowModel()})
export type DataColumn<T> = {key:string;label:string;value:(row:T)=>React.ReactNode;sortValue?:(row:T)=>string|number}
export function DataTable<T extends RowData>({data,columns}: {data:T[];columns:DataColumn<T>[]}) {
  const [search,setSearch] = React.useState("")
  const [sorting,setSorting] = React.useState<SortingState>([])
  const [pagination,setPagination] = React.useState({pageIndex:0,pageSize:10})
  const id = React.useId()
  const filtered = React.useMemo(() => data.filter(row => !search || JSON.stringify(row).toLocaleLowerCase().includes(search.toLocaleLowerCase())),[data,search])
  const helper = createColumnHelper<typeof features,T>()
  const table = useTable({features,data:filtered,columns:helper.columns(columns.map(column => helper.accessor(row => column.sortValue?.(row) ?? String(column.value(row)),{id:column.key,header:column.label,cell:({row}) => column.value(row.original),enableSorting:!!column.sortValue}))),state:{sorting,pagination},onSortingChange:setSorting,onPaginationChange:setPagination})
  return <div className="min-w-0 space-y-3">
    {data.length > 1 && <label htmlFor={id} className="grid gap-2 text-sm">목록 검색<Input id={id} placeholder="이름이나 내용으로 찾기" value={search} onChange={event => {setSearch(event.target.value);setPagination(current => ({...current,pageIndex:0}))}} /></label>}
    <div className="rounded-lg border overflow-x-auto"><Table><TableHeader>{table.getHeaderGroups().map(group => <TableRow key={group.id}>{group.headers.map(header => <TableHead key={header.id}>{header.column.getCanSort() ? <button type="button" onClick={header.column.getToggleSortingHandler()} aria-label={`${header.column.columnDef.header} 정렬`}><FlexRender header={header} /> {header.column.getIsSorted() === "asc" ? "↑" : header.column.getIsSorted() === "desc" ? "↓" : "↕"}</button> : <FlexRender header={header} />}</TableHead>)}</TableRow>)}</TableHeader><TableBody>{table.getRowModel().rows.length ? table.getRowModel().rows.map(row => <TableRow key={row.id}>{row.getAllCells().map(cell => <TableCell key={cell.id}><FlexRender cell={cell} /></TableCell>)}</TableRow>) : <TableRow><TableCell colSpan={columns.length}>데이터 없음</TableCell></TableRow>}</TableBody></Table></div>
    <div className="toolbar text-sm text-muted-foreground"><span>{filtered.length}개</span>{table.getPageCount() > 1 && <><span>{pagination.pageIndex+1} / {table.getPageCount()}</span><label>한 번에 보기 <select value={pagination.pageSize} onChange={event => table.setPageSize(Number(event.target.value))}>{[10,25,50,100,200].map(size => <option key={size}>{size}</option>)}</select></label><Button variant="outline" disabled={!table.getCanPreviousPage()} onClick={() => table.previousPage()}>이전 목록</Button><Button variant="outline" disabled={!table.getCanNextPage()} onClick={() => table.nextPage()}>다음 목록</Button></>}<DetailsDialog label="목록 안내" title="목록 조회 범위"><p>검색과 정렬은 이번에 조회한 자료 안에서만 이루어져요. 다른 구간은 조회 조건의 다음 버튼으로 불러올 수 있어요.</p></DetailsDialog></div>
  </div>
}
