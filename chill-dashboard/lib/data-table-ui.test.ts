import assert from "node:assert/strict"
import test from "node:test"
import {readFileSync} from "node:fs"
import {createRequire} from "node:module"
import {transpileModule,ModuleKind,JsxEmit} from "typescript"

const require=createRequire(import.meta.url)
test("client tables restore page size sorting and search from URL and persist changes",()=>{
  const params=new URLSearchParams("model=plc&page=2&limit=10&sort=name&order=asc&search=hello"),urls:string[]=[]
  let options:Record<string,unknown>={}
  const react=require("react")
  const modules:Record<string,unknown>={
    react:{...react,useId:()=>"table",useState:(value:unknown)=>[typeof value==="function"?value():value,()=>{}],useMemo:(fn:()=>unknown)=>fn()},
    "next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:(url:string)=>urls.push(url)})},
    "@tanstack/react-table":{tableFeatures:()=>({}),createPaginatedRowModel:()=>{},createSortedRowModel:()=>{},createColumnHelper:()=>({columns:(v:unknown)=>v,accessor:()=>({})}),useTable:(v:Record<string,unknown>)=>{options=v;return {getPageCount:()=>3,getHeaderGroups:()=>[],getRowModel:()=>({rows:[]}),getCanPreviousPage:()=>true,getCanNextPage:()=>true}}},
    "@/components/ui/table":{},"@/components/ui/button":{},"@/components/ui/input":{},"lucide-react":{},"@/components/read-region":{},"@/components/status-cell":{},"@/lib/scheduler":{stringifySchedulerJSON:JSON.stringify},
  }
  const compiled=transpileModule(readFileSync(new URL("../components/data-table.tsx",import.meta.url),"utf8"),{compilerOptions:{module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:Record<string,(props:unknown)=>unknown>={}
  new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
  exports.DataTable({data:[{name:"hello"},{name:"other"}],columns:[{key:"name",label:"이름",value:(r:{name:string})=>r.name,sortValue:(r:{name:string})=>r.name}]})
  assert.deepEqual(options.state,{sorting:[{id:"name",desc:false}],pagination:{pageIndex:1,pageSize:10}})
  assert.equal((options.data as unknown[]).length,1)
  ;(options.onPaginationChange as (v:unknown)=>void)({pageIndex:2,pageSize:10})
  assert.equal(new URL(urls.at(-1)!,"https://test").searchParams.get("page"),"3")
  ;(options.onSortingChange as (v:unknown)=>void)([{id:"name",desc:true}])
  const sorted=new URL(urls.at(-1)!,"https://test").searchParams
  assert.equal(sorted.get("order"),"desc")
  assert.equal(sorted.has("page"),false)
  assert.equal(sorted.get("model"),"plc")
})
