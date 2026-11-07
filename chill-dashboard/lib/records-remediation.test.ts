import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement,type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule,ModuleKind,JsxEmit,ScriptTarget } from "typescript"
import nextConfig from "../next.config.ts"
import { dashboardDestination } from "./dashboard-routes.ts"
import * as refs from "./scheduler-history.ts"
import * as scheduler from "./scheduler.ts"
import * as identity from "./workload-identity.ts"
import * as live from "./live.ts"
import * as sourceExport from "./source-export.ts"
import type { ObservationResult } from "./read-types.ts"

const require=createRequire(import.meta.url)
const box=({children}:{children?:ReactNode})=>createElement("div",null,children)
function load(file:string,modules:Record<string,unknown>){const exports:Record<string,unknown>={};const compiled=transpileModule(readFileSync(new URL(file,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText;new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports);return exports}
function modulesFor(params:URLSearchParams,query:(path:string,options?:{enabled?:boolean})=>unknown):Record<string,unknown>{
  return {react:require("react"),"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:()=>{}}),usePathname:()=>"/records"},"next/link":{default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},"@/lib/client":{useSession:()=>({config:{pageSize:25,maxPageSize:100,maxWindowHours:24}}),useChillQuery:query},"@/lib/scheduler":scheduler,"@/lib/scheduler-history":refs,"@/lib/workload-identity":identity,"@/lib/read-types":{},"@/lib/date-time":{},"@/lib/live-display":{displayTime:(value:string)=>value},"@/features/scheduler":{judgmentLabels:{Keep:"현재 구성 유지"},actionNodeName:(node:{name:string})=>node.name,actionKinds:{route:"요청 분배 변경",unroute:"요청 분배 해제",release:"자원 예약 해제"}},"./scheduler":{actionNodeName:(node:{name:string})=>node.name,actionKinds:{route:"요청 분배 변경",unroute:"요청 분배 해제",release:"자원 예약 해제"},actionStatuses:{complete:"완료"}},"@/features/scheduler-settings":{},"./scheduler-transitions":{CurrentTransitionEvidence:()=>createElement("p",null,"현재 동작 대체")},"@/features/scheduler-history":{when:(value:string)=>value},"@/lib/source-export":{safeSource:(value:unknown)=>value},"@/lib/labels":{statusLabel:(value:string)=>value},"@/components/read-region":{QueryGate:box,SourceJSON:()=>null,SourceExport:()=>null},"@/components/screen":{Screen:box,Rows:box,Row:({href,label}:{href:string;label:string})=>createElement("a",{href},label),RowDialog:()=>null,Facts:({items}:{items:[string,ReactNode][]})=>createElement("dl",null,items.map(([key,value])=>createElement("div",{key},key,value))),Empty:()=>null},"@/components/records-navigation":{RecordsNavigation:()=>null,RecordsRange:()=>null},"@/components/ui/button":{Button:box},"@/components/ui/input":{},"@/components/ui/sheet":{Sheet:box,SheetContent:box,SheetHeader:box,SheetTitle:box},"@/components/chart-area-interactive":{},"@/components/stage-breakdown":{duration:(value:number)=>String(value)}}
}

test("scheduler changes cannot be intercepted by an unconditional Next redirect",async()=>{
  const rules=await nextConfig.redirects!()
  assert.equal(rules.some(rule=>rule.source==="/changes"&&!rule.has&&!rule.missing),false)
  const result=new URL(dashboardDestination("/changes",new URLSearchParams("scope=scheduler&from=exact&cursor=17"))!,"https://dashboard.invalid")
  assert.equal(result.pathname,"/records");assert.equal(result.searchParams.get("scope"),"scheduler");assert.equal(result.searchParams.get("filter"),"settings");assert.equal(result.searchParams.get("cursor"),"17")
})

test("power journal queries ignore workload UID while retaining its navigation context",()=>{
  const params=new URLSearchParams("kind=power&uid=service&from=2026-10-31T01:00:00.123456789Z&to=2026-10-31T02:00:00.987654321Z&limit=17&cursor=81"),calls:string[]=[]
  const modules=modulesFor(params,(path,options)=>{if(options?.enabled===false)return {};if(path==="v1/workloads?view=summary")return {data:[]};calls.push(path);return {data:{events:[]},isSuccess:true}})
  modules["./scheduler-workload-states"]=load("../features/scheduler-workload-states.tsx",modules)
  modules["@/lib/live"]=live
  const exports=load("../features/scheduler-history.tsx",modules)
  renderToStaticMarkup(createElement(exports.SchedulerHistoryPage as ()=>ReactNode))
  const query=new URL(calls[0],"https://dashboard.invalid").searchParams
  assert.equal(query.has("uid"),false);assert.equal(params.get("uid"),"service")
  for(const key of ["from","to","limit","cursor"])assert.equal(query.get(key),params.get(key))
})

test("request and management navigation preserves all repeated cohort filters",()=>{
  for(const changes of [false,true]){
    const params=new URLSearchParams(`kind=${changes?"judgments":"requests"}&scope=${changes?"management":"requests"}&uid=service&from=start&to=end&limit=17&cursor=opaque&definition=a&definition=b&outcome=completed&outcome=timeout`)
    const modules=modulesFor(params,path=>({data:path==="v1/workloads?view=summary"?[]:{uid:"service",definitions:[],outcomes:{},requests:[],management:[],cohorts:[],total:0}}))
    const exports=load("../features/observations.tsx",modules)
    const html=renderToStaticMarkup(createElement(exports.ObservationsPage as (props:{changes:boolean})=>ReactNode,{changes}))
    const targetLabel=changes?"같은 기간 서비스 요청":"같은 기간 서비스 변경"
    const link=[...html.matchAll(/href="([^"]+)"[^>]*>([^<]+)<\/a>/g)].find(match=>match[2]===targetLabel)!
    const query=new URL(link[1].replaceAll("&amp;","&"),"https://dashboard.invalid").searchParams
    for(const key of ["uid","from","to","limit","cursor","definition","outcome"])assert.deepEqual(query.getAll(key),params.getAll(key))
  }
})

test("incomplete historical references never mount current-state evidence",()=>{
  for(const search of ["view=history&id=same&from=start&to=end","view=history&id=same&node=node","id=same&offset=17&from=start&to=end"]){
    const modules=modulesFor(new URLSearchParams(search),path=>({data:path==="v1/workloads?view=summary"?[]:{events:[]},isSuccess:true}))
    modules["./scheduler-workload-states"]=load("../features/scheduler-workload-states.tsx",modules)
    modules["@/lib/live"]=live
    const exports=load("../features/scheduler-history.tsx",modules)
    const html=renderToStaticMarkup(createElement(exports.SchedulerHistoryPage as ()=>ReactNode))
    assert.match(html,/기록 식별자 미확인/);assert.doesNotMatch(html,/현재 동작 대체/)
  }
})

test("identity changes retain request and power API kinds and their own cursor",async()=>{
  const previousWindow=Object.getOwnPropertyDescriptor(globalThis,"window")
  try{
    for(const kind of ["requests","power","action"]){
      const params=new URLSearchParams({kind,cursor:kind==="requests"?"opaque-request":"81",offset:"17",at:"event",id:"same",aOffset:"2",aKind:"decision"}),urls:string[]=[],callbacks:((value:string)=>Promise<void>)[]=[]
      Object.defineProperty(globalThis,"window",{configurable:true,value:{location:{href:`https://dashboard.invalid/records?${params}`}}})
      let state=0
      class Client {async cancelQueries(){}clear(){}getDefaultOptions(){return {}}}
      const modules={react:{createContext:()=>({}),useState:(initial:unknown)=>{const index=state++;return [index===2?"old-token":typeof initial==="function"?(initial as ()=>unknown)():initial,()=>{}]},useEffect:()=>{},useCallback:(callback:(value:string)=>Promise<void>)=>{callbacks.push(callback);return callback}},"@tanstack/react-query":{QueryClient:Client,QueryCache:class{},MutationCache:class{}},"next/navigation":{useRouter:()=>({replace:(value:string)=>urls.push(value)})},"@/components/ui/button":{},"@/components/ui/input":{},"@/components/ui/dialog":{},"@/components/brand":{},"lucide-react":{},"@/lib/scheduler":{},"@/lib/session-token":{}}
      Object.assign(modules,{"@/lib/measurement-summary":{}})
      const exports=load("../lib/client.tsx",modules)
      ;(exports.SessionProvider as (props:{children:null})=>unknown)({children:null})
      await callbacks[0]("new-token")
      const next=new URL(urls[0],"https://dashboard.invalid").searchParams
      assert.equal(next.get("kind"),kind==="action"?null:kind)
      assert.equal(next.get("cursor"),kind==="action"?null:params.get("cursor"))
      for(const key of ["offset","at","id","aOffset","aKind"])assert.equal(next.has(key),false)
    }
  }finally{if(previousWindow)Object.defineProperty(globalThis,"window",previousWindow);else Reflect.deleteProperty(globalThis,"window")}
})

test("server route, unroute and release results render declared and actual routing with operation provenance",()=>{
  const at="2026-10-31T01:00:00.123456789Z",state={at,node:{name:"",uid:""},powerMode:"",instances:null,native:null,pods:null,states:null}
  for(const kind of ["route","unroute","release"])for(const historical of [false,true]){
    const declared=kind==="route"?{service:{first:3,second:1}}:kind==="unroute"?{service:{}}:{}
    const operation={id:`manual-${kind}:record`,kind,node:state.node,workloadUID:"service",staticRouting:declared,settingsValidatedAt:at,reason:`선택한 ${kind} 근거`,actorUsername:`operator-${kind}`}
    const result={operation,before:state,actual:{...state,routing:{service:{first:1,second:1}}},startedAt:at,endedAt:at,status:kind==="unroute"?"partial":"complete",progress:null}
    const calls:string[]=[],params=new URLSearchParams({id:operation.id,view:"current",stage:"result",node:"",startedAt:at})
    const modules=modulesFor(params,path=>{calls.push(path);return {data:{results:[result],actions:[]}}})
    const exports=load("../features/scheduler-transitions.tsx",modules)
    const html=historical?renderToStaticMarkup(createElement(exports.HistoryActionDetails as (props:{row:unknown})=>ReactNode,{row:{event:{at,action:{id:operation.id,kind,node:state.node,phase:result.status,startedAt:at,actual:result}},ref:{from:at,to:at,offset:"17",kind:"action",at,id:operation.id}}})):renderToStaticMarkup(createElement(exports.CurrentTransitionEvidence as ()=>ReactNode))
    for(const label of ["설정한 비율","실제 비율","50%",operation.reason,operation.actorUsername])assert.ok(html.includes(label),`${kind}: ${label}`)
    if(kind==="route"){assert.match(html,/75%/);assert.match(html,/25%/)}
    assert.deepEqual(calls,historical?[]:["v1/scheduler/state"])
  }
})

test("selected routing provenance is never borrowed from another journal event",()=>{
  const at="2026-10-31T01:00:00.123456789Z",state={at,node:{name:"",uid:""},instances:null,native:null,pods:null,states:null}
  for(const stored of [false,true]){
    const operation={id:"manual-route:record",kind:"route",node:state.node,workloadUID:"service",staticRouting:{service:{first:1}},...(stored?{settingsValidatedAt:at,reason:"operation-only-reason",actorUsername:"operation-only-actor"}:{})}
    const result={operation,before:state,actual:{...state,routing:{service:{first:1}}},startedAt:at,endedAt:at,status:"complete",progress:null}
    const calls:string[]=[],params=new URLSearchParams({id:operation.id,view:"current",stage:"result",startedAt:at})
    const modules=modulesFor(params,path=>{calls.push(path);return {data:path==="v1/scheduler/state"?{results:[result],actions:[]}:{events:[{settings:{validatedAt:at,afterStaticRouting:operation.staticRouting,actorUsername:"wrong-journal-actor",reason:"wrong-journal-reason"}}]}}})
    const exports=load("../features/scheduler-transitions.tsx",modules),html=renderToStaticMarkup(createElement(exports.CurrentTransitionEvidence as ()=>ReactNode))
    assert.deepEqual(calls,["v1/scheduler/state"])
    assert.doesNotMatch(html,/wrong-journal/)
    assert.equal(html.includes("operation-only-reason"),stored)
    assert.equal(html.includes("operation-only-actor"),stored)
    if(!stored)assert.match(html,/미기록/)
  }
})

test("public request reads and the real download preserve optional server timing and exact nanoseconds",async()=>{
  const body='{"uid":"service","from":"2026-11-08T00:00:00.123456789Z","to":"2026-11-08T01:00:00.987654321Z","definitionFilter":[],"outcomeFilter":[],"total":3,"selected":3,"outcomes":{"completed":3},"cohorts":[],"definitions":[],"management":[],"quantileMethod":"nearest-rank","samplePopulation":"server cohort","requests":[{"kind":"final","requestID":"timed","receivedAt":"2026-11-08T00:00:01.123456789Z","outcome":"completed","elapsedNanos":9223372036854775807,"serverTiming":[{"stage":"responseFlushDone","elapsedNanos":9223372036854775807},{"stage":"receiveLockAttempt","elapsedNanos":0},{"stage":"workerHTTPDone","elapsedNanos":9007199254740993}]},{"kind":"final","requestID":"old","receivedAt":"2026-11-08T00:00:02.123456789Z","outcome":"completed"},{"kind":"final","requestID":"empty","receivedAt":"2026-11-08T00:00:03.123456789Z","outcome":"completed","serverTiming":[]}]}'
  const params=new URLSearchParams("kind=requests&uid=service&from=2026-11-08T00:00:00.123456789Z&to=2026-11-08T01:00:00.987654321Z&limit=17")
  const queries=new Map<string,{queryFn:(context:{signal:AbortSignal})=>Promise<unknown>}>(),results=new Map<string,unknown>()
  const modules=modulesFor(params,()=>({}))
  const client=load("./client.tsx",{
    ...modules,
    react:{...require("react"),useContext:()=>({token:"test-token",config:{pollIntervalMs:5000}}),useSyncExternalStore:()=>true},
    "@tanstack/react-query":{useQuery:(options:{queryKey:string[];queryFn:(context:{signal:AbortSignal})=>Promise<unknown>})=>{
      const path=options.queryKey[1];queries.set(path,options);return {data:results.get(path)}
    }},
    "@/components/brand":{},"@/lib/session-token":{},"@/lib/measurement-summary":{},
  })
  modules["@/lib/client"]={...(modules["@/lib/client"] as object),useChillQuery:client.useChillQuery}
  let download:(()=>void)|undefined
  const readRegion=load("../components/read-region.tsx",{
    ...modules,"@/lib/source-export":sourceExport,
    "@/components/ui/dialog":{},
    "@/components/ui/button":{Button:({onClick,children}:{onClick:()=>void;children:ReactNode})=>{download=onClick;return createElement("button",null,children)}},
  })
  modules["@/components/read-region"]={...(modules["@/components/read-region"] as object),SourceExport:readRegion.SourceExport}
  modules["@/components/screen"]={...(modules["@/components/screen"] as object),RowDialog:({label,children}:{label:string;children:ReactNode})=>label==="자료 내보내기"?children:null}
  const page=load("../features/observations.tsx",modules).ObservationsPage as ()=>ReactNode
  const previousFetch=globalThis.fetch,previousDocument=Object.getOwnPropertyDescriptor(globalThis,"document")
  const previousCreate=URL.createObjectURL,previousRevoke=URL.revokeObjectURL
  const calls:string[]=[]
  let blob:Blob|undefined,clicks=0
  globalThis.fetch=async(input)=>{const path=String(input);calls.push(path);return new Response(path.startsWith("/api/chill/v1/observations/workloads/")?body:"[]",{headers:{"Content-Type":"application/json"}})}
  Object.defineProperty(globalThis,"document",{configurable:true,value:{createElement:()=>({href:"",download:"",click(){clicks++}})}})
  URL.createObjectURL=value=>{if(value instanceof Blob)blob=value;return "blob:request-timing"}
  URL.revokeObjectURL=()=>{}
  try {
    renderToStaticMarkup(createElement(page))
    for(const [path,query] of queries)results.set(path,await query.queryFn({signal:new AbortController().signal}))
    const requestPath=[...queries.keys()].find(path=>path.startsWith("v1/observations/workloads/"))!
    assert.equal(new URL(requestPath,"https://dashboard.invalid").searchParams.get("from"),params.get("from"))
    assert.ok(calls.includes(`/api/chill/${requestPath}`))
    const data=results.get(requestPath) as ObservationResult
    assert.deepEqual(data.requests[0].serverTiming,[{stage:"responseFlushDone",elapsedNanos:9223372036854775807n},{stage:"receiveLockAttempt",elapsedNanos:0},{stage:"workerHTTPDone",elapsedNanos:9007199254740993n}])
    assert.equal(data.requests[0].elapsedNanos,9223372036854775807n)
    assert.equal(data.requests[0].receivedAt,"2026-11-08T00:00:01.123456789Z")
    assert.equal(Object.hasOwn(data.requests[1],"serverTiming"),false)
    assert.deepEqual(data.requests[2].serverTiming,[])
    renderToStaticMarkup(createElement(page))
    assert.ok(download)
    download()
    assert.equal(clicks,1)
    assert.ok(blob)
    assert.equal(blob.type,"application/json")
    const exported=scheduler.parseSchedulerJSON(await blob.text()) as {source:ObservationResult}
    assert.deepEqual(exported.source,scheduler.parseSchedulerJSON(body))
  } finally {
    globalThis.fetch=previousFetch;URL.createObjectURL=previousCreate;URL.revokeObjectURL=previousRevoke
    if(previousDocument)Object.defineProperty(globalThis,"document",previousDocument)
    else Reflect.deleteProperty(globalThis,"document")
  }
})
