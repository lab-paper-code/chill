import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as measurements from "./measurements.ts"
import * as costs from "./operation-cost.ts"
import * as assessment from "./measurement-assessment.ts"
import * as scheduler from "./scheduler.ts"
import * as status from "./status.ts"
import * as coverage from "./measurement-coverage.ts"
import * as requests from "./measurement-request.ts"
import type { MeasurementAssessment, OperationCostMeasurement, ProfilesView, ResultRow } from "./read-types.ts"

type Element={type:unknown;props:Record<string,unknown>}
type Component=(props:Record<string,unknown>)=>unknown
type Column={key:string;label:string;unit?:string;numeric?:boolean;value:(row:unknown)=>unknown}
const require=createRequire(import.meta.url)
function elements(node:unknown):Element[] {
  if(Array.isArray(node))return node.flatMap(elements)
  if(!node||typeof node!=="object"||!("props" in node))return []
  const element=node as Element
  return [element,...elements(element.props.children)]
}
function text(node:unknown):string {
  if(Array.isArray(node))return node.map(text).join("")
  if(node==null||typeof node==="boolean")return ""
  if(typeof node!=="object")return String(node)
  const element=node as Element
  if(typeof element.type==="function")return text((element.type as Component)(element.props))
  return text(element.props?.children)
}
function load(file:string,modules:Record<string,unknown>):Record<string,Component> {
  const source=readFileSync(new URL(file,import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  new Function("require","exports",compiled)((name:string)=>Object.hasOwn(modules,name)?modules[name]:name.startsWith("@/")?{}:require(name),exports)
  return exports
}
function harness(search:string,respond:(path:string)=>unknown) {
  const params=new URLSearchParams(search),pushes:string[]=[],replacements:string[]=[],paths:string[]=[],refreshes:string[]=[]
  const modules={
    react:{useRef:(value:unknown)=>({current:value}),useState:(value:unknown)=>[value,()=>{}]},
    "lucide-react":{TriangleAlert:()=>null},
    "next/link":{default:"a"},
    "next/navigation":{useSearchParams:()=>params,useRouter:()=>({push:(value:string)=>pushes.push(value),replace:(value:string)=>replacements.push(value)})},
    "@/lib/client":{useSession:()=>({config:{namespace:"system"}}),useChillQuery:(path:string)=>{paths.push(path);return {data:respond(path),error:null,isPending:false,isFetching:false,dataUpdatedAt:1,refetch:()=>{refreshes.push(path)}}}},
    "@/lib/measurements":measurements,"@/lib/operation-cost":costs,"@/lib/measurement-assessment":assessment,"@/lib/scheduler":scheduler,"@/lib/status":status,
    "@/components/status-cell":{StatusCell:"status-cell"},
    "@/components/screen":{Screen:"main",Facts:"facts"},
    "@/components/read-region":{QueryGate:"query-gate",SourceExport:"source-export",SourceJSON:"source-json"},
    "@/components/data-table":{DataTable:"data-table",useServerTable:()=>({query:new URLSearchParams({limit:"25",offset:"0"}),pagination:{pageIndex:0,pageSize:25},sorting:[]})},
    "@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},
    "@/components/ui/sheet":{Sheet:"sheet",SheetContent:"sheet-content",SheetHeader:"sheet-header",SheetTitle:"sheet-title",SheetDescription:"sheet-description"},
    "@/components/measurement-navigation":{MeasurementNavigation:"measurement-nav",measurementStates:{usable:"측정 충족",problem:"문제",unknown:"미확인"}},
    "@/features/measurement-plans":{AssessmentDetails:"assessment-details",ResultRows:"result-rows"},
    "@/features/measurement-coverage":{MeasurementFilters:"measurement-filters"},
    "@/features/scheduler-profiles":{SchedulerProfilesPage:"throughput-results"},
  }
  return {modules,params,pushes,replacements,paths,refreshes}
}
function projection(h:{modules:Record<string,unknown>}) {
  const frame=load("../features/measurement-projections.tsx",h.modules).MeasurementResultsPage({})
  const child=elements(frame).find(element=>typeof element.type==="function"&&Object.hasOwn(element.props,"kind"))!
  return (child.type as Component)(child.props)
}

const accepted={scope:{schedulerUsed:true},checklist:{criteriaMet:null,checks:[]},reasons:[],displayStatus:"usable"} as unknown as MeasurementAssessment
const measurement:OperationCostMeasurement={
  kind:"OperationCostMeasurement",metadata:{name:"cost",namespace:"ns",uid:"original",resourceVersion:"1",creationTimestamp:"2026-11-01T00:00:00Z"},node:"actual-device-name",
  references:{cr:{apiVersion:"v1",kind:"OperationCostMeasurement",namespace:"ns",name:"cost",uid:"original"},evidence:[]},
  conditions:{steps:[{name:"prepare",kind:"prepare"},{name:"remove",kind:"remove"}]},
  status:{steps:[{name:"prepare",key:"p1",verdict:"Accepted",model:"long-model-name-that-must-wrap",durationNanos:10e9,wallJoules:100},{name:"prepare",key:"p2",verdict:"Accepted",model:"long-model-name-that-must-wrap",durationNanos:20e9,wallJoules:200},{name:"remove",key:"r1",verdict:"Rejected",model:"long-model-name-that-must-wrap",durationNanos:2e9,wallJoules:999}]},assessment:accepted,
}

test("results frame has one navigation and kind switching preserves context but clears selection and pagination",()=>{
  const h=harness("kind=throughput&namespace=ns&node=device&profile=chosen&page=3&sort=created",()=>undefined)
  const frame=load("../features/measurement-projections.tsx",h.modules).MeasurementResultsPage({})
  assert.equal(elements(frame).filter(element=>element.type==="main").length,1)
  assert.equal(elements(frame).filter(element=>element.type==="measurement-nav").length,1)
  assert.equal(elements(frame).find(element=>element.type==="measurement-nav")?.props.active,"results")
  const selector=elements(frame).find(element=>element.type==="select")!
  assert.deepEqual(elements(selector).filter(element=>element.type==="option").map(element=>element.props.value),["throughput","operation-cost","dispatch-occupancy"])
  ;(selector.props.onChange as (event:{target:{value:string}})=>void)({target:{value:"operation-cost"}})
  const next=new URL(h.pushes[0],"https://dashboard.invalid")
  assert.equal(next.pathname,"/measurements/results")
  assert.equal(next.searchParams.get("kind"),"operation-cost")
  assert.equal(next.searchParams.get("namespace"),"ns")
  assert.equal(next.searchParams.get("node"),"device")
  for(const key of ["profile","page","sort"])assert.equal(next.searchParams.has(key),false)
})

test("cost result table separates prepare and remove, labels statistics and retains actual sample denominators",()=>{
  const h=harness("kind=operation-cost&namespace=ns",path=>path.endsWith("operation-cost-measurements")?{items:[measurement]}:undefined)
  const tree=projection(h)
  const costTable=elements(tree).find(element=>typeof element.type==="function"&&Array.isArray(element.props.items))!
  const table=(costTable.type as Component)(costTable.props) as Element
  const rows=table.props.data as unknown[]
  const columns=table.props.columns as Column[]
  assert.equal(rows.length,2)
  const cell=(key:string,row:number)=>columns.find(column=>column.key===key)!.value(rows[row])
  assert.equal(text(cell("operation",0)),"모델 준비")
  assert.equal(text(cell("operation",1)),"모델 제거")
  assert.equal(text(cell("duration-0",0)),"15.0")
  assert.equal(text(cell("duration-1",0)),"20.0")
  assert.equal(text(cell("energy-0",0)),"150")
  assert.equal(text(cell("energy-0",1)),"미제공")
  assert.equal(text(cell("samples",0)),"2 / 2")
  assert.equal(text(cell("samples",1)),"1 / 0")
  assert.equal(text(cell("node",0)),"actual-device-name")
  assert.deepEqual(columns.filter(column=>column.key.startsWith("duration-")).map(column=>[column.label,column.unit,column.numeric]),[["시간 중앙값","s",true],["시간 최대","s",true]])
  assert.deepEqual(columns.filter(column=>column.key.startsWith("energy-")).map(column=>[column.label,column.unit,column.numeric]),[["에너지 중앙값","J",true],["에너지 최대","J",true]])
  const link=cell("model",0) as Element
  assert.equal(text(link),"long-model-name-that-must-wrap")
  assert.ok(String(link.props.className).includes("whitespace-normal"))
  const selected=new URL(String(link.props.href),"https://dashboard.invalid")
  assert.equal(selected.pathname,"/measurements/results")
  assert.equal(selected.searchParams.get("measurement"),"cost")
  assert.equal(selected.searchParams.get("uid"),"original")
})

test("projection selection rejects a same-name replacement UID and closes without dropping filters",()=>{
  const h=harness("kind=operation-cost&namespace=ns&measurement=cost&uid=original&node=actual-device-name",path=>path.endsWith("/cost")?{...measurement,metadata:{...measurement.metadata,uid:"replacement"}}:{items:[measurement]})
  const tree=projection(h),sheet=elements(tree).find(element=>element.type==="sheet")!
  assert.equal(sheet.props.open,true)
  assert.equal(text(elements(tree).find(element=>element.type==="sheet-title")),"측정 식별자 불일치")
  assert.equal(elements(tree).filter(element=>element.type==="facts"&&Array.isArray(element.props.items)).some(element=>(element.props.items as unknown[][]).some(row=>row[0]==="현재 UID"&&row[1]==="replacement")),true)
  assert.equal(elements(tree).some(element=>element.type==="result-rows"),false)
  ;(sheet.props.onOpenChange as (open:boolean)=>void)(false)
  const closed=new URL(h.replacements[0],"https://dashboard.invalid")
  assert.equal(closed.searchParams.get("node"),"actual-device-name")
  assert.equal(closed.searchParams.get("namespace"),"ns")
  assert.equal(closed.searchParams.has("measurement"),false)
  assert.equal(closed.searchParams.has("uid"),false)
})

test("throughput renders actual batch values with current use separate from indeterminate quality",()=>{
  const row={id:"row",batch:1,p95Milliseconds:0,totalJoulesPerItem:null,incrementalJoulesPerItem:2} as ResultRow
  const profile={id:"frozen-id",nodeName:"device",condition:{logicalModel:"model",deviceClass:"class",powerMode:"mode",artifact:{executionPath:"ort"},cpu:{limit:"1"}},assessment:accepted,resultRows:[row],measuredAt:"2026-11-01T00:00:00Z"} as NonNullable<ProfilesView["profiles"]>[number]
  const h=harness("kind=throughput&namespace=ns&node=device",()=>({profiles:[profile],page:{total:7}}))
  const tree=load("../features/scheduler-profiles.tsx",h.modules).SchedulerProfilesPage({})
  const table=elements(tree).find(element=>element.type==="data-table")!,columns=table.props.columns as Column[]
  const cell=(key:string)=>columns.find(column=>column.key===key)!.value(profile)
  const use=cell("currentUse") as Element,quality=cell("quality") as Element
  const useValue=(use.type as Component)(use.props) as Element,qualityValue=(quality.type as Component)(quality.props) as Element
  assert.equal(useValue.props.axis,"현재 사용")
  assert.equal(useValue.props.label,"사용 중")
  assert.equal(qualityValue.props.axis,"품질 판정")
  assert.equal(qualityValue.props.label,"판정 불가")
  assert.equal(text(cell("p95")),"0.00")
  assert.equal(text(cell("totalEnergy")),"미제공")
  assert.equal(text(cell("incrementalEnergy")),"2.0000")
  const server=table.props.server
  assert.ok(server&&typeof server==="object"&&"total" in server)
  assert.equal(server.total,7)
  const model=cell("model") as Element
  assert.equal(new URL(String(model.props.href),"https://dashboard.invalid").pathname,"/measurements/results")
  assert.equal(elements(tree).some(element=>element.type==="main"||element.type==="measurement-nav"),false)
})

test("selected occupancy keeps valid zero distinct from absence and places originals after core values",()=>{
  for(const recorded of [true,false]) {
    const occupancy={...measurement,kind:"DispatchOccupancyMeasurement",conditions:{workload:"service",batchEnergyOptimization:"request",batch:1,warmupSeconds:1,durationSeconds:10,workers:1},status:recorded?{perRequestNanos:0n,throughput:"0"}:{}}
    const h=harness("kind=dispatch-occupancy&namespace=ns&measurement=cost&uid=original",path=>path.endsWith("/cost")?occupancy:{items:[occupancy]})
    const tree=projection(h)
    const content=elements(tree).find(element=>typeof element.type==="function"&&element.props.data===occupancy)!
    const selected=(content.type as Component)(content.props)
    const children=elements(selected)
    const core=children.find(element=>typeof element.type==="function"&&element.props.data===occupancy&&elements((element.type as Component)(element.props)).some(child=>child.type==="table"&&child.props["aria-label"]==="분배 처리시간 결과"))!
    assert.ok(core)
    const values=(core.type as Component)(core.props)
    const cells=elements(values).filter(element=>element.type==="td")
    assert.equal(text(cells[0]),recorded?"0.00":"미제공")
    assert.equal(text(cells[1]),recorded?"0":"미제공")
    assert.ok(children.indexOf(core)<children.findIndex(element=>element.type==="source-json"))
    assert.ok(children.filter(element=>element.type==="source-json").every(element=>element.props.inline===true))
    assert.equal(elements(tree).filter(element=>element.type==="sheet").length,1)
  }
})

test("empty operation measurements retain a selectable row instead of disappearing",()=>{
  const empty={...measurement,conditions:{steps:[]},status:{steps:[]}}
  const h=harness("kind=operation-cost&namespace=ns",path=>path.endsWith("operation-cost-measurements")?{items:[empty]}:undefined)
  const tree=projection(h)
  const costTable=elements(tree).find(element=>typeof element.type==="function"&&Array.isArray(element.props.items))!
  const table=(costTable.type as Component)(costTable.props) as Element
  const rows=table.props.data as unknown[],columns=table.props.columns as Column[]
  assert.equal(rows.length,1)
  assert.equal(text(columns.find(column=>column.key==="operation")!.value(rows[0])),"미기록")
  assert.equal(text(columns.find(column=>column.key==="duration-0")!.value(rows[0])),"미제공")
})

test("pending run evidence is not presented as a connection failure",()=>{
  const run={namespace:"ns",name:"running",uid:"run-uid",generation:1,spec:{},status:{conditions:[{type:"ExecutionComplete",status:"Unknown",observedGeneration:1}]},assessment:accepted}
  const h=harness("namespace=ns&uid=run-uid",path=>path.includes("evidence-passport")?{code:"EvidencePassportPending",run}:({runs:[run]}))
  Object.assign(h.modules,{"@/lib/measurement-coverage":coverage,"@/lib/measurement-request":requests,"@/lib/labels":{codeText:{}},react:{useState:(value:unknown)=>[value,()=>{}]}})
  const tree=load("../features/read-pages.tsx",h.modules).MeasurementDetail({namespace:"ns",name:"running"})
  assert.match(text(tree),/근거 발행 대기/)
  assert.doesNotMatch(text(tree),/연결 상태/)
  const refresh=elements(tree).find(element=>element.type==="button"&&text(element)==="근거 확인")!
  assert.ok(refresh,"pending evidence needs an explicit refresh path")
  ;(refresh.props.onClick as ()=>void)()
  assert.ok(h.refreshes.some(path=>path.endsWith("/evidence-passport")))
})

test("failed occupancy values remain visible only as unverified raw data",()=>{
  const failed={...measurement,kind:"DispatchOccupancyMeasurement",assessment:{...accepted,displayStatus:"problem"},conditions:{workload:"service"},status:{phase:"Failed",perRequestNanos:1880000,throughput:"87.1",reason:"evidence missing"}}
  const h=harness("kind=dispatch-occupancy&namespace=ns",path=>path.endsWith("dispatch-occupancy-measurements")?{items:[failed]}:undefined)
  const tree=projection(h),table=elements(tree).find(element=>element.type==="data-table")!
  const columns=table.props.columns as Column[]
  for(const key of ["d","x"]) {
    const cell=columns.find(column=>column.key===key)!.value(failed)
    assert.match(text(cell),/미검증 원자료/)
  }
})

test("opening a coverage cell pushes a closeable history entry while retaining the filtered matrix",()=>{
  const cell={id:"one",model:{name:"model",uid:"model"},deviceClass:{name:"device",uid:"device"},executionPath:"cpu",powerMode:"fixed",support:{verdict:"Compatible",reasons:[]},assessment:accepted,variants:[],failureSummary:[],history:[],batchProgress:null}
  const h=harness("model=model&deviceClass=device&status=usable",()=>undefined)
  Object.assign(h.modules,{
    react:{useRef:(current:unknown)=>({current}),useEffect:()=>{}},
    "@tanstack/react-query":{useQuery:({queryKey}:{queryKey:string[]})=>({data:queryKey[1]==="coverage-inventory"?{classes:[],models:[]}:{cells:[cell],scope:{targetBatches:[]},sources:[],nextActions:[]}})},
    "@/lib/measurement-coverage":coverage,"./measurement-coverage.css":{},
  })
  const tree=load("../features/measurement-coverage.tsx",h.modules).MeasurementCoveragePage({})
  const chosen=elements(tree).find(element=>element.props.cell===cell&&typeof element.props.select==="function")!
  ;(chosen.props.select as (cell:unknown,button:unknown)=>void)(cell,{})
  assert.equal(h.replacements.length,0)
  const opened=new URL(h.pushes[0],"https://test").searchParams
  assert.equal(opened.get("cell"),"one")
  assert.equal(opened.get("status"),"usable")
  assert.equal(opened.get("deviceClass"),"device")
})

test("standalone plan verification states its stored-plan and non-submission role",()=>{
  const h=harness("",()=>undefined)
  Object.assign(h.modules,{react:{useState:(value:unknown)=>[value,()=>{}]},"@/lib/client":{useSession:()=>({config:{namespace:"ns"}}),useChillMutation:()=>({})}})
  const tree=load("../features/read-pages.tsx",h.modules).MeasurementCheckPage({})
  assert.equal((tree as Element).props.title,"저장 계획 검사")
  assert.match(text(tree),/새 요청 작성/)
  const facts=elements(tree).find(element=>element.type==="facts")!
  assert.deepEqual(facts.props.items,[["대상","저장된 계획 파일"],["동작","검사만"],["측정 제출","없음"]])
})

test("editable execution path filters accept off-page identifiers and deduplicate suggestions",()=>{
  const h=harness("namespace=ns&path=off-page-path&node=device&page=3&offset=50&profile=selected",()=>undefined)
  const filters=load("../features/measurement-coverage.tsx",{...h.modules,"./measurement-coverage.css":{}}).MeasurementFilters
  const paths=["firstfloor-ort-cuda-arm64","firstfloor-rnn-ort-cuda-arm64","firstfloor-vlm-ort-cuda-arm64"]
  const axes=[{key:"path",label:"실행 경로",values:[...paths,paths[0],""]}]
  const tree=filters({editable:true,states:false,axes})
  const input=elements(tree).find(element=>element.type==="input"&&element.props.list==="measurement-path")!
  assert.ok(input)
  assert.equal(input.props.value,"off-page-path")
  assert.equal(elements(tree).some(element=>element.type==="select"),false)
  const suggestions=elements(elements(tree).find(element=>element.type==="datalist"))
    .filter(element=>element.type==="option")
  assert.deepEqual(suggestions.map(element=>element.props.value),paths)
  assert.equal(new Set(suggestions.map(text)).size,paths.length)
  ;(input.props.onChange as (event:{target:{value:string}})=>void)({target:{value:"another-page-path"}})
  const next=new URL(h.replacements[0],"https://dashboard.invalid").searchParams
  assert.equal(next.get("path"),"another-page-path")
  assert.equal(next.get("namespace"),"ns")
  assert.equal(next.get("node"),"device")
  for(const key of ["page","offset","profile"])assert.equal(next.has(key),false)
  const select=elements(filters({states:false,axes})).find(element=>element.type==="select")!
  const options=elements(select).filter(element=>element.type==="option")
  assert.equal(options.filter(element=>element.props.value===paths[0]).length,1)
  assert.equal(options.filter(element=>element.props.value==="off-page-path").length,1)
})

test("selected cost product shows comparison insufficiency once outside individual transition values",()=>{
  const h=harness("kind=operation-cost&namespace=ns&measurement=cost&uid=original",path=>path.endsWith("/cost")?measurement:{items:[measurement]})
  const tree=projection(h)
  const content=elements(tree).find(element=>typeof element.type==="function"&&element.props.data===measurement)!
  const selected=(content.type as Component)(content.props)
  const values=elements(selected).filter(element=>typeof element.type==="function"&&element.props.data===measurement)
    .map(element=>(element.type as Component)(element.props))
    .find(node=>elements(node).some(element=>element.props["aria-label"]==="전환 1회 비용"))
  assert.ok(values)
  assert.equal(text(values).split("판정 불가 · 비교 표본 부족").length-1,1)
  const individual=elements(values).find(element=>element.type==="details"&&text(element.props.children).includes("개별 전환 실측값·편차"))!
  assert.ok(individual)
  assert.doesNotMatch(text(individual),/판정 불가 · 비교 표본 부족/)
  assert.match(text(individual),/actual-device-name/)
  assert.match(text(individual),/미제공/)
})

test("selected throughput explains absent quality fields without inventing sample counts or criteria",()=>{
  for(const state of ["unknown","pass","fail"]) {
    const assessmentValue={...accepted,checklist:{criteriaMet:state==="pass"?true:null,checks:state==="unknown"?[]:[{code:"Recorded",result:state,message:"",sourceIds:[]}]}}
    const row={id:"row",batch:2,activeTrialCount:0,idleTrialCount:null,latencySampleCount:17} as ResultRow
    const profile={id:"selected",nodeName:"device",condition:{logicalModel:"model",artifact:{name:"artifact",executionPath:"ort"},preprocessor:{name:""},activeProtocol:{},cpu:{limit:"1"}},assessment:assessmentValue,resultRows:[row]}
    const h=harness("kind=throughput&profile=selected",()=>({profiles:[profile]}))
    const tree=load("../features/scheduler-profiles.tsx",h.modules).SchedulerProfilesPage({})
    const evidence=elements(tree).find(element=>typeof element.type==="function"&&element.props.rows===profile.resultRows&&text(element).includes("품질 판정 불가 · 기준·표본 제공 여부"))
    assert.equal(!!evidence,state==="unknown")
    if(!evidence)continue
    const rendered=(evidence.type as Component)(evidence.props)
    const rows=elements(rendered).filter(element=>element.type==="tr").slice(1)
    const cells=rows.map(row=>elements(row).filter(element=>element.type==="th"||element.type==="td").map(text))
    for(const label of ["허용 오차 반폭 기준","최소 활성·유휴 반복 수 기준","최소 지연 표본 수 기준","p95 신뢰구간"])assert.deepEqual(cells.find(row=>row[0]===label),[label,"미제공","현재 조회 계약에 해당 필드 없음"])
    assert.deepEqual(cells.find(row=>row[0]==="배치 2 · 활성 반복 수"),["배치 2 · 활성 반복 수","0","관측 표본 수 · 충족 기준 미제공"])
    assert.deepEqual(cells.find(row=>row[0]==="배치 2 · 유휴 반복 수"),["배치 2 · 유휴 반복 수","미제공","조회 결과에 값 없음"])
    assert.deepEqual(cells.find(row=>row[0]==="배치 2 · 지연 표본 수"),["배치 2 · 지연 표본 수","17","관측 표본 수 · 충족 기준 미제공"])
  }
})

test("namespace form pattern compiles with HTML v flag and enforces DNS labels",()=>{
  const h=harness("kind=dispatch-occupancy",()=>({items:[]}))
  const frame=load("../features/measurement-projections.tsx",h.modules).MeasurementResultsPage({})
  const form=elements(frame).find(item=>typeof item.type==="function"&&Object.hasOwn(item.props,"namespace")&&!Object.hasOwn(item.props,"kind"))!
  const input=elements((form.type as Component)(form.props)).find(item=>item.type==="input")!
  const pattern=new RegExp(`^(?:${String(input.props.pattern)})$`,"v")
  assert.equal(pattern.test("BAD_NAMESPACE"),false)
  assert.equal(pattern.test("chill-system"),true)
  for(const invalid of ["-chill","chill-","a_b","a.b"])assert.equal(pattern.test(invalid),false)
})
