import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { runInNewContext } from "node:vm"
import * as React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ScriptTarget, ModuleKind, JsxEmit } from "typescript"
import * as summary from "./measurement-summary.ts"
import * as scheduler from "./scheduler.ts"
import * as sourceExport from "./source-export.ts"

const require=createRequire(import.meta.url)
const container=({children}:{children?:React.ReactNode})=>React.createElement("div",null,children)
function load<T>(path:string,modules:Record<string,unknown>,extra:Record<string,unknown>={}):T {
  const source=readFileSync(new URL(path,import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:Record<string,unknown>={}
  modules={"lucide-react":{TriangleAlert:()=>null,RefreshCw:()=>null,Braces:()=>null},...modules}
  runInNewContext(compiled,{exports,Error,URLSearchParams,...extra,require:(name:string)=>name in modules?modules[name]:name.startsWith("@/")||name.endsWith(".css")?{}:require(name)})
  return exports as T
}

test("summary decoding failures retain a common API error and visible cause for coverage, BEO and profiles",async()=>{
  const values:unknown[]=[0]
  for(let i=0;i<30;i++)values.push([`~${i}`,`~${i}`])
  const body=JSON.stringify({readProjection:{version:"measurement-summary.v1",values,shapes:[]},items:"~30"})
  let queryFn:((context:{signal:AbortSignal})=>Promise<unknown>)|undefined
  const capture=({queryFn:fn}:{queryFn:typeof queryFn})=>{queryFn=fn;return {error:null}}
  const client=load<{ApiError:new(...args:never[])=>Error;useChillQuery:(path:string,options:{decoder:(body:string)=>unknown})=>unknown}>("./client.tsx",{
    react:{...React,useContext:()=>({token:"test",config:{namespace:"ns"}}),useSyncExternalStore:()=>true},
    "@tanstack/react-query":{useQuery:capture},
    "@/lib/scheduler":scheduler,
    "@/lib/measurement-summary":summary,
  },{fetch:async()=>({ok:true,status:200,text:async()=>body,headers:new Headers()}),performance})
  const coverage=load<{useMeasurementCoverage:(namespace:string)=>unknown;CoverageError:React.ComponentType<{error:unknown;retry:()=>void}>}>("../features/measurement-coverage.tsx",{
    "@/lib/client":client,"@/lib/measurement-summary":summary,
    "@tanstack/react-query":{useQuery:capture},
    "next/navigation":{useSearchParams:()=>new URLSearchParams()},
    "next/link":{__esModule:true,default:container},
    "@/components/ui/button":{Button:container},
  },{fetch:async()=>({ok:true,status:200,text:async()=>body})})
  const region=load<{QueryGate:React.ComponentType<{query:unknown}>}>("../components/read-region.tsx",{
    "@/lib/client":client,"@/lib/scheduler":scheduler,"@/lib/source-export":sourceExport,
    "@/lib/labels":{explain:()=>["일반 조회 오류"],statusLabel:()=>""},
    "@/components/ui/button":{Button:container},
    "@/components/ui/dialog":Object.fromEntries(["Dialog","DialogContent","DialogDescription","DialogHeader","DialogTitle","DialogTrigger"].map(key=>[key,container])),
  })
  for(const kind of ["coverage","beo","profiles"]) {
    if(kind==="coverage")coverage.useMeasurementCoverage("ns")
    else client.useChillQuery(kind,{decoder:summary.parseMeasurementJSON})
    let error:Error&{status?:number;body?:string;data?:{code?:string}}|undefined
    try {await queryFn!({signal:new AbortController().signal})}catch(caught){error=caught as typeof error}
    assert.ok(error instanceof client.ApiError,kind)
    assert.equal(error?.status,0,kind)
    assert.equal(error?.data?.code,"MeasurementSummaryInvalid",kind)
    assert.match(error?.body??"",/측정 요약 확장 한도 초과/,kind)
    const html=renderToStaticMarkup(kind==="coverage"
      ?React.createElement(coverage.CoverageError,{error,retry:()=>{}})
      :React.createElement(region.QueryGate,{query:{error,isPending:false,isFetching:false,dataUpdatedAt:0,refetch:()=>{}}}))
    const headline=html.match(/role="alert"[^>]*><p[^>]*>(.*?)<\/p>/)?.[1]??""
    assert.match(headline,/측정 요약 확장 한도 초과/,kind)
    assert.doesNotMatch(html,/HTTP undefined|HTTP 0/,kind)
  }
})
