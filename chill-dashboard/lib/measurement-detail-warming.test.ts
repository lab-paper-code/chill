import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import { QueryClient, QueryObserver, type QueryObserverOptions } from "@tanstack/react-query"
import { isValidElement, type ReactElement, type ReactNode } from "react"
import * as coverage from "./measurement-coverage.ts"
import * as assessment from "./measurement-assessment.ts"
import * as measurements from "./measurements.ts"
import * as summary from "./measurement-summary.ts"
import type * as Transport from "./client-transport.ts"
import * as occupancy from "./dispatch-occupancy.ts"

const require=createRequire(import.meta.url)
function load<T extends object>(file:string,modules:Record<string,unknown>,suffix="") {
  const exports={} as T
  const source=readFileSync(new URL(file,import.meta.url),"utf8")+suffix
  new Function("require","exports",transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>name==="@/hooks/use-measurement-data"?load("../hooks/use-measurement-data.ts",modules):modules[name]??((name.startsWith("@/")&&name!=="@/lib/client-transport")||name.endsWith(".css")?{}:require(name)),exports)
  return exports
}
const clientModule=load<typeof Transport>("./client-transport.ts",{"@/lib/measurement-summary":summary})
type ProfileOptions=QueryObserverOptions<{profiles:{id:string}[]},Error>

test("R18 opened coverage profile timestamps recover from CoverageWarming 503 to 200",async()=>{
  let options!:ProfileOptions
  let calls=0
  const previous=globalThis.fetch
  globalThis.fetch=async()=>++calls===1?Response.json({code:"CoverageWarming"},{status:503,headers:{"Retry-After":"2"}}):Response.json({profiles:[{id:"profile",measuredAt:"2026-11-03T00:00:00Z"}]})
  const modules={react:{useRef:()=>({current:null}),useEffect:()=>{}},"next/navigation":{useSearchParams:()=>new URLSearchParams()},"@tanstack/react-query":{useQuery:(value:ProfileOptions)=>{options=value;return {data:null,error:null,isPending:true}}},"@/lib/client-transport":clientModule,"@/lib/client":{useSession:()=>({token:"token"}),useChillQuery:()=>({data:null})},"@/lib/measurement-coverage":coverage,"@/lib/measurement-assessment":assessment,"@/lib/measurements":measurements,"@/components/ui/button":{Button:"button"},"next/link":{default:"a"}}
  Object.assign(modules,{"@/lib/measurement-summary":summary,"@/lib/dispatch-occupancy":occupancy})
  const feature=load<{CoverageDetail:(props:unknown)=>ReactNode}>("../features/measurement-coverage.tsx",modules,"\nexport { CoverageDetail };\n")
  const verdict={scope:{schedulerUsed:false},checklist:{checks:[]},reasons:[],evidence:{available:true,accepted:true}}
  const cell={id:"cell",model:{name:"model"},deviceClass:{name:"class"},executionPath:"cpu",powerMode:"fixed",support:{node:{name:"node"},reasons:[]},assessment:verdict,failureSummary:[],variants:[{id:"variant",condition:{},assessment:verdict,resultRows:[{id:"row",profileId:"profile",batch:1,assessment:verdict}],runIds:[]}],history:{},batchProgress:null}
  const queryClient=new QueryClient()
  let unsubscribe=()=>{}
  try {
    feature.CoverageDetail({cell,namespace:"ns",close:()=>{},visible:true})
    const observer=new QueryObserver<{profiles:{id:string}[]}>(queryClient,{...options,retryDelay:(count:number,error:unknown)=>{assert.equal(typeof options.retryDelay,"function");assert.equal((options.retryDelay as (count:number,error:unknown)=>number)(count,error),2000);return 0}})
    const states:string[]=[]
    unsubscribe=observer.subscribe(result=>{if(result.failureReason)states.push(clientModule.isCoverageWarming(result.failureReason)?"초기화 중":"오류")})
    const result=await observer.refetch()
    assert.equal(result.data?.profiles?.[0]?.id,"profile")
    assert.equal(calls,2)
    assert.ok(states.includes("초기화 중"))
    assert.ok(!states.includes("오류"))
    const warming=new clientModule.ApiError(503,"",{code:"CoverageWarming"})
    Object.assign(modules["@tanstack/react-query"],{useQuery:(value:ProfileOptions)=>{options=value;return {data:null,error:null,failureReason:warming,isPending:true}}})
    const tree=feature.CoverageDetail({cell,namespace:"ns",close:()=>{},visible:true})
    const statuses=(node:unknown):ReactElement<{role?:string;children?:ReactNode}>[]=>{
      if(Array.isArray(node))return node.flatMap(statuses)
      if(!isValidElement<{role?:string;children?:ReactNode}>(node))return []
      return [...(node.props.role==="status"?[node]:[]),...statuses(node.props.children)]
    }
    assert.ok(statuses(tree).some(node=>node.props.children==="초기화 중"))
  } finally {unsubscribe();queryClient.clear();globalThis.fetch=previous}
})
