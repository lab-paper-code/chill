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
import * as experiments from "./experiments.ts"

const require=createRequire(import.meta.url)
const box=({children}:{children?:ReactNode})=>createElement("div",null,children)
function load(file:string,modules:Record<string,unknown>){const exports:Record<string,unknown>={};const compiled=transpileModule(readFileSync(new URL(file,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText;new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports);return exports}
function modulesFor(params:URLSearchParams,query:(path:string,options?:{enabled?:boolean})=>unknown):Record<string,unknown>{
  return {react:require("react"),"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:()=>{}}),usePathname:()=>"/records"},"next/link":{default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},"@/lib/client":{useSession:()=>({config:{pageSize:25,maxPageSize:100,maxWindowHours:24}}),useChillQuery:query},"@/lib/scheduler":scheduler,"@/lib/scheduler-history":refs,"@/lib/workload-identity":identity,"@/lib/experiments":experiments,"@/lib/read-types":{},"@/lib/date-time":{},"@/lib/live-display":{displayTime:(value:string)=>value},"@/features/scheduler":{judgmentLabels:{Keep:"현재 구성 유지"},actionNodeName:(node:{name:string})=>node.name,actionKinds:{"manual-static-routing":"설정한 분배"}},"./scheduler":{actionNodeName:(node:{name:string})=>node.name,actionKinds:{"manual-static-routing":"설정한 분배"},actionStatuses:{complete:"완료"}},"@/features/scheduler-settings":{},"./scheduler-transitions":{CurrentTransitionEvidence:()=>createElement("p",null,"현재 동작 대체")},"@/features/scheduler-history":{when:(value:string)=>value},"@/lib/source-export":{safeSource:(value:unknown)=>value},"@/lib/labels":{statusLabel:(value:string)=>value},"@/components/read-region":{QueryGate:box,SourceJSON:()=>null,SourceExport:()=>null},"@/components/screen":{Screen:box,Rows:box,Row:({href,label}:{href:string;label:string})=>createElement("a",{href},label),RowDialog:()=>null,Facts:({items}:{items:[string,ReactNode][]})=>createElement("dl",null,items.map(([key,value])=>createElement("div",{key},key,value))),Empty:()=>null},"@/components/records-navigation":{RecordsNavigation:()=>null,RecordsRange:()=>null},"@/components/ui/button":{Button:box},"@/components/ui/input":{},"@/components/ui/sheet":{Sheet:box,SheetContent:box,SheetHeader:box,SheetTitle:box},"@/components/chart-area-interactive":{},"@/components/stage-breakdown":{duration:(value:number)=>String(value)}}
}

test("scheduler changes cannot be intercepted by an unconditional Next redirect",async()=>{
  const rules=await nextConfig.redirects!()
  assert.equal(rules.some(rule=>rule.source==="/changes"&&!rule.has&&!rule.missing),false)
  const result=new URL(dashboardDestination("/changes",new URLSearchParams("scope=scheduler&from=exact&cursor=17"))!,"https://dashboard.invalid")
  assert.equal(result.pathname,"/records");assert.equal(result.searchParams.get("scope"),"scheduler");assert.equal(result.searchParams.get("filter"),"settings");assert.equal(result.searchParams.get("cursor"),"17")
})

test("power journal queries ignore workload UID while retaining its navigation context",()=>{
  const params=new URLSearchParams("kind=power&uid=service&from=2026-10-31T01:00:00.123456789Z&to=2026-10-31T02:00:00.987654321Z&limit=17&cursor=81"),calls:string[]=[]
  const modules=modulesFor(params,(path,options)=>{if(options?.enabled===false)return {};calls.push(path);return {data:{events:[]},isSuccess:true}})
  const exports=load("../features/scheduler-history.tsx",modules)
  renderToStaticMarkup(createElement(exports.SchedulerHistoryPage as ()=>ReactNode))
  const query=new URL(calls[0],"https://dashboard.invalid").searchParams
  assert.equal(query.has("uid"),false);assert.equal(params.get("uid"),"service")
  for(const key of ["from","to","limit","cursor"])assert.equal(query.get(key),params.get(key))
})

test("request and management navigation preserves all repeated cohort filters",()=>{
  for(const changes of [false,true]){
    const params=new URLSearchParams(`kind=${changes?"judgments":"requests"}&scope=${changes?"management":"requests"}&uid=service&from=start&to=end&limit=17&cursor=opaque&definition=a&definition=b&outcome=completed&outcome=timeout`)
    const modules=modulesFor(params,path=>({data:path==="v1/workloads"?[]:{uid:"service",definitions:[],outcomes:{},requests:[],management:[],cohorts:[],total:0}}))
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
    const modules=modulesFor(new URLSearchParams(search),()=>({data:{events:[]},isSuccess:true}))
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

test("current and historical static-routing evidence compares declared and actual shares and exact settings provenance",()=>{
  const at="2026-10-31T01:00:00.123456789Z",declared={service:{first:3,second:1}},state={at,node:{name:"node",uid:"node"},powerMode:"",instances:null,native:null,pods:null,states:null},operation={id:"split",kind:"manual-static-routing",node:state.node,staticRouting:declared,settingsValidatedAt:at},result={operation,before:state,actual:{...state,routing:{service:{first:1,second:1}}},startedAt:at,endedAt:at,status:"complete",progress:null}
  for(const historical of [false,true]){
    const calls:string[]=[],params=new URLSearchParams({id:"split",view:"current",stage:"result",node:"node",startedAt:at})
    const modules=modulesFor(params,path=>{calls.push(path);return {data:path==="v1/scheduler/state"?{results:[result],actions:[]}:path==="v1/workloads"?[]:{events:[{at,settings:{validatedAt:at,afterStaticRouting:declared,reason:"선택한 분배 근거",actorUsername:"operator-exact"}}]},isSuccess:true}})
    const exports=load("../features/scheduler-transitions.tsx",modules)
    const html=historical?renderToStaticMarkup(createElement(exports.HistoryActionDetails as (props:{row:unknown})=>ReactNode,{row:{event:{at,action:{id:"split",kind:"manual-static-routing",node:state.node,phase:"complete",startedAt:at,actual:result}},ref:{from:at,to:at,offset:"17",kind:"action",at,id:"split"}}})):renderToStaticMarkup(createElement(exports.CurrentTransitionEvidence as ()=>ReactNode))
    for(const label of ["설정한 비율","실제 비율","75%","25%","50%","선택한 분배 근거","operator-exact"])assert.ok(html.includes(label),label)
    const source=calls.find(path=>path.startsWith("v1/observations/scheduler?"))!
    assert.equal(new URL(source,"https://dashboard.invalid").searchParams.get("from"),at)
    assert.equal(new URL(source,"https://dashboard.invalid").searchParams.get("to"),"2026-10-31T01:00:00.123456790Z")
  }
})

test("static-routing provenance rejects another declaration or validation timestamp and follows exact-window pages",()=>{
  const at="2026-10-31T01:00:00.123456789Z",declared={service:{first:3,second:1}},state={at,node:{name:"node",uid:"node"},instances:null,native:null,pods:null,states:null},operation={id:"split",kind:"manual-static-routing",node:state.node,staticRouting:declared,settingsValidatedAt:at},result={operation,before:state,actual:{...state,routing:declared},startedAt:at,endedAt:at,status:"complete",progress:null}
  for(const available of [false,true]){
    const params=new URLSearchParams({id:"split",view:"current",stage:"result",node:"node",startedAt:at}),calls:string[]=[]
    const modules=modulesFor(params,path=>{
      calls.push(path)
      if(path==="v1/scheduler/state")return {data:{results:[result],actions:[]}}
      const page=new URL(path,"https://dashboard.invalid").searchParams.get("cursor")
      return {data:page?{events:available?[{settings:{validatedAt:at,afterStaticRouting:declared,actorUsername:"exact-author",reason:"exact-reason"}}]:[]}:{events:[{settings:{validatedAt:at,afterStaticRouting:{service:{first:1}},actorUsername:"wrong-routing",reason:"wrong-routing-reason"}},{settings:{validatedAt:"2026-10-31T01:00:00.123456788Z",afterStaticRouting:declared,actorUsername:"wrong-time",reason:"wrong-time-reason"}}],nextCursor:"100"}}
    })
    const exports=load("../features/scheduler-transitions.tsx",modules),html=renderToStaticMarkup(createElement(exports.CurrentTransitionEvidence as ()=>ReactNode))
    assert.doesNotMatch(html,/wrong-routing|wrong-time/)
    assert.equal(html.includes("exact-author"),available)
    assert.ok(calls.some(path=>path.includes("cursor=100")))
    if(!available)assert.match(html,/미기록/)
  }
})
