import assert from "node:assert/strict"
import { test } from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import * as React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as live from "./live.ts"
import * as display from "./live-display.ts"
import { decisionRange } from "./experiments.ts"

const at="2026-11-03T03:12:21.898101405Z",now=Date.parse(at)
const precheck:live.LiveEvent={type:"event",seq:1,at,kind:"action",action:{id:"dry-run",kind:"measurement-post",node:{uid:"",name:""},workloadUID:"",status:"NOT_SUBMITTED",startedAt:at,completedAt:at,expectedSeconds:null,expectedSource:null,phase:"failed",result:{status:"NOT_SUBMITTED"}}}
const realFailure:live.LiveEvent={...precheck,seq:2,action:{...precheck.action,id:"real-error",node:{uid:"n",name:"장치"},status:"failed",result:{status:"failed",error:"MeasurementPreflightRejected"}}}
const issue=(index:number):live.LiveEvent=>({type:"event",seq:index+10,at,kind:"cluster",cluster:{kind:"issue",at,event:"raised",pod:{uid:`pod-${index}`,name:`eval-${index}`},source:"observer",reason:"Pod is not labelled as a serving instance"}})

function harness(events:live.LiveEvent[]=[]) {
  const state:live.LiveState={...live.initialLiveState(),status:"fixture",events,snapshot:{workloads:[],nodes:[],instances:[],routing:{},decision:null,actions:[],reservations:[]}}
  let params=new URLSearchParams()
  let archived:live.LiveEvent[]=[]
  const require=createRequire(import.meta.url),wrap=({children}:{children?:React.ReactNode})=>React.createElement("div",{},children)
  const modules:Record<string,unknown>={
    react:React,"next/link":{default:({children,...props}:Record<string,unknown>)=>React.createElement("a",props,children as React.ReactNode)},
    "next/navigation":{usePathname:()=>"/",useSearchParams:()=>params,useRouter:()=>({replace:()=>{},push:()=>{}})},
    "@/lib/client":{useSession:()=>({epoch:1,config:{liveFixture:false}}),useChillQuery:(path:string)=>path.startsWith("v1/observations/events")?{data:{events:archived}}:{}},
    "@/lib/use-live":{useLive:()=>({state,status:"fixture",reconnect:()=>{}})},
    "@/lib/live":live,"@/lib/live-display":display,"@/lib/experiments":{decisionRange},
    "@/lib/scheduler":{stringifySchedulerJSON:JSON.stringify},"@/lib/source-export":{safeSource:(value:unknown)=>value},
    "./scheduler":{actionKinds:{},actionStatuses:{},actionNodeName:(node:{name:string})=>node.name||"전체 노드",judgmentLabels:{Keep:"유지"},SchedulerEvidence:()=>null},
    "@/components/status-cell":{StatusCell:({label}:{label:string})=>React.createElement("span",{},label)},"@/lib/status":{STATUS_AXES:{}},
    "@/components/ui/sheet":{Sheet:({open,children}:{open:boolean;children:React.ReactNode})=>open?wrap({children}):null,SheetContent:wrap,SheetHeader:wrap,SheetTitle:wrap},
    "./evaluation-active":{EvaluationStatusCell:()=>null},"@/lib/evaluation-active":{evaluationInjectionMatch:()=>false},"./power-series":{LivePowerTrend:()=>null},"./live-board.css":{},
  }
  const compile=(path:string)=>{
    const exports:Record<string,React.ComponentType<Record<string,unknown>>>={}
    const source=readFileSync(new URL(path,import.meta.url),"utf8")
    const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
    new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
    return exports
  }
  const board=compile("../features/live-board.tsx")
  modules["./live-board"]=board
  modules["./scheduler-history"]={SchedulerHistoryPage:()=>React.createElement("div",{},"기간 기록")}
  modules["./observations"]={ObservationsPage:()=>null}
  modules["./scheduler-evidence"]={SchedulerPowerPage:()=>null,ComparisonEvidence:()=>null}
  return {state,archive:(events:live.LiveEvent[])=>{archived=events},select:(value:URLSearchParams)=>{params=value},board:()=>renderToStaticMarkup(React.createElement(board.LiveBoardPage)),records:()=>renderToStaticMarkup(React.createElement(compile("../features/records.tsx").RecordsPage))}
}

test("R19 dry-run is a neutral precheck, not a failure, configuration change or anonymous device",()=>{
  const view=harness([precheck])
  let html=view.board()
  assert.match(html,/측정 사전 검사/)
  assert.match(html,/사전 검사 · 미제출/)
  assert.match(html,/data-label="보관 중 실패 건">0</)
  assert.equal(display.deviceAlerts(null,[precheck]).length,0)
  view.select(new URLSearchParams({open:`event:${JSON.stringify(["action",at,"dry-run",""])}`}))
  html=view.board()
  assert.doesNotMatch(html,/href="\/operations\/devices\?node="/)
  assert.match(html,/<th scope="row">종류<\/th><td>측정 요청<\/td>/)
  view.state.events=[realFailure]
  view.select(new URLSearchParams())
  assert.match(view.board(),/data-label="보관 중 실패 건">1</)
  assert.equal(display.deviceAlerts(null,[realFailure]).length,1)
  const knownPrecheck={...precheck,action:{...precheck.action,node:{uid:"n",name:"장치"}}}
  assert.equal(display.deviceAlerts(null,[realFailure,knownPrecheck]).length,1,"prechecks cannot clear an earlier real error")
  assert.equal(display.isActionFailure({...precheck.action,result:{status:"NOT_SUBMITTED",error:"PreflightRejected"}}),true,"real inspection errors stay failures")
})

