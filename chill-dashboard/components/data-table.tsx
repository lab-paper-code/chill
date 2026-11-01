"use client"

// Fork of shadcn-ui/ui dashboard-01 data-table; no demo records or drag/reorder.
import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { createColumnHelper, createPaginatedRowModel, createSortedRowModel, rowPaginationFeature, rowSortingFeature, sortFns, tableFeatures, useTable, type RowData, type SortingState, type PaginationState } from "@tanstack/react-table"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ArrowDown, ArrowUp, ArrowUpDown, ChevronLeft, ChevronRight, Search } from "lucide-react"
import { DetailsDialog } from "@/components/read-region"
import { stringifySchedulerJSON } from "@/lib/scheduler"
import { StatusCell } from "@/components/status-cell"
import type { StatusValue } from "@/lib/status"

const features = tableFeatures({rowPaginationFeature,rowSortingFeature,sortFns,paginatedRowModel:createPaginatedRowModel(),sortedRowModel:createSortedRowModel()})
export type DataColumn<T> = {key:string;label:string;unit?:string;numeric?:boolean;headerStatus?:StatusValue;value:(row:T)=>React.ReactNode;sortValue?:(row:T)=>string|number|bigint|undefined}
type ServerTable = {pagination:PaginationState;sorting:SortingState;total:number;busy:boolean;onPaginationChange:(value:PaginationState)=>void;onSortingChange:(value:SortingState)=>void}
export function useServerTable(defaultSorting:SortingState=[{id:"created",desc:true}]) {
  const params=useSearchParams(),router=useRouter()
  const pageSize=[10,25,50].includes(Number(params.get("limit")))?Number(params.get("limit")):25
  const requestedPage=Number(params.get("page"))
  const pagination={pageIndex:Number.isSafeInteger(requestedPage)&&requestedPage>0?requestedPage-1:0,pageSize}
  const sorting:SortingState=params.get("sort")?[{id:params.get("sort")!,desc:params.get("order")==="desc"}]:defaultSorting
  function update(patch:Record<string,string|undefined>) {const next=new URLSearchParams(params.toString());next.delete("offset");for(const [key,value] of Object.entries(patch)){if(value)next.set(key,value);else next.delete(key)}router.replace(`?${next}`,{scroll:false})}
  const query=new URLSearchParams({limit:String(pageSize),offset:String(pagination.pageIndex*pageSize)})
  if(sorting[0]) {query.set("sort",sorting[0].id);query.set("order",sorting[0].desc?"desc":"asc")}
  return {pagination,sorting,query,
    onPaginationChange:(value:PaginationState)=>update({page:String(value.pageIndex+1),limit:String(value.pageSize)}),
    onSortingChange:(value:SortingState)=>update({page:undefined,sort:value[0]?.id,order:value[0]?(value[0].desc?"desc":"asc"):undefined})}
}
export function DataTable<T extends RowData>({data,columns,searchable=true,server}: {data:T[];columns:DataColumn<T>[];searchable?:boolean;server?:ServerTable}) {
  const params=useSearchParams(),router=useRouter()
  const local=useServerTable(columns.some(column=>column.key==="created")?[{id:"created",desc:true}]:[])
  const search=params.get("search")??""
  function setSearch(value:string){const next=new URLSearchParams(params.toString());if(value)next.set("search",value);else next.delete("search");next.delete("page");router.replace(`?${next}`,{scroll:false})}
  const controls=server??local,sorting=controls.sorting,pagination=controls.pagination
  const id = React.useId()
  const filtered = !searchable||server?data:data.filter(row => !search || stringifySchedulerJSON(row).toLocaleLowerCase().includes(search.toLocaleLowerCase()))
  const helper = createColumnHelper<typeof features,T>()
  const table = useTable({features,data:filtered,columns:helper.columns(columns.map(column => helper.accessor(row => column.sortValue?.(row),{id:column.key,header:column.label,enableSorting:!!column.sortValue,sortUndefined:"last"}))),manualSorting:!!server,manualPagination:!!server,rowCount:server?.total,enableMultiSort:false,enableSortingRemoval:false,state:{sorting,pagination},onSortingChange:update=>controls.onSortingChange(typeof update==="function"?update(sorting):update),onPaginationChange:update=>controls.onPaginationChange(typeof update==="function"?update(pagination):update)})
  const total=server?.total??filtered.length,pageCount=Math.max(1,table.getPageCount())
  return <div className="min-w-0 space-y-3" aria-busy={server?.busy}>
    {searchable&&!server&&data.length > 1 && <label htmlFor={id} className="relative block max-w-sm"><span className="sr-only">목록 검색</span><Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" /><Input id={id} className="min-h-11 pl-9" placeholder="이름이나 내용으로 찾기" value={search} onChange={event => setSearch(event.target.value)} /></label>}
    {columns.some(column=>column.sortValue)&&<label className="flex items-center gap-2 text-sm sm:hidden">정렬<select className="min-h-11 min-w-0 rounded-md border bg-background px-2" value={sorting[0]?`${sorting[0].id}:${sorting[0].desc?"desc":"asc"}`:""} onChange={event=>{const [id,order]=event.target.value.split(":");controls.onSortingChange([{id,desc:order==="desc"}])}}><option value="" disabled>선택</option>{columns.filter(column=>column.sortValue).flatMap(column=>["asc","desc"].map(order=><option key={`${column.key}:${order}`} value={`${column.key}:${order}`}>{column.label} · {order==="asc"?"오름차순":"내림차순"}</option>))}</select></label>}
    <div className="overflow-x-auto rounded-xl border"><Table className="table-fixed max-sm:block"><TableHeader className="bg-muted/70 max-sm:hidden">{table.getHeaderGroups().map(group => <TableRow key={group.id} className="hover:bg-transparent">{group.headers.map(header => {
      const column=columns.find(column=>column.key===header.column.id)!
      const label=<>{column.label}{column.unit&&` (${column.unit})`}</>
      return <TableHead key={header.id} aria-sort={header.column.getIsSorted()==="asc"?"ascending":header.column.getIsSorted()==="desc"?"descending":header.column.getCanSort()?"none":undefined} className={`h-11 px-4 text-sm font-semibold whitespace-normal text-muted-foreground ${column.numeric?"text-right tabular-nums":"text-left [overflow-wrap:anywhere]"}`}>
        {header.column.getCanSort() ? <button type="button" disabled={server?.busy} className="inline-flex min-h-11 items-center gap-1.5 hover:text-foreground disabled:opacity-50" onClick={header.column.getToggleSortingHandler()} aria-label={`${column.label} 정렬`}>{label}{header.column.getIsSorted() === "asc" ? <ArrowUp className="size-3.5" aria-hidden="true" /> : header.column.getIsSorted() === "desc" ? <ArrowDown className="size-3.5" aria-hidden="true" /> : <ArrowUpDown className="size-3.5 opacity-60" aria-hidden="true" />}</button> : label}
        {column.headerStatus&&<div className="py-1"><StatusCell {...column.headerStatus}/></div>}
      </TableHead>
    })}</TableRow>)}</TableHeader><TableBody className="max-sm:block">{table.getRowModel().rows.length ? table.getRowModel().rows.map(row => <TableRow key={row.id} className="max-sm:block max-sm:border-b-2">{row.getAllCells().map((cell, index) => <TableCell key={cell.id} data-label={`${columns[index].label}${columns[index].unit?` (${columns[index].unit})`:""}`} className={`px-4 py-3 align-top whitespace-normal max-sm:grid max-sm:grid-cols-[minmax(6rem,.4fr)_minmax(0,1fr)] max-sm:gap-3 max-sm:before:content-[attr(data-label)] max-sm:before:text-left max-sm:before:font-medium [&_a]:inline-flex [&_a]:min-h-11 [&_a]:max-w-full [&_a]:items-center md:[&_a]:min-h-6 ${columns[index].numeric?"text-right tabular-nums":"text-left [overflow-wrap:anywhere]"}`}><div className="min-w-0">{columns[index].value(row.original)}</div></TableCell>)}</TableRow>) : <TableRow className="hover:bg-transparent"><TableCell colSpan={columns.length} className="px-4 py-8 text-center text-muted-foreground">표시할 항목이 없어요. 필터를 확인하세요.</TableCell></TableRow>}</TableBody></Table></div>
    {total>0&&<nav aria-label="목록 페이지" className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground"><span role="status" className="tabular-nums">전체 {total}개</span><label className="flex items-center gap-2">한 번에 보기 <select className="min-h-11 rounded-md border bg-background px-2" disabled={server?.busy} value={pagination.pageSize} onChange={event => table.setPageSize(Number(event.target.value))}>{[10,25,50].map(size => <option key={size} value={size}>{size}개</option>)}</select></label><span className="ml-auto flex items-center gap-1"><Button className="min-h-11 min-w-11" variant="ghost" size="icon" aria-label="이전 목록" disabled={server?.busy||!table.getCanPreviousPage()} onClick={() => table.previousPage()}><ChevronLeft /></Button><span className="min-w-14 text-center tabular-nums text-foreground">{pagination.pageIndex+1} / {pageCount}</span><Button className="min-h-11 min-w-11" variant="ghost" size="icon" aria-label="다음 목록" disabled={server?.busy||!table.getCanNextPage()} onClick={() => table.nextPage()}><ChevronRight /></Button></span><DetailsDialog label="목록 안내" title="목록 조회 범위"><p>{server?"필터와 정렬은 서버 전체 자료에 적용한 뒤 페이지로 나누어요.":"검색과 정렬은 이 저장 영역에서 조회한 전체 자료에 적용한 뒤 페이지로 나누어요."}</p></DetailsDialog></nav>}
  </div>
}
