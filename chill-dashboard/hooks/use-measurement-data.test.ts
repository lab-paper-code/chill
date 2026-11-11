import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import type { MeasurementPlan } from "../lib/read-types.ts"
import * as measurementSummary from "../lib/measurement-summary.ts"

const plan:MeasurementPlan={metadata:{namespace:"ns",name:"195",uid:"plan-uid",resourceVersion:"1",creationTimestamp:"2026-10-29T00:00:00Z"},spec:{state:{candidate:{modelSpec:"model & one",deviceClass:"class",executionPath:"cpu",powerMode:"MAXN",preprocessor:"pre"},targetNode:"node"},activeProtocol:{dataset:"data",durationSeconds:10},idleProtocol:{durationSeconds:10},trialsPerBatch:3,retryLimit:1},status:{phase:"Complete",result:{store:"s",key:"result"}}}

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

test("request profile history preserves paging, query policy and summary decoding",async()=>{
  const require=createRequire(import.meta.url)
  const source=readFileSync(new URL("../hooks/use-measurement-data.ts",import.meta.url),"utf8")+"\nexports.capture=useRequestProfileHistory"
  const code=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const warmingRetry=()=>false,warmingDelay=()=>1000
  class ApiError extends Error {
    status:number
    constructor(status:number,message:string){super(message);this.status=status}
  }
  type Page={profiles:{id:string;completedItems:bigint}[];page:{offset:number;limit:number;total:number;nextOffset?:number};readAt:string}
  type Options={queryKey:string[];enabled:boolean;retry:unknown;retryDelay:unknown;queryFn:(context:{signal:AbortSignal})=>Promise<Page>}
  for(const token of ["fixture",""])for(const outcome of ["complete","stalled","denied"]) {
    const signal=new AbortController().signal,paths:string[]=[],failure=new Error("follow-up failed")
    let options:Options|undefined
    const state={error:null,isPending:true}
    const modules:Record<string,unknown>={
      "@tanstack/react-query":{useQuery:(value:Options)=>{options=value;return state}},
      "@/lib/client":{ApiError,warmingRetry,warmingDelay,useSession:()=>({token,config:{namespace:"ns"}}),useChillQuery:()=>({}),decodeResponse:(body:string,decoder:(text:string)=>unknown)=>{
        assert.equal(decoder,measurementSummary.parseMeasurementJSON)
        return decoder(body)
      },relay:async(path:string,requestToken:string,requestSignal:AbortSignal)=>{
        assert.equal(requestToken,token)
        assert.equal(requestSignal,signal)
        paths.push(path)
        assert.ok(paths.length<=2,"stalled pagination must stop before a third request")
        if(paths.length===2&&outcome==="denied")throw failure
        const first=paths.length===1,offset=first?0:50,id=first?"A":"B"
        const page={offset,limit:50,total:2,...(first||outcome==="stalled"?{nextOffset:50}:{})}
        return {body:JSON.stringify({readProjection:{version:"measurement-summary.v2",shapes:[["id","completedItems"]],values:[{$o:[0,id,"PRECISE_INTEGER"]}]},profiles:["~0"],page,readAt:first?"first-page":"final-page"}).replace('"PRECISE_INTEGER"',"18446744073709551615")}
      }},
      "@/lib/measurement-summary":measurementSummary,
    }
    const exports={} as {capture:()=>unknown}
    new Function("require","exports",code)((name:string)=>modules[name]??(name.startsWith("@/")||name.endsWith(".css")?{}:require(name)),exports)
    exports.capture()
    assert.ok(options,"capture the production request-history query")
    assert.deepEqual(options.queryKey,["chill","request-profile-history"])
    assert.equal(options.enabled,!!token)
    assert.equal(typeof options.retry,"function")
    assert.equal(typeof options.retryDelay,"function")
    assert.equal(options.retry,warmingRetry)
    assert.equal(options.retryDelay,warmingDelay)
    if(outcome==="complete")assert.deepEqual(await options.queryFn({signal}),{
      profiles:[{id:"A",completedItems:18446744073709551615n},{id:"B",completedItems:18446744073709551615n}],
      page:{offset:50,limit:50,total:2},readAt:"final-page",
    })
    else if(outcome==="stalled")await assert.rejects(options.queryFn({signal}),(error:unknown)=>{
      assert.ok(error instanceof ApiError)
      assert.equal(error.status,0)
      assert.equal(error.message,"프로파일 페이지가 전진하지 않습니다")
      return true
    })
    else await assert.rejects(options.queryFn({signal}),(error:unknown)=>error===failure,"follow-up failure must reject, not return partial [A]")
    assert.equal(paths.length,2)
    paths.forEach((path,index)=>{
      const url=new URL(path,"https://local.invalid")
      assert.equal(url.pathname,"/v1/scheduler/profiles")
      assert.deepEqual([...url.searchParams],[["view","summary"],["summary","v2"],["limit","50"],["offset",String(index*50)]])
    })
  }
})
