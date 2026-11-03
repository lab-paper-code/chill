import assert from "node:assert/strict"
import test from "node:test"
import {readFileSync} from "node:fs"
import {createRequire} from "node:module"
import {transpileModule,ModuleKind,JsxEmit,ScriptTarget} from "typescript"
import {relayPlan} from "./relay.ts"
import {serverConfig} from "./server-config.ts"
import * as scheduler from "./scheduler.ts"
import * as summary from "./measurement-summary.ts"
import type * as Client from "./client.tsx"
import type {MeasurementPlanList} from "./read-types.ts"

const require=createRequire(import.meta.url)
const config=serverConfig({CHILL_BASE_URL:"https://query.invalid",CHILL_NAMESPACE:"ns",CHILL_SYSTEM_NAME:"chill"})
function load<T>(file:string,modules:Record<string,unknown>,fetch?:unknown):T {
  const code=transpileModule(readFileSync(new URL(file,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  new Function("require","exports","fetch",code)((name:string)=>modules[name]??(name.startsWith("@/")||name.endsWith(".css")||name==="lucide-react"||name==="next/navigation"?{}:require(name)),exports,fetch)
  return exports as T
}
const items=Array.from({length:279},(_,index)=>({metadata:{name:`request-${index}`,namespace:"ns",uid:`uid-${index}`}}))

for(const mode of ["old-query","new-query","downgrade-mid-page"] as const)test(`control323 review: new dashboard + ${mode} passes real relay and retains all 279 BEOs`,async()=>{
  const paths:string[]=[],signal=new AbortController().signal
  const fetch=async(path:string,options:{signal:AbortSignal})=>{
    assert.equal(options.signal,signal)
    const incoming=new URL(path,"https://dashboard.invalid")
    const plan=relayPlan("GET",incoming.pathname.slice("/api/chill/".length).split("/"),incoming.search,config)
    paths.push(plan.path)
    const query=new URL(plan.path,"https://query.invalid").searchParams
    const old=mode==="old-query"||mode==="downgrade-mid-page"&&Number(query.get("offset"))>0
    if(old&&query.has("summary"))return {ok:false,status:400,headers:new Headers(),text:async()=>JSON.stringify({code:"InvalidQuery"})}
    const paged=query.has("limit"),offset=Number(query.get("offset")),end=Math.min(offset+50,items.length)
    const body={items:paged?items.slice(offset,end):items,identity:"~3",readProjection:{version:query.has("summary")?"measurement-summary.v2":"measurement-summary.v1",shapes:[],values:query.has("summary")?["ns","request","uid",{$s:["~0","~1","~2"]}]:["ns/request/uid","unused","unused","ns/request/uid"]},...(paged?{page:{offset,limit:50,total:items.length,...(end<items.length?{nextOffset:end}:{})}}:{})}
    return {ok:true,status:200,headers:new Headers(),text:async()=>JSON.stringify(body)}
  }
  const client=load<typeof Client>("./client.tsx",{"@/lib/scheduler":scheduler,"@/lib/measurement-summary":summary},fetch)
  let queryFn:({signal}:{signal:AbortSignal})=>Promise<MeasurementPlanList>=async()=>({items:[]})
  const state={data:undefined,error:null}
  const feature=load<{useMeasurementPlans:(namespace:string)=>unknown}>("../hooks/use-measurement-data.ts",{
    "@/lib/client":{...client,useSession:()=>({token:"fixture"})},"@/lib/measurement-summary":summary,
    "@tanstack/react-query":{useQuery:(options:{queryFn:typeof queryFn})=>{queryFn=options.queryFn;return state}},
  })
  feature.useMeasurementPlans("ns")
  const result=await queryFn({signal})
  assert.deepEqual(result.items,items)
  assert.equal(new Set(result.items!.map(item=>item.metadata.uid)).size,279)
  assert.equal((result as MeasurementPlanList&{identity:string}).identity,"ns/request/uid")
  assert.equal(paths.length,mode==="new-query"?6:mode==="old-query"?2:3)
  if(mode!=="new-query")assert.equal(paths.at(-1),"/api/v1/namespaces/ns/batch-energy-optimizations?view=summary")
})

test("control323 review: coverage/profiles negotiate with old Query without retrying permissions or other errors",async()=>{
  for(const route of ["v1/namespaces/ns/measurement-coverage?view=summary&summary=v2&node=node-a","v1/scheduler/profiles?view=summary&summary=v2&limit=50&offset=100"]) {
    for(const status of [400,403,503]) {
      const paths:string[]=[]
      const client=load<typeof Client>("./client.tsx",{"@/lib/scheduler":scheduler,"@/lib/measurement-summary":summary},async(path:string)=>{
        const incoming=new URL(path,"https://dashboard.invalid")
        paths.push(relayPlan("GET",incoming.pathname.slice("/api/chill/".length).split("/"),incoming.search,config).path)
        const legacy=!incoming.searchParams.has("summary")
        return {ok:legacy,status:legacy?200:status,headers:new Headers(),text:async()=>JSON.stringify(legacy?{value:"retained"}:{code:route.includes("coverage")?"InvalidCoverageQuery":"InvalidQuery"})}
      })
      const read=client.relay(route,"fixture",new AbortController().signal)
      if(status===400) {
        assert.deepEqual((await read).data,{value:"retained"})
        assert.equal(paths.length,2)
        const legacy=new URL(paths[1],"https://query.invalid")
        assert.equal(legacy.searchParams.has("summary"),false)
        for(const key of ["node","limit","offset"])assert.equal(legacy.searchParams.get(key),new URL(route,"https://local.invalid").searchParams.get(key))
      } else {await assert.rejects(read);assert.equal(paths.length,1)}
    }
  }
})
