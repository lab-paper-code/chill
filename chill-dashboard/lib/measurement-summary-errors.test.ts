import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { runInNewContext } from "node:vm"
import * as React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ScriptTarget, ModuleKind, JsxEmit } from "typescript"
import * as summary from "./measurement-summary.ts"
import * as scheduler from "./scheduler.ts"
import * as sourceExport from "./source-export.ts"
import * as coverageHelpers from "./measurement-coverage.ts"
import * as assessment from "./measurement-assessment.ts"
import * as measurements from "./measurements.ts"
import * as requestHelpers from "./measurement-request.ts"

const require=createRequire(import.meta.url)
const container=({children}:{children?:React.ReactNode})=>React.createElement("div",null,children)
function load<T>(path:string,modules:Record<string,unknown>,extra:Record<string,unknown>={},expose=""):T {
  const source=readFileSync(new URL(path,import.meta.url),"utf8")+expose
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:Record<string,unknown>={}
  modules={"lucide-react":{TriangleAlert:()=>null,RefreshCw:()=>null,Braces:()=>null},...modules}
  runInNewContext(compiled,{exports,Error,URLSearchParams,...extra,require:(name:string)=>name in modules?modules[name]:name.startsWith("@/")||name.endsWith(".css")?{}:require(name)})
  return exports as T
}

test("summary decoding failures retain a common API error and visible cause for coverage, BEO and profiles",async()=>{
  const values:unknown[]=[0]
  for(let i=0;i<30;i++)values.push([`~${i}`,`~${i}`])
  const body=JSON.stringify({readProjection:{version:"measurement-summary.v1",values,shapes:[]},items:"~30"})
  let queryFn:((context:{signal:AbortSignal})=>Promise<unknown>)|undefined
  const capture=({queryFn:fn}:{queryFn:typeof queryFn})=>{queryFn=fn;return {error:null}}
  const client=load<{ApiError:new(...args:never[])=>Error;useChillQuery:(path:string,options:{decoder:(body:string)=>unknown})=>unknown}>("./client.tsx",{
    react:{...React,useContext:()=>({token:"test",config:{namespace:"ns"}}),useSyncExternalStore:()=>true},
    "@tanstack/react-query":{useQuery:capture},
    "@/lib/scheduler":scheduler,
    "@/lib/measurement-summary":summary,
  },{fetch:async()=>({ok:true,status:200,text:async()=>body,headers:new Headers()}),performance})
  const coverage=load<{useMeasurementCoverage:(namespace:string)=>unknown;CoverageError:React.ComponentType<{error:unknown;retry:()=>void}>}>("../features/measurement-coverage.tsx",{
    "@/lib/client":client,"@/lib/measurement-summary":summary,
    "@tanstack/react-query":{useQuery:capture},
    "next/navigation":{useSearchParams:()=>new URLSearchParams()},
    "next/link":{__esModule:true,default:container},
    "@/components/ui/button":{Button:container},
  },{fetch:async()=>({ok:true,status:200,text:async()=>body})})
  const region=load<{QueryGate:React.ComponentType<{query:unknown}>}>("../components/read-region.tsx",{
    "@/lib/client":client,"@/lib/scheduler":scheduler,"@/lib/source-export":sourceExport,
    "@/lib/labels":{explain:()=>["일반 조회 오류"],statusLabel:()=>""},
    "@/components/ui/button":{Button:container},
    "@/components/ui/dialog":Object.fromEntries(["Dialog","DialogContent","DialogDescription","DialogHeader","DialogTitle","DialogTrigger"].map(key=>[key,container])),
  })
  for(const kind of ["coverage","beo","profiles"]) {
    if(kind==="coverage")coverage.useMeasurementCoverage("ns")
    else client.useChillQuery(kind,{decoder:summary.parseMeasurementJSON})
    let error:Error&{status?:number;body?:string;data?:{code?:string}}|undefined
    try {await queryFn!({signal:new AbortController().signal})}catch(caught){error=caught as typeof error}
    assert.ok(error instanceof client.ApiError,kind)
    assert.equal(error?.status,0,kind)
    assert.equal(error?.data?.code,"MeasurementSummaryInvalid",kind)
    assert.match(error?.body??"",/측정 요약 확장 한도 초과/,kind)
    const html=renderToStaticMarkup(kind==="coverage"
      ?React.createElement(coverage.CoverageError,{error,retry:()=>{}})
      :React.createElement(region.QueryGate,{query:{error,isPending:false,isFetching:false,dataUpdatedAt:0,refetch:()=>{}}}))
    const headline=html.match(/role="alert"[^>]*><p[^>]*>(.*?)<\/p>/)?.[1]??""
    assert.match(headline,/측정 요약 확장 한도 초과/,kind)
    assert.doesNotMatch(html,/HTTP undefined|HTTP 0/,kind)
  }
})

