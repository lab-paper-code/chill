import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement,type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule,ModuleKind,JsxEmit,ScriptTarget } from "typescript"
import * as scheduler from "./scheduler.ts"
import * as identity from "./workload-identity.ts"

const require=createRequire(import.meta.url)
test("service request records preserve exact API range, opaque page cursor and repeated definition/outcome filters",()=>{
  const params=new URLSearchParams("kind=requests&uid=service&from=2026-10-31T01%3A00%3A00.123456789Z&to=2026-10-31T02%3A00%3A00.987654321Z&limit=17&cursor=opaque-page&definition=a&definition=b&outcome=completed&outcome=timeout"),calls:string[]=[]
  const box=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const modules:Record<string,unknown>={react:require("react"),"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:()=>{}})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillQuery:(path:string)=>{calls.push(path);return {data:path==="v1/workloads?view=summary"?[]:{uid:"service",definitions:[],outcomes:{completed:1},requests:[{receivedAt:"2026-10-31T01:30:00Z",kind:"final",outcome:"completed",elapsedNanos:0}],management:[],cohorts:[],total:1,nextCursor:"next-opaque"}}}},"@/lib/scheduler":scheduler,"@/lib/workload-identity":identity,"@/lib/labels":{},"@/lib/date-time":{},"@/features/scheduler-history":{when:(value:string)=>value},"@/components/read-region":{QueryGate:box,SourceJSON:()=>null,SourceExport:()=>null},"@/components/screen":{Screen:({children,title}:{children?:ReactNode;title:string})=>createElement("main",null,createElement("h1",null,title),children),Rows:box,Row:()=>null,RowDialog:()=>null,Empty:box},"@/components/chart-area-interactive":{},"@/components/stage-breakdown":{duration:(value:number)=>String(value)},"@/components/ui/button":{Button:({children}:{children?:ReactNode})=>createElement("button",null,children)},"@/components/ui/input":{},"@/components/records-navigation":{RecordsNavigation:()=>createElement("nav",null,"서비스 요청"),RecordsRange:()=>null},"@/components/ui/sheet":{Sheet:()=>null,SheetContent:box,SheetHeader:box,SheetTitle:box}}
  const exports:Record<string,unknown>={},compiled=transpileModule(readFileSync(new URL("../features/observations.tsx",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports)
  const html=renderToStaticMarkup(createElement(exports.ObservationsPage as ()=>ReactNode))
  assert.match(html,/<h1>기록<\/h1>/);assert.match(html,/완료/);assert.match(html,/<td class="p-3 text-right tabular-nums">0<\/td>/);assert.match(html,/다음 기록 보기/)
  const query=new URL(calls.find(path=>path.startsWith("v1/observations/workloads/"))!,"https://dashboard.invalid").searchParams
  for(const key of ["from","to","limit","cursor","definition","outcome"])assert.deepEqual(query.getAll(key),params.getAll(key))
  assert.equal(query.has("kind"),false)
})
