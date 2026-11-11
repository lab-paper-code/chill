import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as summary from "./measurement-summary.ts"
import type * as Client from "./client.tsx"

const require=createRequire(import.meta.url)
function load(fetch:typeof globalThis.fetch):typeof Client {
  const modules:Record<string,unknown>={"lucide-react":{},"next/navigation":{},"@/components/ui/button":{},"@/components/ui/input":{},"@/components/ui/dialog":{},"@/components/brand":{},"@/lib/scheduler":{},"@/lib/session-token":{},"@/lib/measurement-summary":summary}
  const exports={} as typeof Client
  const source=readFileSync(new URL("./client.tsx",import.meta.url),"utf8")
  let ticks=0
  new Function("require","exports","fetch","performance","Error",transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>modules[name]??require(name),exports,fetch,{now:()=>100+37.5*ticks++},Error)
  return exports
}

for(const failure of ["fetch","text"] as const) test(`${failure} failure preserves response diagnostics and current unknown-outcome branches`,async()=>{
  const token="private-token"
  const cases=[
    {method:"GET",path:"v1/measurements?variantId=secret",unknownOutcome:false},
    {method:"POST",path:"v1/measurements?variantId=secret",unknownOutcome:true},
    {method:"POST",path:"v1/measurements/preflight",unknownOutcome:false},
    {method:"POST",path:"v1/measurements?dryRun=All",unknownOutcome:false},
    {method:"POST",path:"v1/measurements/preflight?variantId=secret",unknownOutcome:true},
  ]
  for(const {method,path,unknownOutcome} of cases) {
    const original=new TypeError(`${token} failed; repeated ${token}`)
    const headers=new Headers({"X-Chill-Request-ID":"request-42","X-Chill-Workload-UID":"workload-7","X-Chill-Relay-Timing":"relay=12"})
    const client=load(async()=>{
      if(failure==="fetch")throw original
      return {status:202,headers,text:async()=>{throw original}} as unknown as Response
    })
    await assert.rejects(client.relay(path,token,new AbortController().signal,method),error=>{
      assert.ok(error instanceof client.ApiError)
      assert.equal(error.status,failure==="text"?202:0)
      assert.equal(error.requestID,failure==="text"?"request-42":null)
      assert.equal(error.workloadUID,failure==="text"?"workload-7":null)
      assert.equal(error.relayTiming,failure==="text"?"relay=12":null)
      assert.equal(error.browserRTT,37.5)
      assert.equal(error.unknownOutcome,unknownOutcome,`${method} ${path}`)
      assert.equal(error.requestPath,`/${path.split("?")[0]}`)
      assert.deepEqual(error.originalError,{name:"TypeError",message:"[redacted] failed; repeated [redacted]"})
      assert.deepEqual(client.errorDetails(error),{status:error.status,code:undefined,message:undefined,exception:error.originalError,requestPath:error.requestPath})
      return true
    })
  }
})

test("decoder classification retains measurement decoder identity and generic wrapper behavior",()=>{
  const client=load(globalThis.fetch)
  const body=JSON.stringify({readProjection:{version:"unsupported",values:[],shapes:[]}})
  const cases=[
    {decoder:summary.parseMeasurementJSON,code:"MeasurementSummaryInvalid",message:"측정 요약 형식 미지원"},
    {decoder:()=>{throw new Error("generic decode failure")},code:"DashboardPrecisionUnsupported",message:"generic decode failure"},
    {decoder:(body:string)=>summary.parseMeasurementJSON(body),code:"DashboardPrecisionUnsupported",message:"측정 요약 형식 미지원"},
  ]
  for(const {decoder,code,message} of cases) assert.throws(()=>client.decodeResponse(body,decoder),error=>{
    assert.ok(error instanceof client.ApiError)
    assert.equal(error.status,0)
    assert.equal(error.body,message)
    assert.deepEqual(error.data,{code,message})
    return true
  })
})

test("warming delay clamps finite seconds to 1–5 and falls back to 2 seconds",()=>{
  const client=load(globalThis.fetch)
  for(const [seconds,delay] of [[0,1000],[-1,1000],[1,1000],[3,3000],[5,5000],[6,5000],[NaN,2000]]) {
    const error=new client.ApiError(503,"",{code:"CoverageWarming"},false,null,null,null,null,seconds)
    assert.equal(client.warmingDelay(0,error),delay,String(seconds))
  }
  assert.equal(client.warmingDelay(0,new Error("generic")),2000)
})
