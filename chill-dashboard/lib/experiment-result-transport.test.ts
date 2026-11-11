import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createServer, type Server } from "node:http"
import { setImmediate } from "node:timers/promises"
import * as React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import * as query from "@tanstack/react-query"
import * as icons from "lucide-react"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as experiments from "./experiments.ts"
import * as comparison from "./experiment-comparison.ts"
import * as measurements from "./measurements.ts"
import * as scheduler from "./scheduler.ts"
import * as labels from "./labels.ts"
import type * as Client from "./client.tsx"
import type * as Transport from "./client-transport.ts"
import * as summary from "./measurement-summary.ts"
import type * as ReadRegion from "../components/read-region.tsx"
import type * as Conclusion from "../features/experiments-conclusion.tsx"

const require=createRequire(import.meta.url)
function load<T>(path:string,modules:Record<string,unknown>):T {
  const exports={}
  const source=readFileSync(new URL(path,import.meta.url),"utf8")
  new Function("require","exports",transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>name in modules?modules[name]:(name.startsWith("@/")&&name!=="@/lib/client-transport")?{}:require(name),exports)
  return exports as T
}
// Exact 40,900-byte HTTP 200 body saved at 2026-11-04T10:18:42.058Z in
// live-r3-run-7-7-published-error.json; the capture contains no failed-request cause.
const body=readFileSync(new URL("./fixtures/experiment-r3-7-7-results.json",import.meta.url),"utf8").trimEnd()
const publication=experiments.decodeExperiment(body)
const path=experiments.experimentPath(publication.id)
const wrap=({children}:{children?:React.ReactNode})=>React.createElement("div",null,children)
function harness() {
  const cache=new query.QueryClient()
  const transport=load<typeof Transport>("./client-transport.ts",{"@/lib/measurement-summary":summary})
  let options:query.QueryObserverOptions<experiments.Experiment,Transport.ApiError>={queryKey:[]}
  const client=load<typeof Client>("./client.tsx",{
    "@/lib/client-transport":transport,
    react:{...React,useContext:()=>({token:"fixture",config:{pollIntervalMs:5000}}),useSyncExternalStore:()=>true},
    "@tanstack/react-query":{...query,useQuery:(value:typeof options)=>{options=value;return {}}},
    "next/navigation":{},"lucide-react":icons,
  })
  client.useChillQuery<experiments.Experiment>(path,{decoder:experiments.decodeExperiment})
  const gate=load<typeof ReadRegion>("../components/read-region.tsx",{
    "@/lib/client-transport":transport,"@/lib/client":client,"@/lib/scheduler":scheduler,"@/lib/labels":labels,"lucide-react":icons,
    "@/lib/source-export":{safeSource:(value:unknown)=>value},"@/components/ui/button":{Button:wrap},
    "@/components/ui/dialog":Object.fromEntries(["Dialog","DialogTrigger","DialogContent","DialogDescription","DialogHeader","DialogTitle"].map(name=>[name,wrap])),
  })
  const conclusion=load<typeof Conclusion>("../features/experiments-conclusion.tsx",{
    "next/link":{default:"a"},"@/lib/client-transport":transport,"@/lib/client":client,"@/lib/experiments":experiments,
    "@/lib/experiment-comparison":comparison,"@/lib/measurements":measurements,"@/components/ui/button":{Button:wrap},
  })
  return {cache,client,transport,observe:()=>new query.QueryObserver(cache,options),render:(result:query.QueryObserverResult<experiments.Experiment,Transport.ApiError>)=>renderToStaticMarkup(React.createElement(gate.QueryGate,{query:{...result,refetch:()=>{}}},result.data&&React.createElement(conclusion.ExperimentStageResults,{run:result.data,stage:result.data.stages[0]})))}
}
async function connect(server:Server) {
  const listening=Promise.withResolvers<void>()
  server.listen(0,"127.0.0.1",listening.resolve)
  await listening.promise
  const address=server.address()
  assert.ok(address&&typeof address!=="string")
  const original=globalThis.fetch
  globalThis.fetch=(input,init)=>original(new URL(String(input),`http://127.0.0.1:${address.port}`),init)
  return async()=>{
    globalThis.fetch=original
    server.closeAllConnections()
    const closed=Promise.withResolvers<void>()
    server.close(error=>error?closed.reject(error):closed.resolve())
    await closed.promise
  }
}

test("RUN-2 diagnostic: route cancellation reverts state and remount renders the saved successful result",async()=>{
  let requests=0
  const pending=Promise.withResolvers<void>()
  const server=createServer((request,response)=>{
    assert.equal(request.url,`/api/chill/${path}`)
    if(++requests===1){pending.resolve();return}
    response.writeHead(200,{"Content-Type":"application/json"}).end(body)
  })
  const disconnect=await connect(server),app=harness(),observer=app.observe(),errors:unknown[]=[]
  let unsubscribe=observer.subscribe(result=>{if(result.error)errors.push(result.error)})
  try {
    await pending.promise
    unsubscribe()
    // Let fetch's aborted promise reject after TanStack has cancelled its retryer.
    await setImmediate()
    const cancelled=app.cache.getQueryState(["chill",path])!
    assert.equal(cancelled.error,null)
    assert.equal(cancelled.fetchStatus,"idle")
    assert.deepEqual(errors,[])
    const remounted=app.observe()
    unsubscribe=remounted.subscribe(()=>{})
    const result=await remounted.refetch()
    assert.equal(result.error,null)
    assert.equal(result.data?.id,publication.id)
    assert.equal(requests,2)
    const html=app.render(result)
    assert.match(html,/서비스별 결과/)
    assert.match(html,/27,639/)
    assert.match(html,/28,793/)
    assert.doesNotMatch(html,/role="alert"|정보를 불러오지 못했어요/)
  } finally {unsubscribe();app.cache.clear();await disconnect()}
})

for(const failure of ["timeout","transport"] as const)test(`RUN-2 diagnostic: genuine ${failure} failure remains observable, not empty results`,async()=>{
  let requests=0
  const server=createServer((_request,response)=>{
    requests++
    if(failure==="transport")response.destroy()
    else response.writeHead(504,{"Content-Type":"application/json"}).end(JSON.stringify({code:"DashboardRelayTimeout"}))
  })
  const disconnect=await connect(server),app=harness(),observer=app.observe()
  const unsubscribe=observer.subscribe(()=>{})
  try {
    const result=await observer.refetch()
    assert.ok(result.error instanceof app.transport.ApiError)
    assert.equal(result.error.status,failure==="transport"?0:504)
    assert.equal(result.data,undefined)
    assert.equal(requests,1)
    assert.match(app.render(result),/role="alert"/)
    if(failure==="transport")assert.match(app.render(result),/TypeError|AbortError/)
    if(failure==="timeout")assert.deepEqual(result.error.data,{code:"DashboardRelayTimeout"})
  } finally {unsubscribe();app.cache.clear();await disconnect()}
})
