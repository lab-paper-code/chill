import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import * as React from "react"
import * as query from "@tanstack/react-query"
import * as icons from "lucide-react"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import type { ChillMutationInput } from "./client.tsx"
import type { RelayResponse } from "./client-transport.ts"
import * as summary from "./measurement-summary.ts"

const require=createRequire(import.meta.url)
for(const reason of ["unmount","logout"] as const)test(`warming mutation makes no further HTTP requests after ${reason}`,async()=>{
  const client=new query.QueryClient(),cleanups:(()=>void)[]=[],session={token:"original-token",epoch:1,signals:new Set<AbortController>()}
  let observer:query.MutationObserver<RelayResponse,Error,ChillMutationInput>|undefined
  const modules:Record<string,unknown>={
    react:{...React,useState:(initial:unknown)=>[typeof initial==="function"?initial():initial,()=>{}],useRef:(current:unknown)=>({current}),useContext:()=>session,useEffect:(effect:()=>undefined|(()=>void))=>{const cleanup=effect();if(cleanup)cleanups.push(cleanup)}},
    "@tanstack/react-query":{...query,useMutation:(options:query.MutationObserverOptions<RelayResponse,Error,ChillMutationInput>)=>{observer=new query.MutationObserver(client,{...options,retryDelay:()=>0});return {mutateAsync:(input:ChillMutationInput)=>observer!.mutate(input)}}},
    "lucide-react":icons,"next/navigation":{},"@/components/ui/button":{},"@/components/ui/input":{},"@/components/ui/dialog":{},"@/components/brand":{},"@/lib/scheduler":{},"@/lib/session-token":{},
  }
  const transport={}
  const transportSource=readFileSync(new URL("./client-transport.ts",import.meta.url),"utf8")
  new Function("require","exports",transpileModule(transportSource,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS}}).outputText)((name:string)=>name==="@/lib/measurement-summary"?summary:require(name),transport)
  modules["@/lib/client-transport"]=transport
  const exports:{useChillMutation?:()=>{mutateAsync:(input:ChillMutationInput)=>Promise<RelayResponse>}}={}
  const source=readFileSync(new URL("./client.tsx",import.meta.url),"utf8")
  new Function("require","exports",transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>modules[name]??require(name),exports)
  const mutation=exports.useChillMutation!(),previous=globalThis.fetch
  let calls=0,cancelled=false
  globalThis.fetch=async()=>++calls===1?Response.json({code:"CoverageWarming"},{status:503,headers:{"Retry-After":"2"}}):Response.json({request:"Accepted"})
  const unsubscribe=observer!.subscribe(result=>{
    if(result.failureCount!==1||cancelled)return
    cancelled=true
    if(reason==="unmount")for(const cleanup of cleanups)cleanup()
    else {for(const controller of session.signals)controller.abort();session.token="";session.epoch++}
  })
  try {
    await assert.rejects(mutation.mutateAsync({path:"v1/workloads",method:"POST",body:{name:"original"}}))
    assert.equal(cancelled,true)
    assert.equal(calls,1)
  } finally {unsubscribe();for(const cleanup of cleanups)cleanup();client.clear();globalThis.fetch=previous}
})
