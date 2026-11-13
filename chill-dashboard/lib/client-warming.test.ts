import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import { QueryClient, QueryObserver } from "@tanstack/react-query"
import * as icons from "lucide-react"
import type * as Client from "./client.tsx"
import type * as Transport from "./client-transport.ts"
import * as summary from "./measurement-summary.ts"

const require=createRequire(import.meta.url)
const modules:Record<string,unknown>={"lucide-react":icons,"next/navigation":{},"@/components/ui/button":{},"@/components/ui/input":{},"@/components/ui/dialog":{},"@/components/brand":{},"@/lib/scheduler":{},"@/lib/session-token":{},"@/lib/measurement-summary":summary}
const exports={} as typeof Transport
const source=readFileSync(new URL("./client.tsx",import.meta.url),"utf8")
const transportSource=readFileSync(new URL("./client-transport.ts",import.meta.url),"utf8")
new Function("require","exports",transpileModule(transportSource,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>modules[name]??require(name),exports)
modules["@/lib/client-transport"]=exports

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

test("data-dependent polling stops for terminal measurements, hidden tabs and denied reads",()=>{
  let visible=true
  let interval:(query:{state:{data?:{phase:string};error?:{status:number}}})=>number|false=()=>false
  const client={} as typeof Client
  const dependencies:Record<string,unknown>={...modules,react:{...require("react"),useContext:()=>({token:"token",config:{pollIntervalMs:5000}}),useSyncExternalStore:()=>visible},"@tanstack/react-query":{useQuery:(options:{refetchInterval:typeof interval})=>{interval=options.refetchInterval;return {data:undefined,error:null}}}}
  new Function("require","exports",transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>dependencies[name]??require(name),client)
  client.useChillQuery<{phase:string}>("v1/measurements",{poll:data=>data?.phase==="Measuring"})
  assert.equal(interval?.({state:{data:{phase:"Complete"}}}),false)
  assert.equal(interval?.({state:{data:{phase:"Measuring"},error:{status:403}}}),false)
  assert.equal(interval?.({state:{data:{phase:"Measuring"}}}),5000)
  visible=false
  client.useChillQuery<{phase:string}>("v1/measurements",{poll:data=>data?.phase==="Measuring"})
  assert.equal(interval?.({state:{data:{phase:"Measuring"}}}),false)
})

test("poll predicates receive a failed detail status while retaining the cached measurement",()=>{
  let interval:(query:{state:{data?:{phase:string|null};error?:Transport.ApiError|null}})=>number|false=()=>false
  const client={} as typeof Client
  const dependencies:Record<string,unknown>={...modules,react:{...require("react"),useContext:()=>({token:"token",config:{pollIntervalMs:5000}}),useSyncExternalStore:()=>true},"@tanstack/react-query":{useQuery:(options:{refetchInterval:typeof interval})=>{interval=options.refetchInterval;return {data:undefined,error:null}}}}
  new Function("require","exports",transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>dependencies[name]??require(name),client)
  client.useChillQuery<{phase:string|null}>("v1/namespaces/ns/dispatch-occupancy-measurements/removed",{poll:(data,error?:Transport.ApiError|null)=>error?.status!==404&&(!data?.phase||data.phase==="Measuring")})
  const pending={phase:null}
  assert.equal(interval({state:{data:pending}}),5000)
  assert.equal(interval({state:{data:pending,error:new exports.ApiError(404,"missing",null)}}),false)
  assert.equal(interval({state:{data:pending,error:new exports.ApiError(503,"unavailable",null)}}),5000)
})

test("client transport preserves settings ETags and conditional headers, including legacy absence and 409/428",async()=>{
  const previous=globalThis.fetch,sent:RequestInit[]=[],signal=new AbortController().signal
  let status=200,tag:string|undefined='"7"'
  globalThis.fetch=async(_input,init)=>{sent.push(init!);return Response.json(status===200?{controls:{}}:{code:status===409?"SettingsConflict":"SettingsPreconditionRequired"},{status,headers:tag?{ETag:tag}:{}})}
  try {
    const read=await exports.relay("v1/scheduler/settings","test-token",signal)
    assert.equal(read.etag,'"7"')
    tag='"8"'
    assert.equal((await exports.relay("v1/scheduler/settings","test-token",signal,"PUT",'{"controls":{}}',read.etag!)).etag,'"8"')
    assert.equal(new Headers(sent.at(-1)!.headers).get("If-Match"),'"7"')
    assert.equal(sent.at(-1)!.body,'{"controls":{}}')
    tag=undefined
    const legacy=await exports.relay("v1/scheduler/settings","test-token",signal)
    assert.equal(legacy.etag,null)
    await exports.relay("v1/scheduler/settings","test-token",signal,"PUT",'{"controls":{}}')
    assert.equal(new Headers(sent.at(-1)!.headers).has("If-Match"),false)
    for(status of [409,428])await assert.rejects(exports.relay("v1/scheduler/settings","test-token",signal,"PUT",'{"controls":{}}'),error=>error instanceof exports.ApiError&&error.status===status&&!error.unknownOutcome)
    assert.equal(sent.length,6)
  } finally {globalThis.fetch=previous}
})
