import assert from "node:assert/strict"
import test from "node:test"
import * as React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import * as live from "./live.ts"
import * as display from "./live-display.ts"
import * as history from "./scheduler-history.ts"
import * as scheduler from "./scheduler.ts"
import { fixtureMessages } from "./live-fixture.ts"
import { hookRunner,load,type Tree } from "./evaluation-author-test-helpers.ts"

test("service decision evidence stays at opening selection until the user selects another decision",()=>{
  const at="2026-11-04T10:00:00.000Z"
  let state=live.initialLiveState(),params=new URLSearchParams(),active=hookRunner()
  for(const frame of fixtureMessages("first",at))state=live.applyLiveMessage(state,live.parseLiveMessage(frame)!,Date.parse(at))
  const initial=state.snapshot!.decision!.at,calls:string[]=[],buttons:Tree[]=[],mounted=new Map<string,typeof active>()
  let recorded:unknown=undefined
  const wrap=({children}:{children:React.ReactNode})=>children
  const modules:Record<string,unknown>={
    react:{...React,useState:(value:unknown)=>active.react.useState(value),useRef:(value:unknown)=>active.react.useRef(value),useEffect:()=>{},useCallback:(value:unknown)=>value,useMemo:(value:()=>unknown)=>value()},
    "next/link":{default:"a"},"next/navigation":{usePathname:()=>"/",useSearchParams:()=>params,useRouter:()=>({push:(path:string)=>{params=new URL(path,"https://local").searchParams},replace:()=>{}})},
    "@/lib/client":{useSession:()=>({epoch:1,config:{liveFixture:false}}),useChillQuery:(path:string,options?:{poll?:unknown})=>{
      if(path.startsWith("v1/observations/scheduler")){
        assert.ok(!options?.poll,"selected historical evidence has no polling timer")
        calls.push(path)
        return {data:{events:recorded?[recorded]:[]}}
      }
      return {}
    }},
    "@/lib/use-live":{useLive:()=>({state,status:"fixture",reconnect:()=>{}})},"@/lib/live":live,"@/lib/live-display":display,"@/lib/scheduler-history":history,"@/lib/scheduler":scheduler,"@/lib/source-export":{safeSource:(value:unknown)=>value},
    "./scheduler":{actionKinds:{},actionStatuses:{},actionNodeName:(node:{name:string})=>node.name,judgmentLabels:{Ready:"준비됨"},SchedulerEvidence:()=>null},
    "@/components/status-cell":{StatusCell:({label}:{label:string})=>React.createElement("span",{},label)},"@/lib/status":{STATUS_AXES:{}},
    "@/components/ui/tooltip":{Tooltip:wrap,TooltipTrigger:wrap,TooltipContent:()=>null},
    "@/components/ui/sheet":{Sheet:({open,children}:{open:boolean;children:React.ReactNode})=>open?children:null,SheetContent:wrap,SheetHeader:wrap,SheetTitle:wrap},
    "./evaluation-active":{EvaluationStatusCell:()=>null},"@/lib/evaluation-active":{evaluationInjectionMatch:()=>false},"./power-series":{LivePowerTrend:()=>null},"./live-board.css":{},
  }
  modules["./observation-age.css"]={}
  modules["./serving-instance.css"]={}
  modules["@/components/observation-age"]=load("../components/observation-age.tsx",modules)
  modules["@/components/serving-instance"]=load("../components/serving-instance.tsx",modules)
  modules["./scheduler-workload-states"]=load("../features/scheduler-workload-states.tsx",modules)
  const {LiveBoardPage}=load("../features/live-board.tsx",modules)
  function render(){
    const seen=new Set<string>();buttons.length=0
    function resolve(node:unknown,path:string):React.ReactNode{
      if(Array.isArray(node))return node.map((child,index)=>React.createElement(React.Fragment,{key:index},resolve(child,`${path}/${index}`)))
      if(!React.isValidElement(node))return node as React.ReactNode
      const element=node as React.ReactElement<Record<string,unknown>>,identity=`${path}/${typeof element.type==="function"?element.type.name:String(element.type)}:${element.key??""}`
      if(typeof element.type==="function"){
        seen.add(identity)
        active=mounted.get(identity)??hookRunner();mounted.set(identity,active)
        return resolve(active.render(element.type as (props:Record<string,unknown>)=>unknown,element.props),identity)
      }
      if(element.type==="button")buttons.push(element as Tree)
      const {children,...props}=element.props
      return React.createElement(element.type as React.ElementType,{...props,key:element.key},resolve(children,identity))
    }
    const html=renderToStaticMarkup(resolve(React.createElement(LiveBoardPage as React.ComponentType),"root"))
    for(const key of mounted.keys())if(!seen.has(key))mounted.delete(key)
    return html
  }
  function click(label:string){
    const button=buttons.find(row=>row.props.children===label);assert.ok(button,`rendered button: ${label}`)
    ;(button.props.onClick as (event:unknown)=>void)({currentTarget:{}})
  }
  render();click("검사용 서비스 A")
  assert.match(render(),/판단 근거 · 선택 당시/)
  assert.equal(calls.length,1)
  const original=calls[0]
  assert.equal(new URL(original,"https://local").searchParams.get("from"),history.decisionRange(initial)!.from)
  recorded={at:initial,decision:{at:initial,status:"Keep",keep:{id:"keep",keep:true,status:"Feasible",expectedJoules:1000},target:null,search:{windowStart:initial,windowEnd:initial,received:0,accepted:0,computeInputs:0,examined:1},capacity:{workloads:[{uid:state.snapshot!.workloads[0].uid,rate:42.5,queued:0,routingRate:50,retainedRoutingRate:25}]}}}
  const recordedHTML=render()
  assert.match(recordedHTML,/42\.5/)
  const later="2026-11-04T10:00:10.000Z"
  state={...state,snapshot:{...state.snapshot!,decision:{...state.snapshot!.decision!,at:later}}}
  render();render()
  assert.deepEqual([...new Set(calls)],[original],"live updates do not start another historical request")
  click("판단 근거");render()
  assert.equal(new URL(calls.at(-1)!,"https://local").searchParams.get("from"),history.decisionRange(later)!.from,"explicit decision selection loads the new decision")
  params=new URLSearchParams()
  render()
  state={...state,snapshot:{...state.snapshot!,reservations:[{owner:"fixture",nodes:[],workloadUIDs:[],instances:[],podUIDs:[],reason:"",explanation:""}]}}
  render()
  const afterReservation=render()
  assert.deepEqual(afterReservation.match(/aria-live="polite" title="([^"]+)"/)?.[1].match(/\d+/g),["0","1"],"previous and current reservation counts remain available without fixing tooltip prose")
  click("화면 일시정지")
  assert.match(render(),/aria-live="off" aria-atomic="true"/,"paused observations do not announce operational changes")
  click("실시간으로 복귀")
  assert.match(render(),/aria-live="polite" aria-atomic="true"/)
})
