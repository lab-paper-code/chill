import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import { QueryClient, QueryObserver } from "@tanstack/react-query"
import { createElement } from "react"
import type { ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import * as icons from "lucide-react"
import type * as Client from "./client.tsx"
import type * as ReadRegion from "../components/read-region.tsx"

const require=createRequire(import.meta.url)
const modules:Record<string,unknown>={"lucide-react":icons,"next/navigation":{},"@/components/ui/button":{},"@/components/ui/input":{},"@/components/ui/dialog":{},"@/components/brand":{},"@/lib/scheduler":{},"@/lib/session-token":{},"@/lib/measurement-summary":{}}
const exports={} as typeof Client
const source=readFileSync(new URL("./client.tsx",import.meta.url),"utf8")
new Function("require","exports",transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>modules[name]??require(name),exports)

test("CoverageWarming is initialization, retries Retry-After and resolves real data",async()=>{
  let calls=0
  const previous=globalThis.fetch
  globalThis.fetch=async()=>++calls===1?Response.json({code:"CoverageWarming",message:"serving initialization is not complete"},{status:503,headers:{"Retry-After":"2"}}):Response.json({items:[{name:"ready"}]})
  const client=new QueryClient({defaultOptions:{queries:{retry:exports.warmingRetry,retryDelay:(count,error)=>{assert.equal(exports.warmingDelay(count,error),2000);return 0}}}})
  const observer=new QueryObserver(client,{queryKey:["warm"],queryFn:async({signal})=>(await exports.relay("v1/models","token",signal)).data})
  const states:string[]=[]
  const unsubscribe=observer.subscribe(result=>{if(result.failureReason)states.push(exports.isCoverageWarming(result.failureReason)?"초기화 중":"오류")})
  try {
    const result=await observer.refetch()
    assert.deepEqual(result.data,{items:[{name:"ready"}]})
    assert.equal(calls,2)
    assert.ok(states.includes("초기화 중"))
    assert.ok(!states.includes("오류"))
    const warming=new exports.ApiError(503,"",{code:"CoverageWarming"},false,null,null,null,null,2)
    assert.equal(exports.warmingRetry(179,warming),true)
    assert.equal(exports.warmingRetry(180,warming),false)
    assert.equal(exports.warmingRetry(0,new exports.ApiError(503,"",{code:"CoverageUnavailable"})),false)
    assert.equal(exports.warmingRetry(0,new exports.ApiError(403,"",{code:"CoverageWarming"})),false)
  } finally {unsubscribe();client.clear();globalThis.fetch=previous}
})

test("shared QueryGate renders initialization as a status rather than an HTTP error",()=>{
  const gate={} as typeof ReadRegion
  const wrap=({children}:{children?:ReactNode})=>createElement("div",{},children)
  const body=readFileSync(new URL("../components/read-region.tsx",import.meta.url),"utf8")
  const dependencies:Record<string,unknown>={"lucide-react":icons,"@/lib/client":{...exports,useSession:()=>({token:"token"})},"@/components/ui/button":{Button:wrap},"@/components/ui/dialog":{},"@/lib/source-export":{},"@/lib/scheduler":{},"@/lib/labels":{explain:()=>["오류"],statusLabel:()=>""}}
  new Function("require","exports",transpileModule(body,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>dependencies[name]??require(name),gate)
  const query={error:new exports.ApiError(503,"",{code:"CoverageWarming"}),isPending:false,isFetching:false,dataUpdatedAt:0,refetch:()=>{}}
  const html=renderToStaticMarkup(createElement(gate.QueryGate,{query},null))
  assert.match(html,/role="status"[^>]*>초기화 중/)
  assert.doesNotMatch(html,/role="alert"|HTTP 503|오류/)
})
