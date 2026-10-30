"use client"

// Fork of shadcn-ui/ui dashboard-01 data-table; no demo records or drag/reorder.
import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { createColumnHelper, createPaginatedRowModel, createSortedRowModel, FlexRender, rowPaginationFeature, rowSortingFeature, sortFns, tableFeatures, useTable, type RowData, type SortingState, type PaginationState } from "@tanstack/react-table"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ArrowDown, ArrowUp, ArrowUpDown, ChevronLeft, ChevronRight, Search } from "lucide-react"
import { DetailsDialog } from "@/components/read-region"
import { stringifySchedulerJSON } from "@/lib/scheduler"

const features = tableFeatures({rowPaginationFeature,rowSortingFeature,sortFns,paginatedRowModel:createPaginatedRowModel(),sortedRowModel:createSortedRowModel()})
export type DataColumn<T> = {key:string;label:string;value:(row:T)=>React.ReactNode;sortValue?:(row:T)=>string|number|bigint|undefined}
type ServerTable = {pagination:PaginationState;sorting:SortingState;total:number;busy:boolean;onPaginationChange:(value:PaginationState)=>void;onSortingChange:(value:SortingState)=>void}
export function useServerTable() {
  const params=useSearchParams(),router=useRouter()
  const pageSize=[10,25,50].includes(Number(params.get("limit")))?Number(params.get("limit")):25
  const requestedPage=Number(params.get("page"))
  const pagination={pageIndex:Number.isSafeInteger(requestedPage)&&requestedPage>0?requestedPage-1:0,pageSize}
  const sorting:SortingState=params.get("sort")?[{id:params.get("sort")!,desc:params.get("order")==="desc"}]:[{id:"created",desc:true}]
  function update(patch:Record<string,string|undefined>) {const next=new URLSearchParams(params.toString());next.delete("offset");for(const [key,value] of Object.entries(patch)){if(value)next.set(key,value);else next.delete(key)}router.replace(`?${next}`,{scroll:false})}
  const query=new URLSearchParams({limit:String(pageSize),offset:String(pagination.pageIndex*pageSize)})
  if(sorting[0]) {query.set("sort",sorting[0].id);query.set("order",sorting[0].desc?"desc":"asc")}
  return {pagination,sorting,query,
    onPaginationChange:(value:PaginationState)=>update({page:String(value.pageIndex+1),limit:String(value.pageSize)}),
    onSortingChange:(value:SortingState)=>update({page:undefined,sort:value[0]?.id,order:value[0]?(value[0].desc?"desc":"asc"):undefined})}
}
export function DataTable<T extends RowData>({data,columns,searchable=true,server}: {data:T[];columns:DataColumn<T>[];searchable?:boolean;server?:ServerTable}) {
  const [search,setSearch] = React.useState("")
  const [localSorting,setSorting] = React.useState<SortingState>(()=>columns.some(column=>column.key==="created")?[{id:"created",desc:true}]:[])
  const [localPagination,setPagination] = React.useState({pageIndex:0,pageSize:25})
  const sorting=server?.sorting??localSorting,pagination=server?.pagination??localPagination
  const id = React.useId()
  const filtered = React.useMemo(() => !searchable||server?data:data.filter(row => !search || stringifySchedulerJSON(row).toLocaleLowerCase().includes(search.toLocaleLowerCase())),[data,search,searchable,server])
  const helper = createColumnHelper<typeof features,T>()
  const table = useTable({features,data:filtered,columns:helper.columns(columns.map(column => helper.accessor(row => column.sortValue?.(row),{id:column.key,header:column.label,enableSorting:!!column.sortValue,sortUndefined:"last"}))),manualSorting:!!server,manualPagination:!!server,rowCount:server?.total,enableMultiSort:false,enableSortingRemoval:false,state:{sorting,pagination},onSortingChange:update=>{const value=typeof update==="function"?update(sorting):update;if(server)server.onSortingChange(value);else {setSorting(value);setPagination(current=>({...current,pageIndex:0}))}},onPaginationChange:update=>{const value=typeof update==="function"?update(pagination):update;if(server)server.onPaginationChange(value);else setPagination(value)}})
  const total=server?.total??filtered.length,pageCount=Math.max(1,table.getPageCount())
  return <div className="min-w-0 space-y-3" aria-busy={server?.busy}>
    {searchable&&!server&&data.length > 1 && <label htmlFor={id} className="relative block max-w-sm"><span className="sr-only">목록 검색</span><Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" /><Input id={id} className="min-h-11 pl-9" placeholder="이름이나 내용으로 찾기" value={search} onChange={event => {setSearch(event.target.value);setPagination(current => ({...current,pageIndex:0}))}} /></label>}
    <div className="overflow-x-auto rounded-xl border"><Table><TableHeader className="bg-muted/70">{table.getHeaderGroups().map(group => <TableRow key={group.id} className="hover:bg-transparent">{group.headers.map(header => <TableHead key={header.id} aria-sort={header.column.getIsSorted()==="asc"?"ascending":header.column.getIsSorted()==="desc"?"descending":header.column.getCanSort()?"none":undefined} className="h-11 px-4 text-[0.8125rem] font-semibold text-muted-foreground">{header.column.getCanSort() ? <button type="button" disabled={server?.busy} className="inline-flex min-h-11 items-center gap-1.5 hover:text-foreground disabled:opacity-50" onClick={header.column.getToggleSortingHandler()} aria-label={`${header.column.columnDef.header} 정렬`}><FlexRender header={header} />{header.column.getIsSorted() === "asc" ? <ArrowUp className="size-3.5" aria-hidden="true" /> : header.column.getIsSorted() === "desc" ? <ArrowDown className="size-3.5" aria-hidden="true" /> : <ArrowUpDown className="size-3.5 opacity-60" aria-hidden="true" />}</button> : <FlexRender header={header} />}</TableHead>)}</TableRow>)}</TableHeader><TableBody>{table.getRowModel().rows.length ? table.getRowModel().rows.map(row => <TableRow key={row.id}>{row.getAllCells().map((cell, index) => <TableCell key={cell.id} className="px-4 py-3 align-top whitespace-normal [&_a]:inline-flex [&_a]:min-h-11 [&_a]:items-center md:[&_a]:min-h-6">{columns[index].value(row.original)}</TableCell>)}</TableRow>) : <TableRow className="hover:bg-transparent"><TableCell colSpan={columns.length} className="px-4 py-8 text-center text-muted-foreground">표시할 항목이 없어요. 필터를 확인하세요.</TableCell></TableRow>}</TableBody></Table></div>
    <nav aria-label="목록 페이지" className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground"><span role="status" className="tabular-nums">전체 {total}개</span><label className="flex items-center gap-2">한 번에 보기 <select className="min-h-11 rounded-md border bg-background px-2" disabled={server?.busy} value={pagination.pageSize} onChange={event => table.setPageSize(Number(event.target.value))}>{[10,25,50].map(size => <option key={size} value={size}>{size}개</option>)}</select></label><span className="ml-auto flex items-center gap-1"><Button className="min-h-11 min-w-11" variant="ghost" size="icon" aria-label="이전 목록" disabled={server?.busy||!table.getCanPreviousPage()} onClick={() => table.previousPage()}><ChevronLeft /></Button><span className="min-w-14 text-center tabular-nums text-foreground">{pagination.pageIndex+1} / {pageCount}</span><Button className="min-h-11 min-w-11" variant="ghost" size="icon" aria-label="다음 목록" disabled={server?.busy||!table.getCanNextPage()} onClick={() => table.nextPage()}><ChevronRight /></Button></span><DetailsDialog label="목록 안내" title="목록 조회 범위"><p>{server?"필터와 정렬은 서버 전체 자료에 적용한 뒤 페이지로 나누어요.":"검색과 정렬은 이 저장 영역에서 조회한 전체 자료에 적용한 뒤 페이지로 나누어요."}</p></DetailsDialog></nav>
  </div>
}
