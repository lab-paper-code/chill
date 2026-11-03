import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import type { MeasurementPlan } from "../lib/read-types.ts"
import * as measurementSummary from "../lib/measurement-summary.ts"

const plan:MeasurementPlan={metadata:{namespace:"ns",name:"195",uid:"plan-uid",resourceVersion:"1",creationTimestamp:"2026-10-29T00:00:00Z"},spec:{state:{candidate:{modelSpec:"model & one",deviceClass:"class",executionPath:"cpu",powerMode:"MAXN",preprocessor:"pre"},targetNode:"node"},activeProtocol:{dataset:"data",durationSeconds:10},idleProtocol:{durationSeconds:10},trialsPerBatch:3,retryLimit:1,batchSizes:[1]},status:{phase:"Complete",result:{store:"s",key:"result"}}}

test("control323: BEO paging retains later copy sources, rejects stalled/error pages and shares cancellation",async()=>{
  const require=createRequire(import.meta.url)
  const code=transpileModule(readFileSync(new URL("../hooks/use-measurement-data.ts",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  for(const outcome of ["complete","stalled","denied","aborted"]) {
    const paths:string[]=[],signal=new AbortController().signal
    let queryFn:({signal}:{signal:AbortSignal})=>Promise<{items:MeasurementPlan[]}> = async()=>{throw new Error("query not captured")}
    const error=new Error(outcome),state={error:null as Error|null,data:{items:[plan]}}
    const modules:Record<string,unknown>={
      "@tanstack/react-query":{useQuery:(options:{enabled:boolean;queryFn:typeof queryFn})=>{assert.equal(options.enabled,true);queryFn=options.queryFn;return state}},
      "@/lib/client":{useSession:()=>({token:"fixture"}),ApiError:class extends Error {constructor(_status:number,body:string){super(body)}},decodeResponse:(body:string,decoder:(text:string)=>unknown)=>decoder(body),relay:async(path:string,_token:string,requestSignal:AbortSignal)=>{
        assert.equal(requestSignal,signal)
        paths.push(path)
        const offset=new URL(path,"https://local.invalid").searchParams.get("offset")
        if(offset!=="0"&&["denied","aborted"].includes(outcome))throw error
        return {body:JSON.stringify({items:[offset==="0"?plan:{...plan,metadata:{...plan.metadata,name:"later-copy-source"}}],page:{nextOffset:offset==="0"?50:outcome==="stalled"?50:undefined}})}
      }},
      "@/lib/measurement-summary":measurementSummary,
    }
    const exports={} as {useMeasurementPlans:(namespace:string)=>{data:unknown}}
    new Function("require","exports",code)((name:string)=>modules[name]??(name.startsWith("@/")||name.endsWith(".css")?{}:require(name)),exports)
    exports.useMeasurementPlans("ns")
    if(outcome==="complete")assert.deepEqual((await queryFn({signal})).items.map(item=>item.metadata.name),["195","later-copy-source"])
    else await assert.rejects(queryFn({signal}),outcome==="stalled"?/페이지가 전진하지/:error)
    assert.equal(paths.length,2)
    for(const path of paths)assert.equal(new URL(path,"https://local.invalid").searchParams.get("limit"),"50")
    state.error=error
    assert.equal(exports.useMeasurementPlans("ns").data,state.data,"failed refresh retains the last complete list")
  }
})
