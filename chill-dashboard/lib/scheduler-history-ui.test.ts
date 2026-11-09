import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as scheduler from "./scheduler.ts"
import * as history from "./scheduler-history.ts"
import * as capacity from "./capacity.ts"
import * as liveDisplay from "./live-display.ts"
import * as dates from "./date-time.ts"
import * as identity from "./workload-identity.ts"
import * as labels from "./labels.ts"
import * as occupancy from "./dispatch-occupancy.ts"
import { formatMeasurementTime } from "./measurements.ts"

type Element = { type: unknown; props: Record<string, unknown> }
const require=createRequire(import.meta.url)
function nodes(value:unknown):Element[]{
  if(Array.isArray(value))return value.flatMap(nodes)
  if(!value||typeof value!=="object"||!("props" in value))return []
  const element=value as Element;return [element,...nodes(element.props.children)]
}
function render(value:unknown):unknown {
  if(Array.isArray(value))return value.map(render)
  if(!value||typeof value!=="object"||!("props" in value))return value
  const element=value as Element
  if(typeof element.type==="function")return render(element.type(element.props))
  return {...element,props:{...element.props,children:render(element.props.children)}}
}
function harness(query="",responses:Record<string,unknown>={}){
  const params=new URLSearchParams(query),calls:string[]=[],urls:string[]=[]
  const modules:Record<string,unknown>={
    react:{useState:(value:unknown)=>[typeof value==="function"?value():value,()=>{}],useRef:()=>({current:null}),useEffect:()=>{}},
    "next/link":{default:"a"},"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:(url:string)=>urls.push(url)}),usePathname:()=>"/records"},
    "@tanstack/react-query":{useQueryClient:()=>({getQueryState:()=>undefined})},
    "@/lib/client":{useSession:()=>({token:"token",config:{pageSize:25,maxPageSize:100,maxWindowHours:24}}),useChillQuery:(path:string)=>{calls.push(path);return responses[path]??(path==="v1/workloads?view=summary"?{data:[{desired:{metadata:{name:"서비스 이름",uid:"service-uid"}}}]}:{data:path.startsWith("v1/observations/scheduler?")?{events:[]}:undefined})}},
    "@/lib/scheduler":scheduler,"@/lib/scheduler-history":history,"@/lib/capacity":capacity,"@/lib/live-display":liveDisplay,"@/lib/date-time":dates,"@/lib/workload-identity":identity,"@/lib/labels":labels,
    "@/lib/dispatch-occupancy":occupancy,
    "./scheduler-history":{when:formatMeasurementTime},
    "@/components/read-region":{QueryGate:"gate",SourceJSON:"source",SourceExport:"export",StatusBadge:"badge"},
    "@/components/screen":{Screen:"main",Rows:"rows",Row:"row",RowDialog:"dialog",Facts:"facts",Empty:"empty"},
    "@/components/data-table":{DataTable:"table"},"@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},
    "@/components/ui/sheet":{},"@/components/records-navigation":{RecordsNavigation:"navigation",RecordsRange:"range"},
    "@/components/ui/chart":{ChartContainer:"chart",ChartTooltip:"tooltip",ChartTooltipContent:"tooltip-content"},
    recharts:{BarChart:"bar-chart",Bar:"bar",CartesianGrid:"chart-grid",XAxis:"x-axis",YAxis:"y-axis"},
  }
  function load(file:string){
    const exports:Record<string,(props:Record<string,unknown>)=>Element>={}
    const compiled=transpileModule(readFileSync(new URL(file,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
    new Function("require","exports",compiled)((name:string)=>modules[name]??(name.startsWith("@/")||name.startsWith("./")?{}:require(name)),exports)
    return exports
  }
  modules["./scheduler-workload-states"]=load("../features/scheduler-workload-states.tsx")
  const rateEvidence=load("../features/scheduler-capacity.tsx")
  modules["./scheduler-capacity"]=rateEvidence
  modules["@/features/scheduler-capacity"]=rateEvidence
  return {load,calls,urls,modules}
}

test("decision evidence keeps selected and keep tests separate without inventing missing values",()=>{
  const screen=harness().load("../features/scheduler-history.tsx")
  const evidence=scheduler.parseSchedulerJSON(readFileSync(new URL("./fixtures/scheduler-feedback-capacity.json",import.meta.url),"utf8")) as capacity.CapacityView
  const [failed,,passed]=evidence.tests!
  const diagnostic={workloads:[{uid:"ic",rate:97.65,queued:0,routingRate:128.16703347622592,retainedRoutingRate:110.28686611755043,weights:{"instance-failed":2},tests:[failed],keepTests:[passed]},{uid:"missing"}]}
  const tree=render(screen.DecisionCapacity({capacity:diagnostic,names:new Map([["ic","IC"]])}))
  const facts=nodes(tree).filter(node=>node.type==="facts").flatMap(node=>node.props.items as [string,unknown][])
  assert.ok(facts.some(([label,value])=>label==="계획 수요 (req/s)"&&value==="97.6500"))
  assert.ok(facts.some(([label,value])=>label==="목표 상대 프로파일 요청률 (req/s)"&&value==="128.1670"))
  assert.ok(facts.some(([label,value])=>label==="전환 중 유지 상대 프로파일 요청률 (req/s)"&&value==="110.2869"))
  assert.ok(facts.some(([label,value])=>label==="계획 수요 (req/s)"&&value==="미기록"))
  assert.deepEqual(nodes(tree).filter(node=>node.type==="h5").map(node=>node.props.children),["선택안","유지안","선택안","유지안"])
  assert.deepEqual(nodes(tree).filter(node=>node.type==="table"&&nodes(node).some(child=>child.type==="caption"&&child.props.children==="현재 조건에서 검사한 요청률과 창 판정")).flatMap(table=>nodes(table).filter(node=>node.type==="td").map(node=>node.props.children)),[40,"미충족",0,100,12.5,"충족",4,100,20,"미충족",1,100])
  assert.equal(facts.some(([label])=>/보호 수요|여유|목표 κ|유지 κ/.test(label)),false)
  assert.equal(nodes(tree).some(node=>node.type==="badge"),false)
})

test("current scheduler readout passes the decision budget, never snapshot or current settings substitutes",()=>{
  const fixture=JSON.parse(readFileSync(new URL("./fixtures/e5-scheduler-control.json",import.meta.url),"utf8"))
  const pathFeedback=[{workloadUID:"decision-only",percentile:95,totalNanos:0,postNanos:0,sampleCount:0,source:"configured"}]
  const view={state:{snapshot:{...fixture.snapshot,pathFeedback:[{...pathFeedback[0],totalNanos:9000000,source:"empirical"}]},decision:{...fixture.decision,pathFeedback,settings:{...fixture.decision.settings,completionMarginMS:123}}}}
  const h=harness("",{"v1/scheduler":{data:view}})
  const screen=h.load("../features/scheduler.tsx")
  const components=h.modules["./scheduler-workload-states"] as Record<string,unknown>
  const component=components.SchedulerPathFeedback
  for(const kind of ["candidates","estimates"]){
    const entry=screen.SchedulerEvidence({kind})
    const tree=(entry.type as (props:Record<string,unknown>)=>Element)(entry.props)
    const readout=nodes(tree).find(node=>node.type===component)!
    assert.ok(readout)
    assert.equal(readout.props.decision,view.state.decision)
  }
  Reflect.deleteProperty(view.state.decision,"pathFeedback")
  const entry=screen.SchedulerEvidence({kind:"estimates"})
  const tree=(entry.type as (props:Record<string,unknown>)=>Element)(entry.props)
  const readout=nodes(tree).find(node=>node.type===component)!
  const absent=(component as (props:Record<string,unknown>)=>unknown)(readout.props)
  assert.equal(absent,null)
})

test("demand labels the recorded planning rate and routing weights remain relative profile rates",()=>{
  const screen=harness().load("../features/scheduler-history.tsx")
  const diagnostic={workloads:[{uid:"idle",rate:40,queued:0,routingRate:50,retainedRoutingRate:50,weights:{instance:50}}]}
  const tree=render(screen.DecisionCapacity({capacity:diagnostic,names:new Map()}))
  const facts=nodes(tree).filter(node=>node.type==="facts").flatMap(node=>node.props.items as [string,unknown][])
  assert.ok(facts.some(([label,value])=>label==="계획 수요 (req/s)"&&value==="40.0000"))
  assert.ok(facts.some(([label,value])=>label==="instance"&&value==="50"))
  assert.equal(facts.some(([label])=>label==="최근 수요 (req/s)"),false)
  const text=nodes(tree).flatMap(node=>node.props.children).filter(value=>typeof value==="string").join(" ")
  assert.match(text,/상대 가중치/)
  assert.match(text,/SLO 처리 용량 보장이 아니에요/)
})

test("judgment history defaults to decision/action on the server, not mixed power records",()=>{
  const h=harness(),screen=h.load("../features/scheduler-history.tsx")
  screen.SchedulerHistoryPage({})
  const request=new URL(h.calls.find(path=>path.startsWith("v1/observations/scheduler?"))!,"https://dashboard.invalid")
  assert.equal(request.searchParams.get("filter"),"decision-action")
})

test("scope baseline and transition evidence select exact records while normal history keeps every judgment",()=>{
  const from="2026-11-01T00:00:00Z",to="2026-11-01T01:00:00Z"
  const baseline={at:"2026-11-01T00:10:00Z",offset:25,decision:{at:"2026-11-01T00:10:00Z",status:"Keep",workloads:[{uid:"service-uid",state:"Controlled"}]}}
  const repeated={...baseline,at:"2026-11-01T00:20:00Z",offset:26,decision:{...baseline.decision,at:"2026-11-01T00:20:00Z"}}
  const changed={at:"2026-11-01T00:30:00Z",offset:27,decision:{at:"2026-11-01T00:30:00Z",status:"Keep",workloads:[{uid:"service-uid",state:"Fixed",reason:"ReservedScope"}]}}
  for(const scope of [false,true]){
    const h=harness(new URLSearchParams({from,to,cursor:"25",filter:"decision",...(scope?{timeline:"scope"}:{})}).toString())
    const api=h.modules["@/lib/client"] as {useChillQuery:(path:string)=>unknown},query=api.useChillQuery
    api.useChillQuery=path=>path.startsWith("v1/observations/scheduler?")?{data:{events:[changed,repeated,baseline]}}:query(path)
    h.modules["@/features/scheduler"]={judgmentLabels:{Keep:"현재 구성 유지"}}
    const tree=h.load("../features/scheduler-history.tsx").SchedulerHistoryPage({})
    const buttons=nodes(tree).filter(node=>node.type==="button"&&node.props.children===(scope?"판단 근거":"근거"))
    const expected=scope?[baseline,changed]:[changed,repeated,baseline]
    assert.equal(buttons.length,expected.length)
    const original=Object.getOwnPropertyDescriptor(globalThis,"document")
    Object.defineProperty(globalThis,"document",{configurable:true,value:{activeElement:null}})
    try {
      buttons.forEach((button,index)=>{
        ;(button.props.onClick as ()=>void)()
        const selected=new URL(h.urls[index],"https://dashboard.invalid").searchParams
        assert.deepEqual(history.readSchedulerRecord(selected),history.schedulerRecordRef(expected[index],from,to,"0"))
        assert.equal(selected.get("cursor"),"25")
        assert.equal(selected.get("timeline"),scope?"scope":null)
      })
    } finally {
      if(original)Object.defineProperty(globalThis,"document",original)
      else Reflect.deleteProperty(globalThis,"document")
    }
  }
})

test("history target uses workload names, preserves direct UID and does not repeat range values in edit rows",()=>{
  const h=harness("uid=service-uid&from=2026-11-01T00:00:00Z&to=2026-11-01T01:00:00Z&cursor=25"),screen=h.load("../features/scheduler-history.tsx")
  const tree=screen.SchedulerHistoryPage({}),target=nodes(tree).find(node=>node.type==="dialog"&&node.props.label==="대상 변경")!
  const selector=nodes(target).find(node=>node.type==="select")!
  assert.ok(nodes(selector).some(node=>node.type==="option"&&node.props.children==="서비스 이름"))
  assert.ok(nodes(target).some(node=>node.type==="input"&&node.props.name==="uid"))
  ;(selector.props.onChange as (event:unknown)=>void)({target:{value:""}})
  assert.equal(new URL(h.urls[0],"https://dashboard.invalid").searchParams.has("uid"),false)
  assert.equal(new URL(h.urls[0],"https://dashboard.invalid").searchParams.has("cursor"),false)
  assert.equal(target.props.value,undefined)
  const controls=nodes(tree).find(node=>node.type===screen.HistoryControls)!
  const controlTree=screen.HistoryControls(controls.props)
  assert.equal(nodes(controlTree).find(node=>node.type==="dialog")!.props.value,undefined)
  assert.equal(nodes(tree).find(node=>node.type==="range")!.props.target,"서비스 이름")
})

test("empty current diagnostics are successful evidence without pending or completion claims",()=>{
  for(const workload of ["","unknown-service"]){
    const h=harness(workload?`workload=${workload}`:"",{"v1/scheduler/capacity":{data:{tests:[]},dataUpdatedAt:0},"v1/scheduler/state?view=compact":{data:{decision:{at:"2026-11-09T00:00:00Z",capacity:{workloads:[]}}}}})
    const tree=render(h.load("../features/scheduler-capacity.tsx").CapacityEvidence({}))
    assert.equal(nodes(tree).some(node=>node.type==="table"),false)
    assert.equal(nodes(tree).some(node=>node.props.role==="alert"),false)
    const text=nodes(tree).flatMap(node=>node.props.children).filter(value=>typeof value==="string").join(" ")
    assert.match(text,/기록된 요청률 검사 근거 없음/)
    assert.match(text,/기록된 Workload 진단 없음/)
    assert.doesNotMatch(text,/Pending|Built|다시 계산|완료 예정|용량 계산이 끝/)
    assert.equal(h.calls.includes("v1/scheduler/settings"),false)
  }
})

test("HTTP evidence fixture renders exact input identity and distinguishes failed zero from untested zero",()=>{
  const evidence=scheduler.parseSchedulerJSON(readFileSync(new URL("./fixtures/scheduler-feedback-capacity.json",import.meta.url),"utf8")) as capacity.CapacityView
  const h=harness("",{"v1/scheduler/capacity":{data:evidence,dataUpdatedAt:0}})
  const tree=render(h.load("../features/scheduler-capacity.tsx").CapacityEvidence({}))
  const facts=nodes(tree).filter(node=>node.type==="facts").flatMap(node=>node.props.items as [string,unknown][])
  assert.ok(facts.some(([label,value])=>label==="GPU UUID"&&value==="gpu-uuid"))
  assert.ok(facts.some(([label,value])=>label==="메모리"&&value==="9007199254740993 bytes"))
  assert.deepEqual(facts.filter(([label])=>label==="κ̂ (κHat)").map(([,value])=>value),evidence.tests!.map(capacity.capacityKappaHat))
  assert.ok(nodes(tree).some(node=>node.type==="th"&&node.props.children==="failed input / +&"))
  assert.deepEqual(nodes(tree).filter(node=>node.type==="table"&&nodes(node).some(child=>child.type==="caption"&&child.props.children==="현재 조건에서 검사한 요청률과 창 판정")).flatMap(table=>nodes(table).filter(node=>node.type==="td").map(node=>node.props.children)),[40,"미충족",0,100,12.5,"충족",4,100,20,"미충족",1,100])
  const budgets=nodes(tree).filter(node=>node.type==="table"&&nodes(node).some(child=>child.type==="caption"&&child.props.children==="결정의 서비스별 경로 예산"))
  assert.deepEqual(budgets.map(table=>nodes(table).filter(node=>node.type==="td").map(node=>node.props.children)),[[99,40,30,"empirical",1],[99,0,0,"configured",0],[99,10,0,"empirical",2]])
  assert.equal(facts.some(([label])=>/CPU|스레드/.test(label)),false)
  const text=nodes(tree).flatMap(node=>node.props.children).filter(value=>typeof value==="string").join(" ")
  assert.match(text,/물리 처리량 0을 뜻하지 않아요/)
  assert.match(text,/검사한 요청률 없음/)
})

test("whole power shows shared reasons once and retains distinct node reasons and raw export",()=>{
  const data={from:"2026-11-01T00:00:00Z",to:"2026-11-01T01:00:00Z",complete:false,wholePoolJoules:null,wholePoolMeanWatts:null,activePoolJoules:null,activePoolMeanWatts:null,reasons:["shared"],nodes:Array.from({length:10},(_,index)=>({
    node:{name:`node-${index}`},joules:null,meanWatts:null,reasons:["shared","all-nodes",...(index===0?["only-a"]:[])],
  }))}
  const h=harness("",{[`v1/observations/power?${new URLSearchParams({from:data.from,to:data.to})}`]:{data}})
  const tree=h.load("../features/scheduler.tsx").PowerInterval({from:data.from,to:data.to})
  const table=nodes(tree).find(node=>node.type==="table")!
  const column=(table.props.columns as {key:string;value:(row:unknown)=>Element}[]).find(column=>column.key==="coverage")!
  const reasonValues=(tree:Element)=>nodes(tree).flatMap(node=>Array.isArray(node.props.values)?node.props.values:[])
  assert.deepEqual(reasonValues(tree),["shared","all-nodes"])
  assert.deepEqual(reasonValues(column.value(data.nodes[0])),["only-a"])
  assert.deepEqual(reasonValues(column.value(data.nodes[1])),[])
  const exported=nodes(tree).find(node=>node.type==="export")!.props.value
  assert.ok(exported&&typeof exported==="object"&&"result" in exported)
  assert.equal(exported.result,data)
})


test("interval preview requests latest cluster events and keeps exact identity and return selection",()=>{
  const from="2026-11-01T00:00:00.123456789Z",to="2026-11-01T01:00:00.987654321Z"
  const returnTo="/experiments/results?a=P&al=P&as=C&b=Q&bs=B&service=S&offset=5"
  const path=`v1/observations/scheduler?${new URLSearchParams({from,to,view:"summary",filter:"decision-action",order:"desc",limit:"10"})}`
  const event={at:"2026-11-01T00:30:00.123456789Z",offset:9007199254740993n,action:{id:"action",kind:"route",phase:"starting",node:{name:"node",uid:"node-uid"},startedAt:"2026-11-01T00:29:59.123456789Z"}}
  const h=harness("",{[path]:{data:{events:[event],nextCursor:"older",gapUntil:from}}})
  h.modules.react={useState:()=>[true,()=>{}]}
  h.modules["./scheduler"]={actionKinds:{route:"분배"},actionNodeName:(node:{name:string})=>node.name}
  const tree=h.load("../features/scheduler-evidence.tsx").SchedulerIntervalEvents({from,to,returnTo,label:"기준"})
  assert.equal(h.calls[0],path)
  assert.equal(new URL(h.calls[0],"https://dashboard.invalid").searchParams.has("returnTo"),false)
  const links=nodes(tree).filter(node=>node.type==="a")
  const selected=new URL(String(links.find(node=>String(node.props.href).includes("offset="))!.props.href),"https://dashboard.invalid").searchParams
  assert.deepEqual(history.readSchedulerRecord(selected),history.schedulerRecordRef(event,from,to,"0"))
  assert.equal(selected.get("returnTo"),returnTo)
  const more=new URL(String(links.find(node=>String(node.props.href).includes("cursor="))!.props.href),"https://dashboard.invalid")
  assert.equal(more.searchParams.get("cursor"),"older")
  assert.equal(more.searchParams.get("order"),"desc")
  assert.equal(more.searchParams.get("returnTo"),returnTo)
  const text=nodes(tree).flatMap(node=>node.props.children).filter(value=>typeof value==="string").join(" ")
  assert.match(text,/기준/)
  assert.match(text,/클러스터 전체/)
  assert.match(text,/일부 기록/)
  assert.doesNotMatch(text,/전체.*건/)
})

test("records restore experiment selection from an internal return path only",()=>{
  const destination="/experiments/results?a=P&al=P&as=C&service=S&offset=5"
  for(const returnTo of [destination,"https://evil.invalid/experiments/results","//evil.invalid/experiments"]){
    const h=harness(new URLSearchParams({returnTo}).toString())
    const tree=h.load("../features/records.tsx").RecordsPage({})
    assert.deepEqual(nodes(tree).filter(node=>node.type==="a").map(node=>node.props.href),returnTo===destination?[destination]:[])
  }
})

test("candidate and baseline previews use their own absolute ranges without service filters",()=>{
  const h=harness(),preview=h.load("../features/scheduler-evidence.tsx").SchedulerIntervalEvents
  const intervals=[{from:"2026-11-01T00:00:00Z",to:"2026-11-01T01:00:00Z",label:"후보"},{from:"2026-10-01T00:00:00Z",to:"2026-10-01T01:00:00Z",label:"기준"}]
  for(const interval of intervals)preview({...interval,returnTo:"/experiments/results?a=P&service=S"})
  assert.deepEqual(h.calls.map(path=>{
    const params=new URL(path,"https://dashboard.invalid").searchParams
    assert.equal(params.has("uid"),false)
    assert.equal(params.has("service"),false)
    assert.equal(params.has("returnTo"),false)
    return {from:params.get("from"),to:params.get("to")}
  }),intervals.map(({from,to})=>({from,to})))
})

test("published interval observations issue no request until the explicit query button is clicked",()=>{
  const from="2026-11-04T10:00:34.817685Z",to="2026-11-04T10:15:35.090158Z"
  const h=harness(),requests:string[]=[]
  let requested=false
  h.modules.react={useState:(initial:boolean)=>[requested||initial,(value:boolean)=>{requested=value}]}
  h.modules["@/lib/client"]={useChillQuery:(path:string,options:{enabled?:boolean})=>{
    if(options.enabled===false)return {isPending:true,error:null}
    requests.push(path)
    return {data:{events:[{at:from,decision:{status:"Keep"}}]},error:null}
  }}
  h.modules["./scheduler"]={judgmentLabels:{Keep:"현재 구성 유지"}}
  const preview=h.load("../features/scheduler-evidence.tsx").SchedulerIntervalEvents
  const props={from,to,returnTo:"/experiments/results?a=published&service=camera",label:"선택 단계"}
  const initial=preview(props)
  assert.deepEqual(requests,[])
  assert.equal(nodes(initial).some(node=>node.type==="table"),false)
  const button=nodes(initial).find(node=>node.type==="button"&&node.props.children==="같은 기간 사건 조회")
  assert.ok(button,"opening a service detail must offer an explicit observation query")
  ;(button.props.onClick as ()=>void)()
  const loaded=preview(props)
  assert.equal(requests.length,1)
  const query=new URL(requests[0],"https://dashboard.invalid").searchParams
  assert.deepEqual(Object.fromEntries(query),{from,to,view:"summary",filter:"decision-action",order:"desc",limit:"10"})
  assert.ok(nodes(loaded).some(node=>node.type==="table"))
  assert.ok(nodes(loaded).some(node=>node.type==="a"&&String(node.props.href).startsWith("/records?")))
  assert.ok(nodes(loaded).some(node=>node.props.children==="판단 · 현재 구성 유지"))
})

test("selected record keeps an accessible experiment return link inside its Sheet",()=>{
  const returnTo="/experiments/results?a=P&al=P&as=C&service=S"
  const ref=history.schedulerRecordRef({at:"2026-11-01T00:30:00Z",decision:{at:"2026-11-01T00:30:00Z",status:"Keep"}},"2026-11-01T00:00:00Z","2026-11-01T01:00:00Z","42")
  const params=history.schedulerRecordParams(ref);params.set("returnTo",returnTo)
  const h=harness(params.toString())
  const tree=h.load("../features/scheduler-history.tsx").SchedulerHistoryPage({})
  assert.ok(nodes(tree).some(node=>node.type==="a"&&node.props.href===returnTo))
  assert.ok(h.calls.includes(history.schedulerRecordPath(ref)))
  for(const path of h.calls)assert.equal(new URL(path,"https://dashboard.invalid").searchParams.has("returnTo"),false)
})