test("paged request defaults and coverage timestamps render second-page summary rejection causes",async(t)=>{
  const client=load<{ApiError:new(status:number,body:string,data:unknown)=>Error;decodeResponse:(body:string,decoder:(body:string)=>unknown)=>unknown}>("./client.tsx",{
    "@/lib/scheduler":scheduler,"@/lib/measurement-summary":summary,
  })
  const region=load<{QueryGate:React.ComponentType<{query:unknown;children?:React.ReactNode}>}>("../components/read-region.tsx",{
    "@/lib/client":{...client,useSession:()=>({token:"test"})},"@/lib/scheduler":scheduler,"@/lib/source-export":sourceExport,
    "@/lib/labels":{explain:()=>["일반 조회 오류"],statusLabel:()=>""},
    "@/components/ui/button":{Button:container},
    "@/components/ui/dialog":Object.fromEntries(["Dialog","DialogContent","DialogDescription","DialogHeader","DialogTitle","DialogTrigger"].map(key=>[key,container])),
  })
  const values:unknown[]=[0]
  for(let i=0;i<30;i++)values.push([`~${i}`,`~${i}`])
  const invalid=JSON.stringify({readProjection:{version:"measurement-summary.v1",values,shapes:[]},profiles:"~30"})
  for(const kind of ["request","coverage"]) await t.test(kind,async()=>{
    const calls:string[]=[]
    const fetch=async(path:string)=>{calls.push(path);return {ok:true,status:200,headers:new Headers(),text:async()=>calls.length===1?JSON.stringify({profiles:[{id:"first-page",measuredAt:"2026-11-01T00:00:00Z"}],page:{nextOffset:50}}):invalid}}
    const pagedClient=load<typeof client>("./client.tsx",{
      "@/lib/scheduler":scheduler,"@/lib/measurement-summary":summary,
    },{fetch,performance})
    let queryFn:((context:{signal:AbortSignal})=>Promise<unknown>)|undefined
    let state:Record<string,unknown>={isPending:true,error:null,data:undefined,isFetching:false,dataUpdatedAt:0,refetch:()=>{}}
    const modules={
      react:{...React,useEffect:()=>{}},
      "next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({replace:()=>{}})},
      "next/link":{__esModule:true,default:container},
      "@tanstack/react-query":{useQuery:({queryFn:fn}:{queryFn:typeof queryFn})=>{queryFn=fn;return state}},
      "@/lib/client":{...pagedClient,useSession:()=>({token:"test",config:{namespace:"ns"}}),useChillQuery:(path:string)=>({data:path.startsWith("v1/workloads")?[]:{items:[]},error:null}),useChillMutation:()=>({isPending:false})},
      "@/lib/measurement-summary":summary,"@/lib/measurement-coverage":coverageHelpers,"@/lib/measurement-assessment":assessment,
      "@/lib/measurements":measurements,"@/lib/measurement-request":requestHelpers,
      "@/components/read-region":region,"@/components/screen":{Screen:container},
      "@/components/measurement-navigation":{MeasurementNavigation:()=>null,MeasurementEvidenceChecks:()=>null},
      "@/components/ui/button":{Button:container},"@/components/ui/input":{Input:()=>null},
      "@/features/measurement-coverage":{useCoverageInventory:()=>({data:{classes:[],models:[]}}),useMeasurementCoverage:()=>({}),useModelCompatibility:()=>({})},
    }
    let element:React.ReactElement
    if(kind==="request") {
      const page=load<{MeasurementRequestPage:React.ComponentType}>("../features/measurement-request.tsx",modules,{fetch})
      element=React.createElement(page.MeasurementRequestPage)
    } else {
      const page=load<{CoverageDetail:React.ComponentType<Record<string,unknown>>}>("../features/measurement-coverage.tsx",modules,{fetch},"\nexport { CoverageDetail }\n")
      const checklist={criteriaMet:null,version:"result-checklist.v1",checks:[]},scope={schedulerUsed:null,reused:false,facts:[]}
      const verdict={status:"usable",displayStatus:"usable",reasons:[],checklist,scope}
      const progress={target:[1],measured:[1],criteriaMet:[],missing:[],inProgress:[],unknown:[]}
      const row={id:"row",batch:1,profileId:"first-page",sourceProfileId:"",p95Milliseconds:null,totalJoulesPerItem:null,incrementalJoulesPerItem:null}
      const variant={id:"variant",condition:{},assessment:verdict,batchProgress:progress,resultRows:[row],requestIds:[],runIds:[],profileIds:["first-page"]}
      element=React.createElement(page.CoverageDetail,{cell:{id:"cell",model:{name:"model"},deviceClass:{name:"class"},executionPath:"ort-cpu",powerMode:"fixed",support:{verdict:"Compatible",reasons:[]},assessment:verdict,batchProgress:progress,variantCount:1,variants:[variant],failureSummary:[],history:[]},namespace:"ns",close:()=>{},visible:true})
    }
    renderToStaticMarkup(element)
    let error:Error&{body?:string;data?:{code?:string}}
    try {await queryFn!({signal:new AbortController().signal});assert.fail("second page must reject")}catch(caught){error=caught as typeof error}
    assert.equal(error.data?.code,"MeasurementSummaryInvalid",kind)
    assert.equal(calls.length,2,kind)
    assert.match(calls[1],/offset=50/,kind)
    state={...state,isPending:false,error}
    const html=renderToStaticMarkup(element)
    assert.match(html,/role="alert"/,kind)
    assert.match(html,/측정 요약 확장 한도 초과/,kind)
    assert.doesNotMatch(html,/HTTP 0|측정 시각 미기록|측정 시각 조회 중/,kind)
    if(kind==="request")assert.doesNotMatch(html,/배치 \(건\)|측정 요청 만들기/,kind)
  })
})
