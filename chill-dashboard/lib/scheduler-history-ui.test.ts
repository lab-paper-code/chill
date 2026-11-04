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
  return {load,calls,urls,modules}
}

test("selected decision capacity renders recorded diagnostics without inventing missing values",()=>{
  const screen=harness().load("../features/scheduler-history.tsx")
  const capacity={workloads:[{uid:"ic",rate:97.65,kappaNew:128.16703347622592,kappaRetained:110.28686611755043,scaleDown:{windowMax:102.13333333333333}},{uid:"missing"}]}
  const tree=screen.DecisionCapacity({capacity,headroom:.25,names:new Map([["ic","IC"]])})
  const cells=nodes(tree).filter(node=>node.type==="td").map(node=>node.props.children)
  assert.deepEqual(cells.slice(0,7),["IC","128.1670","110.2869","127.6667","97.6500","102.1333","0.5004"])
  assert.deepEqual(cells.slice(7),["missing",...Array(6).fill("미기록")])
  const missing=screen.DecisionCapacity({capacity,headroom:undefined,names:new Map()})
  const absent=nodes(missing).filter(node=>node.type==="td").map(node=>node.props.children)
  assert.equal(absent[3],"미기록")
  assert.equal(absent[6],"미기록")
  assert.equal(nodes(tree).some(node=>node.type==="badge"),false)
})

test("capacity demand labels the recorded planning floor rather than zero observed traffic",()=>{
  const screen=harness().load("../features/scheduler-history.tsx")
  const decision={search:{loads:[{uid:"idle",inputsPerSecond:0,recentInputsPerSecond:0}]},capacity:{workloads:[{uid:"idle",rate:40,minimumDemand:40,kappaNew:50,kappaRetained:50,scaleDown:{windowMax:40}}]}}
  const tree=screen.DecisionCapacity({capacity:decision.capacity,headroom:.25,names:new Map()})
  const headers=nodes(tree).filter(node=>node.type==="th").map(node=>node.props.children)
  const cells=nodes(tree).filter(node=>node.type==="td").map(node=>node.props.children)
  assert.equal(headers[4],"계획 수요 (req/s)")
  assert.equal(cells[4],"40.0000")
  assert.equal(headers.includes("최근 수요 (req/s)"),false)
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

test("capacity with no returned rows shows purpose-built missing evidence and no empty table",()=>{
  for(const workload of ["","unknown-service"]){
    const h=harness(workload?`workload=${workload}`:"",{"v1/scheduler/capacity":{data:{rows:[]},dataUpdatedAt:0}})
    const tree=h.load("../features/scheduler-capacity.tsx").CapacityEvidence({})
    assert.equal(nodes(tree).some(node=>node.type==="table"),false)
    const facts=nodes(tree).filter(node=>node.type==="facts").flatMap(node=>node.props.items as [string,unknown][])
    assert.ok(facts.some(([label,value])=>label==="상태"&&value==="처리 용량 근거 없음"))
    assert.ok(facts.some(([label,value])=>label==="누락 사유"&&value==="서버 미제공"))
    assert.equal(JSON.stringify(facts).includes("κ"),false)
  }
})

test("capacity input facts use shared execution without CPU reservations or thread allocations",()=>{
  const row:capacity.CapacityRow={key:"shared",status:"Built",mu:10,builtAt:"2026-11-01T00:00:00Z",input:{
    workloadUID:"service-uid",slo:{percentile:99,targetLatencyNanos:100000000},node:{name:"node",uid:"node-uid"},powerMode:"fixed",profileID:"profile",
    allocation:{gpuUUID:"gpu-uuid",memoryBytes:9007199254740993n},maxBatch:4,completionMarginMS:1,capacityWindowNanos:1000000000,
    capacityWindows:5,capacityPassingWindows:4,capacityMinimumRequests:100,capacityGridSteps:10,capacityBisectionWidth:.01,capacityTailSamples:20,
  }}
  const h=harness("row=shared",{"v1/scheduler/capacity":{data:{rows:[row]},dataUpdatedAt:0}})
  const tree=h.load("../features/scheduler-capacity.tsx").CapacityEvidence({})
  const details=nodes(tree).find(node=>typeof node.type==="function"&&node.props.row===row)!
  const facts=nodes((details.type as (props:Record<string,unknown>)=>Element)(details.props)).filter(node=>node.type==="facts").flatMap(node=>node.props.items as [string,unknown][])
  assert.ok(facts.some(([label,value])=>label==="GPU UUID"&&value==="gpu-uuid"))
  assert.ok(facts.some(([label,value])=>label==="메모리"&&value==="9007199254740993 bytes"))
  assert.equal(facts.some(([label])=>/CPU|스레드/.test(label)),false)
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

test("missing dispatch occupancy links directly to the matching d request conditions",()=>{
  const row={key:"missing-d",status:"CapacityRejected",reason:"MissingDispatchOccupancy",mu:0,input:{workloadUID:"service-uid",slo:{percentile:99,targetLatencyNanos:100000000},node:{name:"node",uid:"node-uid"},powerMode:"15W",profileID:"ns/beo/beo-uid",allocation:{},maxBatch:4}} as capacity.CapacityRow
  const h=harness("row=missing-d",{"v1/scheduler/capacity":{data:{rows:[row]},dataUpdatedAt:0}})
  const tree=h.load("../features/scheduler-capacity.tsx").CapacityEvidence({})
  const details=nodes(tree).find(node=>typeof node.type==="function"&&node.props.row===row)!
  const link=nodes((details.type as (props:Record<string,unknown>)=>Element)(details.props)).find(node=>node.type==="a"&&node.props.children==="d 측정 요청")!
  const url=new URL(String(link.props.href),"https://dashboard.invalid")
  assert.equal(url.pathname,"/measurements/requests/new")
  for(const [key,value] of Object.entries({kind:"dispatch-occupancy",namespace:"ns",profileID:"ns/beo/beo-uid",node:"node",nodeUID:"node-uid",powerMode:"15W",workloadUID:"service-uid",batch:"4"}))assert.equal(url.searchParams.get(key),value)
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