test("R19 nonserving pod seeds group before limiting without hiding real service issues",()=>{
  const real={...issue(99),cluster:{...issue(99).cluster!,reason:"Serving instance readiness lost"}} as live.LiveEvent
  const events=[real,...Array.from({length:66},(_,index)=>issue(index))]
  const rows=display.incidentFlow(events,[],now)
  assert.equal(rows.length,2)
  const group=rows.find(row=>row.kind==="event"&&row.count===66)
  assert.ok(group&&group.kind==="event")
  assert.equal(group.members.length,66)
  const html=harness(events).board()
  assert.match(html,/비서빙 파드/)
  assert.match(html,/×66/)
  assert.doesNotMatch(html,/접속 후 수신/)
})

test("R19 decision count names its session scope and retention stays inside the live header",()=>{
  const view=harness()
  view.state.resumeHistory={reason:"checkpoint-unmatched",replayed:false,requested:{afterSeq:0,throughSeq:999},retained:{events:null,power:null},gaps:[{fromSeq:1,toSeq:999,scope:"events",reason:"NoRetainedFrames"}]}
  view.state.eventArchive={from:at,to:at,partial:true,coverage:{from:null,to:null,sources:[],gaps:[{from:"2026-11-03T03:10:00Z",to:"2026-11-03T03:11:00Z",stream:"cluster",reason:"BeforeRetention"}]}}
  const html=view.board(),header=html.slice(html.indexOf('<header class="live-heading"'),html.indexOf('</header>'))
  assert.match(html,/최근 5분 · 현재 접속 수신 판단/)
  assert.match(header,/보존 공백/)
  assert.match(header,/12:10:00 KST.*12:11:00 KST/)
  assert.match(header,/최근 사건 일부만 표시/)
  assert.doesNotMatch(header,/999|손실.*건|누락.*건/)
  view.state.eventArchive=null
  assert.equal(display.retentionNotice(view.state,"KST"),"보존 공백 · 기간 미제공")
})

test("R19 issue-to-records retains exact selected issue and period, not unrelated Keep history only",()=>{
  const event=issue(1),identity=JSON.stringify(["cluster",at,event.cluster!.pod!.uid,"raised"])
  const view=harness([event])
  view.select(new URLSearchParams({open:`event:${identity}`}))
  const html=view.board(),hrefs=[...html.matchAll(/href="([^"]+)"/g)].map(match=>match[1].replaceAll("&amp;","&"))
  const href=hrefs.find(value=>value.startsWith("/records?")&&new URL(value,"http://local").searchParams.has("open"))
  assert.ok(href)
  const params=new URL(href,"http://local").searchParams
  assert.equal(params.get("open"),`event:${identity}`)
  assert.equal(params.get("eventAt"),at)
  assert.equal(Date.parse(params.get("to")!)-Date.parse(params.get("from")!),600000)
  view.select(params)
  assert.match(view.records(),/서비스 인스턴스가 아닌 파드/)
  assert.match(view.records(),/선택 사건/)
  view.state.events=[]
  view.archive([event])
  assert.match(view.records(),/서비스 인스턴스가 아닌 파드/,"reload restores the exact issue from GET")
  view.archive([])
  assert.match(view.records(),/보관 범위 밖 사건/,"unavailable archive events are not fabricated")
})

test("R19 new contracts: committed measurement WS frames reach neutral, success and failure rows",()=>{
  const view=harness()
  const snapshot=live.parseLiveMessage({type:"snapshot",seq:0,at,...view.state.snapshot})
  assert.ok(snapshot)
  Object.assign(view.state,live.applyLiveMessage(view.state,snapshot,now))
  const frames=[
    {...precheck,seq:1,action:{...precheck.action,status:"validated",phase:"started",result:undefined}},
    {...precheck,seq:2,action:{...precheck.action,phase:"completed"}},
  ]
  for(const frame of frames){
    const parsed=live.parseLiveMessage(frame)
    assert.ok(parsed)
    Object.assign(view.state,live.applyLiveMessage(view.state,parsed,now))
  }
  assert.equal(view.state.events.length,2)
  let html=view.board()
  assert.match(html,/측정 사전 검사/)
  assert.match(html,/사전 검사 · 미제출/)
  assert.match(html,/data-label="보관 중 실패 건">0</)
  assert.equal(display.deviceAlerts(null,view.state.events).length,0)
  const error=live.parseLiveMessage({...realFailure,seq:3,action:{...realFailure.action,status:"NOT_SUBMITTED",result:{status:"NOT_SUBMITTED",error:"PreflightRejected"}}})
  assert.ok(error)
  Object.assign(view.state,live.applyLiveMessage(view.state,error,now))
  html=view.board()
  assert.match(html,/data-label="보관 중 실패 건">1</)
  const success=live.parseLiveMessage({...precheck,seq:4,action:{...precheck.action,id:"submitted",status:"complete",phase:"completed",result:{status:"complete"}}})
  assert.ok(success)
  Object.assign(view.state,live.applyLiveMessage(view.state,success,now))
  assert.match(view.board(),/data-label="결과">완료<\/td>/)
})
