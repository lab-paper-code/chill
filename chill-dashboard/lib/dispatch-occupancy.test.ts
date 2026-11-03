import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as occupancy from "./dispatch-occupancy.ts"
import * as measurements from "./measurements.ts"
import * as scheduler from "./scheduler.ts"
import * as summary from "./measurement-summary.ts"
import type { CoverageOccupancy, DispatchOccupancyMeasurement, MeasurementCoverageCell, OccupancyEstimate, OccupancyMutation, OccupancyTask } from "./read-types.ts"

type Element={type:unknown;props:Record<string,unknown>}
type Component=(props:Record<string,unknown>)=>unknown
type Input={path:string;method:string;body?:unknown;version?:string}
type Call={input:Input;options?:{onSuccess?:(response:unknown)=>void;onError?:(error:unknown)=>void}}
const require=createRequire(import.meta.url)
function nodes(value:unknown):Element[] {
  if(Array.isArray(value))return value.flatMap(nodes)
  if(!value||typeof value!=="object"||!("props" in value))return []
  const node=value as Element;return [node,...nodes(node.props.children)]
}
function text(value:unknown):string {
  if(Array.isArray(value))return value.map(text).join("")
  if(value==null||typeof value==="boolean")return ""
  if(typeof value!=="object")return String(value)
  const node=value as Element
  return text(typeof node.type==="function"?(node.type as Component)(node.props):node.props?.children)
}
function load(file:string,modules:Record<string,unknown>,names:string[]=[]) {
  const source=readFileSync(new URL(file,import.meta.url),"utf8")+(names.length?`\nexport {${names.join(",")}}`:"")
  const exports:Record<string,Component>={}
  const code=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",code)((name:string)=>modules[name]??(name.startsWith("@/")?{}:require(name)),exports)
  return exports
}
const plan={metadata:{namespace:"ns",name:"beo",uid:"beo-uid",resourceVersion:"1",creationTimestamp:"2026-11-03T00:00:00Z"},spec:{state:{candidate:{modelSpec:"model",deviceClass:"class",executionPath:"cpu",powerMode:"fixed"},targetNode:"node"}}}
const workload={desired:{metadata:{name:"workload",uid:"workload-uid",resourceVersion:"1"},spec:{modelRef:{name:"model"}}}}
const task={metadata:{namespace:"ns",name:"occ-generated",uid:"occ-uid",resourceVersion:"7",creationTimestamp:"2026-11-03T00:00:00Z"},spec:{node:"node",batchEnergyOptimization:"beo",workload:"workload",batch:4,supplyMode:"closed-loop",warmupSeconds:2,durationSeconds:8,workers:7,minimumBatches:11,maximumIdleFraction:"0.05",conditions:{profileID:"ns/beo/beo-uid"}}} as OccupancyTask
const dom={kind:"DispatchOccupancyMeasurement",metadata:task.metadata,node:"node",conditions:task.spec,status:{},phase:null,createdAt:task.metadata.creationTimestamp,d:null,references:{cr:{apiVersion:"v1",kind:"DispatchOccupancyMeasurement",namespace:"ns",name:"occ-generated",uid:"occ-uid"},evidence:[]}} as DispatchOccupancyMeasurement
function harness(search="namespace=ns&profileID=ns%2Fbeo%2Fbeo-uid&node=node&nodeUID=node-uid&workloadUID=workload-uid&batch=4") {
  const states:unknown[]=[],replacements:string[]=[],calls:Call[][]=[[],[]]
  let stateIndex=0,mutationIndex=0
  const mutations=[0,1].map(index=>({isPending:false,error:null as unknown,data:undefined as unknown,reset(){this.error=null;this.data=undefined},mutate(input:Input,options?:Call["options"]){calls[index].push({input,options})}}))
  const responses:Record<string,unknown>={"v1/namespaces/ns/batch-energy-optimizations?view=summary":{items:[plan]},"v1/workloads?view=summary":[workload],"v1/namespaces/ns/dispatch-occupancy-measurements":{items:[]}}
  const params=new URLSearchParams(search)
  const modules:Record<string,unknown>={
    react:{useState:(initial:unknown)=>{const index=stateIndex++;if(!(index in states))states[index]=typeof initial==="function"?(initial as ()=>unknown)():initial;return [states[index],(value:unknown)=>{states[index]=typeof value==="function"?(value as (previous:unknown)=>unknown)(states[index]):value}]},useRef:()=>({current:null})},
    "next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:(url:string)=>replacements.push(url),push:(url:string)=>replacements.push(url)})},"next/link":{default:"a"},
    "lucide-react":{TriangleAlert:"triangle-alert"},
    "@/lib/client":{decodeResponse:(body:string,decoder:(body:string)=>unknown)=>decoder(body),useSession:()=>({config:{namespace:"ns"}}),useChillQuery:(path:string)=>({data:responses[path],isPending:false,isFetching:false,error:null}),useChillMutation:()=>mutations[mutationIndex++]},
    "@/hooks/use-measurement-data":{useMeasurementPlans:()=>({data:responses["v1/namespaces/ns/batch-energy-optimizations?view=summary"],isPending:false,isFetching:false,error:null}),useMeasurementCoverage:()=>({data:{cells:[{currentMeasurement:{id:"ns/beo/beo-uid"}}]},isPending:false}),useCoverageInventory:()=>({isPending:false,data:{classes:[{name:"class",nodes:[{name:"node",uid:"node-uid",resourceVersion:"1",ready:"True",schedulingDisabled:false}]}]}})},
    "@/lib/dispatch-occupancy":occupancy,"@/lib/measurements":measurements,"@/lib/scheduler":scheduler,"@/lib/measurement-summary":summary,
    "@/components/read-region":{QueryGate:"gate",SourceJSON:"source-json"},"@/components/screen":{Screen:"main",Facts:"facts"},
    "@/components/measurement-navigation":{MeasurementNavigation:"measurement-nav"},"@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},
    "@/components/ui/dialog":{Dialog:"dialog",DialogContent:"dialog-content",DialogHeader:"dialog-header",DialogTitle:"dialog-title",DialogDescription:"dialog-description"},
  }
  const feature=load("../features/dispatch-occupancy-request.tsx",modules)
  function render(component=feature.DispatchOccupancyRequestPage,props:Record<string,unknown>={}) {stateIndex=0;mutationIndex=0;return component(props)}
  return {render,modules,mutations,calls,replacements,responses,params}
}
const button=(tree:unknown,label:string)=>nodes(tree).find(node=>node.type==="button"&&text(node)===label)!
function click(tree:unknown,label:string) {const node=button(tree,label);assert.ok(node,label);assert.ok(!node.props.disabled,label);(node.props.onClick as ()=>void)()}

test("d requests reject invalid selection and stale checks before allowing a protocol-free submission",()=>{
  const invalid=harness("namespace=ns&batch=0")
  click(invalid.render(),"검사")
  assert.equal(invalid.calls[0].length,0)
  assert.match(text(invalid.render()),/워크로드를 선택/)
  const h=harness()
  assert.equal(button(h.render(),"측정 요청").props.disabled,true)
  click(h.render(),"검사")
  assert.equal(h.calls[0][0].input.path,"v1/namespaces/ns/dispatch-occupancy-measurements?dryRun=All")
  assert.deepEqual(h.calls[0][0].input.body,{node:"node",batchEnergyOptimization:"beo",workload:"workload",batch:4})
  h.calls[0][0].options?.onSuccess?.({body:JSON.stringify({request:"NOT_SUBMITTED",task})})
  let tree=h.render()
  const protocol=nodes(tree).find(node=>Object.hasOwn(node.props,"task"))!
  assert.match(text(protocol),/읽기 전용/)
  assert.match(text(protocol),/동시 송신자.*7/)
  assert.equal(nodes((protocol.type as Component)(protocol.props)).filter(node=>["input","select"].includes(String(node.type))).length,0)
  click(tree,"측정 요청")
  assert.deepEqual(h.calls[1][0].input.body,{name:"occ-generated",node:"node",batchEnergyOptimization:"beo",workload:"workload",batch:4})
  h.calls[1][0].options?.onSuccess?.({data:{request:"Accepted",task}})
  const destination=new URL(h.replacements[0],"https://dashboard.invalid")
  assert.equal(destination.pathname,"/measurements/results")
  assert.equal(destination.searchParams.get("kind"),"dispatch-occupancy")
  assert.equal(destination.searchParams.get("uid"),"occ-uid")
  h.responses["v1/namespaces/ns/batch-energy-optimizations?view=summary"]={items:[{...plan,metadata:{...plan.metadata,resourceVersion:"changed"}}]}
  tree=h.render()
  assert.equal(button(tree,"측정 요청").props.disabled,true,"resource identity changes invalidate a check")
})

test("d dry-run failure states and NOT_SUBMITTED never navigate or imply acceptance",()=>{
  for(const [code,status,label] of [["OccupancyInfeasible",400,"측정 조건 불충족"],["OccupancyPolicyUnavailable",503,"측정 정책 사용 불가"]] as const) {
    const h=harness();click(h.render(),"검사")
    h.mutations[0].error={status,data:{code},body:code}
    const tree=h.render()
    assert.match(text(tree),new RegExp(label))
    assert.equal(button(tree,"측정 요청").props.disabled,true)
    assert.deepEqual(h.replacements,[])
  }
  const h=harness();click(h.render(),"검사");h.calls[0][0].options?.onSuccess?.({body:JSON.stringify({request:"NOT_SUBMITTED",task})})
  click(h.render(),"측정 요청")
  h.mutations[1].data={data:{request:"NOT_SUBMITTED",task}}
  h.calls[1][0].options?.onSuccess?.({data:{request:"NOT_SUBMITTED",task}})
  assert.match(text(h.render()),/미제출/)
  assert.deepEqual(h.replacements,[])
})

test("uncertain d submission blocks retries and opens only a recorded server identity",()=>{
  const h=harness();click(h.render(),"검사");h.calls[0][0].options?.onSuccess?.({body:JSON.stringify({request:"NOT_SUBMITTED",task})})
  click(h.render(),"측정 요청")
  h.calls[1][0].options?.onError?.({status:0,unknownOutcome:true})
  assert.equal(button(h.render(),"측정 요청").props.disabled,true)
  assert.match(text(h.render()),/자동 재제출 차단/)
  h.responses["v1/namespaces/ns/dispatch-occupancy-measurements"]={items:[dom]}
  const link=nodes(h.render()).find(node=>node.type==="a"&&text(node)==="접수된 측정 열기")!
  assert.equal(new URL(String(link.props.href),"https://dashboard.invalid").searchParams.get("uid"),"occ-uid")
})

test("Measuring deletion is disabled; confirmed deletion sends pinned UID and If-Match and handles 409",()=>{
  const h=harness(),feature=load("../features/measurement-projections.tsx",h.modules,["DeleteOccupancy"])
  const props={data:{...dom,phase:"Measuring"},refresh:()=>{},onDeleted:()=>{}}
  let tree=h.render(feature.DeleteOccupancy,props)
  assert.equal(button(tree,"측정 중").props.disabled,true)
  const ready={...props,data:{...dom,phase:"Complete"}}
  tree=h.render(feature.DeleteOccupancy,ready)
  const checkbox=nodes(tree).find(node=>node.type==="input")!
  ;(checkbox.props.onChange as (event:unknown)=>void)({target:{checked:true}})
  tree=h.render(feature.DeleteOccupancy,ready)
  const confirm=nodes(tree).filter(node=>node.type==="button"&&text(node)==="삭제").at(-1)!
  assert.equal(confirm.props.disabled,false)
  ;(confirm.props.onClick as ()=>void)()
  const call=h.calls[0][0]
  assert.equal(call.input.method,"DELETE")
  assert.equal(call.input.version,'"7"')
  assert.equal(new URL(call.input.path,"https://dashboard.invalid").searchParams.get("uid"),"occ-uid")
  call.options?.onError?.({status:409})
  h.mutations[0].error={status:409}
  tree=h.render(feature.DeleteOccupancy,ready)
  assert.match(text(tree),/삭제 충돌/)
  assert.equal(nodes(tree).find(node=>node.type==="dialog")?.props.open,false)
  assert.ok(button(tree,"최신 상태 조회"))
})

test("coverage d links retain conditions, use the current profile for superseded evidence, and preserve signed d",()=>{
  const row={node:{name:"node",uid:"node-uid"},profileID:"ns/old/old-uid",batch:4,state:"none",reason:"ProfileSuperseded",adopted:null} as CoverageOccupancy
  const cell={model:{name:"model"},deviceClass:{name:"class"},executionPath:"cpu",powerMode:"fixed",currentMeasurement:{id:"ns/current/current-uid"}} as MeasurementCoverageCell
  const query=new URL(occupancy.coverageOccupancyRequestHref(cell,row,"ns"),"https://dashboard.invalid").searchParams
  assert.equal(query.get("measurementID"),"ns/current/current-uid")
  for(const [key,value] of Object.entries({kind:"dispatch-occupancy",model:"model",deviceClass:"class",path:"cpu",powerMode:"fixed",node:"node",nodeUID:"node-uid",batch:"4"}))assert.equal(query.get(key),value)
  assert.equal(occupancy.occupancyReasonLabel(row.reason),"이전 프로파일 측정")
  assert.equal(occupancy.occupancyReasonLabel("NotSaturated"),"부하 미포화")
  assert.equal(occupancy.occupancyMilliseconds(-1000000n),"-1.00")
  assert.equal(occupancy.occupancyMilliseconds(null),"미제공")
  assert.equal(occupancy.occupancyNumber(0),"0")
  assert.equal(occupancy.occupancyRunning({...dom,phase:null}),true)
  assert.equal(occupancy.occupancyRunning({...dom,phase:"Insufficient"}),false)
  assert.equal(occupancy.occupancyElapsed({...dom,phase:"Complete",measurementWindow:{start:"2026-11-03T00:01:00Z",end:"2026-11-03T00:01:08Z"}}),"8 s")
  assert.equal(occupancy.occupancyElapsed(dom,Date.parse("2026-11-03T00:00:12Z")),"12 s")
  assert.equal(occupancy.occupancyChecked("changed","checked",{request:"NOT_SUBMITTED",task} as OccupancyMutation),false)
})

test("occupancy result polling stops on terminal phases and shows projected values without stale status fallback",()=>{
  const h=harness("namespace=ns&measurement=occ-generated&uid=occ-uid")
  const polls:((data:unknown)=>boolean)[]=[]
  const projected={...dom,phase:"Insufficient",reason:"NotSaturated",status:{phase:"Measuring",perRequestNanos:999999999},d:{perRequestNanos:-1000000,throughput:0,idleFraction:0.2,verdict:"Insufficient"},measurementWindow:{start:"2026-11-03T00:01:00Z",end:"2026-11-03T00:01:08Z"}}
  Object.assign(h.modules,{
    "@/lib/client":{useChillQuery:(path:string,options:{poll:(data:unknown)=>boolean})=>{polls.push(options.poll);return {data:path.endsWith("/occ-generated")?projected:{items:[projected]},dataUpdatedAt:Date.parse("2026-11-03T00:02:00Z"),error:null,refetch:()=>{}}}},
    "@/components/status-cell":{StatusCell:"status-cell"},
    "@/components/measurement-filters":{MeasurementFilters:"filters"},
    "@/components/data-table":{DataTable:"data-table"},
    "@/components/ui/sheet":{Sheet:"sheet",SheetContent:"sheet-content",SheetHeader:"sheet-header",SheetTitle:"sheet-title",SheetDescription:"sheet-description"},
  })
  const feature=load("../features/measurement-projections.tsx",h.modules,["ProjectionResults","OccupancyResult"])
  const tree=h.render(feature.ProjectionResults,{kind:"dispatch-occupancy-measurements",namespace:"ns"})
  assert.equal(polls[0]({items:[projected]}),false)
  assert.equal(polls[0]({items:[{...projected,phase:"Measuring"}]}),true)
  assert.equal(polls[1]({...projected,phase:null}),true)
  assert.equal(polls[1](projected),false)
  const table=nodes(tree).find(node=>node.type==="data-table")!
  const columns=table.props.columns as {key:string;value:(item:unknown)=>unknown}[]
  assert.equal(text(columns.find(column=>column.key==="d")!.value(projected)),"미검증 원자료 · -1.00")
  const phase=columns.find(column=>column.key==="phase")!.value(projected) as Element
  const status=(phase.type as Component)(phase.props) as Element
  assert.equal(status.props.label,"근거 부족")
  const detail=h.render(feature.OccupancyResult,{data:projected})
  assert.match(text(detail),/-1\.00/)
  assert.match(text(detail),/20\.00/)
  const facts=nodes(detail).find(node=>node.type==="facts")?.props.items as [string,unknown][]
  assert.equal(facts.find(([label])=>label==="경과")?.[1],"8 s")
})

test("reused profile IDs resolve through server cell lineage, never by treating profileID as a BEO identity",()=>{
  const profileID="ns/beo/beo-uid/reuse-node-other"
  const h=harness(`namespace=ns&profileID=${encodeURIComponent(profileID)}&node=node&nodeUID=node-uid&workloadUID=workload-uid&batch=4`)
  const coverage=h.modules["@/hooks/use-measurement-data"] as {useMeasurementCoverage:()=>unknown}
  coverage.useMeasurementCoverage=()=>({data:{cells:[{currentMeasurement:{id:"ns/beo/beo-uid"},variants:[{profileIds:[profileID]}]}]},isPending:false})
  click(h.render(),"검사")
  assert.deepEqual(h.calls[0][0].input.body,{node:"node",batchEnergyOptimization:"beo",workload:"workload",batch:4})
  const stale=harness("namespace=ns&measurementID=ns%2Fbeo%2Freplaced-uid&workloadUID=workload-uid&batch=4")
  click(stale.render(),"검사")
  assert.equal(stale.calls[0].length,0)
  assert.match(text(stale.render()),/식별자 불일치/)
  assert.equal(occupancy.occupancyNumber(-1),"미제공")
  assert.equal(occupancy.occupancyNumber(1.1,true),"미제공")
})

test("frozen saturation sizing supplies read-only rows with exact integers and unavailable values",()=>{
  const h=harness()
  const feature=load("../features/dispatch-occupancy-request.tsx",h.modules)
  const inputsJSON='{"computed":{"saturationSizing":{"requiredWorkers":9007199254740993,"workerCap":31,"byteCapWorkers":23,"lifetimeCapWorkers":19,"selectedWorkers":17,"limitingBound":"lifetime","fullQueuedBatches":9007199254740995,"refillSeconds":1.25,"residualIdleSeconds":0,"estimatedIdleFraction":0.025,"maximumIdleFraction":0.04}},"source":{"name":"raw-only"}}'
  const render=(inputsJSON:unknown)=>feature.OccupancyProtocol({task:{...task,spec:{...task.spec,conditions:{...task.spec.conditions,inputsJSON}}}})
  const rows=(tree:unknown)=>new Map(nodes(tree).filter(node=>node.type==="tr").map(node=>nodes(node.props.children).filter(child=>child.type==="th"||child.type==="td").map(text) as [string,string]))
  const tree=render(inputsJSON),values=rows(tree)
  for(const [label,value] of [["필요 송신자 (명)","9007199254740993"],["선택 송신자 (명)","17"],["송신자 상한 (명)","31"],["바이트 한도 송신자 (명)","23"],["수명 한도 송신자 (명)","19"],["제한 요인","lifetime"],["가득 찬 대기 배치 (개)","9007199254740995"],["재충전 (s)","1.25"],["잔여 유휴 (s)","0"],["예상 유휴 비율","0.025"],["포화 산정 최대 유휴 비율","0.04"]])assert.equal(values.get(label),value,label)
  assert.equal(nodes(tree).filter(node=>["input","select"].includes(String(node.type))).length,0)
  assert.equal(nodes(tree).find(node=>node.type==="source-json")?.props.value!==undefined,true)
  assert.doesNotMatch(text(tree),/raw-only/)
  for(const missing of [undefined,null,"not-json","null","{}",'{"computed":{"saturationSizing":null}}','{"computed":{"saturationSizing":{"requiredWorkers":null}}}']) {
    const unavailable=rows(render(missing))
    assert.equal(unavailable.get("필요 송신자 (명)"),"미제공")
    assert.equal(unavailable.get("선택 송신자 (명)"),"미제공")
    assert.equal(unavailable.get("예상 유휴 비율"),"미제공")
  }
})

test("dry-run shows only the returned native closed-loop fact, never a configurable supply mode",()=>{
  const h=harness(),feature=load("../features/dispatch-occupancy-request.tsx",h.modules)
  for(const supplyMode of ["closed-loop",undefined,"poisson"]) {
    const tree=feature.OccupancyProtocol({task:{...task,spec:{...task.spec,supplyMode}}})
    const values=new Map(nodes(tree).filter(node=>node.type==="tr").map(node=>nodes(node.props.children).filter(child=>child.type==="th"||child.type==="td").map(text) as [string,string]))
    assert.equal(values.has("공급 방식"),false)
    assert.equal(values.get("공급 방식 (서버 고정)"),supplyMode==="closed-loop"?"closed-loop":"미제공")
    assert.equal(nodes(tree).some(node=>["input","select"].includes(String(node.type))),false)
  }
})

test("W8 short bursts show returned pair facts without saturation sizing or idle criteria",()=>{
  const h=harness(),feature=load("../features/dispatch-occupancy-request.tsx",h.modules)
  const tree=feature.OccupancyProtocol({task:{...task,spec:{...task.spec,minimumPairs:10,conditions:{inputsJSON:'{"computed":{"saturationSizing":{"requiredWorkers":999}}}'}}}})
  assert.match(text(tree),/짧은 버스트/)
  assert.match(text(tree),/최소 쌍/)
  assert.match(text(tree),/배치 \(건\)/)
  assert.doesNotMatch(text(tree),/유휴|포화|필요 송신자/)
})

test("W8 observation identity matching excludes every mismatched key but not runtime images",()=>{
  const cell={model:{name:"model"},powerMode:"fixed",executionPath:"cpu"} as MeasurementCoverageCell
  const tuple={node:{uid:"node-uid"},batch:4,profileID:"profile"} as CoverageOccupancy
  const row={node:{uid:"node-uid"},model:"model",powerMode:"fixed",executionPath:"cpu",batch:4,profile:{profileID:"profile"},runtimeImage:"old"} as OccupancyEstimate
  assert.equal(occupancy.occupancyMatches(row,cell,tuple),true)
  assert.equal(occupancy.occupancyMatches({...row,runtimeImage:"different"},cell,tuple),true)
  for(const changed of [{node:{name:"node",uid:"recreated"}},{model:"other"},{powerMode:"other"},{executionPath:"other"},{batch:8},{profile:{...row.profile,profileID:"other"}}])assert.equal(occupancy.occupancyMatches({...row,...changed},cell,tuple),false)
  const start="2026-11-03T00:00:00Z",end="2026-11-03T00:01:00Z"
  assert.ok(occupancy.occupancyObservationPath(start,end,60))
  for(const [from,to,max] of [[start,end,59],[start,start,60],["bad",end,60]] as const)assert.equal(occupancy.occupancyObservationPath(from,to,max),null)
})

for(const maximumWindowSeconds of [undefined,60])test(`W8 period rejection preserves applied facts with server cap ${maximumWindowSeconds??"absent"}`,()=>{
  const h=harness(),paths:string[]=[]
  const window={start:"2026-11-03T00:00:00.000000001Z",end:"2026-11-03T00:01:00.000000001Z"}
  const statistics={median:-1000000,p25:-2000000,p75:0,iqr:2000000,p10:-3000000,p90:1000000,mean:-500000,negativeFraction:0.75}
  const row={node:{name:"node",uid:"node-uid"},model:"model",powerMode:"fixed",executionPath:"cpu",batch:4,profile:{profileID:"profile"},runtimeImage:"display-image",window,pairs:10,statistics}
  const cell={model:{name:"model"},powerMode:"fixed",executionPath:"cpu",occupancy:[{node:row.node,batch:4,profileID:"profile",schedulerD:{source:"measured",value:1,pairs:10,window,updatedAt:window.end}}]} as MeasurementCoverageCell
  let response:{data:unknown;error:unknown}={data:{window,rows:[row,{...row,node:{name:"recreated",uid:"wrong"}}]},error:null}
  Object.assign(h.modules.react as object,{useEffect:(effect:()=>void)=>effect()})
  h.modules["./measurement-coverage.css"]={}
  h.modules["@tanstack/react-query"]={}
  h.modules["@/lib/client"]={useChillQuery:(path:string)=>{paths.push(path);return {...response,isFetching:false,refetch(){}}}}
  const feature=load("../features/measurement-coverage.tsx",h.modules)
  const render=()=>h.render(feature.CoverageOccupancyPeriod,{cell})
  render();let tree=render()
  const first=new URL(paths[0],"https://dashboard.invalid")
  assert.equal(first.searchParams.get("from"),window.start)
  assert.equal(first.searchParams.get("to"),window.end)
  assert.match(text(tree),/display-image/)
  assert.doesNotMatch(text(tree),/recreated/)
  const inputs=nodes(tree).filter(node=>node.type==="input")
  ;(inputs[1].props.onChange as (event:unknown)=>void)({target:{value:"2026-11-03T00:02:00Z"}})
  tree=render();click(tree,"관측 조회")
  response={data:undefined,error:{status:400,data:{code:"OccupancyWindowExceeded",message:"window too long",...(maximumWindowSeconds===undefined?{}:{maximumWindowSeconds})},body:'{"code":"OccupancyWindowExceeded","message":"window too long"}'}}
  render();tree=render()
  assert.match(text(tree),/OccupancyWindowExceeded/)
  assert.match(text(tree),/window too long/)
  assert.match(text(tree),/적용 구간 2026-11-03T00:00:00\.000000001Z/)
  assert.match(text(tree),/display-image/)
  assert.equal(button(tree,"관측 조회").props.disabled,maximumWindowSeconds!==undefined)
  if(maximumWindowSeconds!==undefined)assert.match(text(tree),/최대 60 s/)
  else assert.doesNotMatch(text(tree),/최대 \d+ s/)
})

test("W8 DOM renders signed observed facts without scalar fallback or saturation verdicts",()=>{
  const h=harness(),feature=load("../features/measurement-projections.tsx",h.modules,["OccupancyResult"])
  for(const estimate of [null,{pairs:10,statistics:{median:-1000000.5,iqr:2000000,p10:-3000000,p90:1000000,negativeFraction:0.75},window:{start:"start",end:"end"}}]) {
    const projected={...dom,schemaVersion:"chill.dacs.io/dispatch-occupancy-measurement.v1alpha2",phase:"Complete",minimumPairs:10,estimate,d:{perRequestNanos:999000000,throughput:123,idleFraction:.5,verdict:"Accepted"}}
    const tree=h.render(feature.OccupancyResult,{data:projected})
    assert.match(text(tree),/관측 d\(B\)/)
    assert.match(text(tree),/IQR/)
    assert.doesNotMatch(text(tree),/유휴|포화|판정|Accepted|999\.00|미검증/)
    if(estimate){assert.match(text(tree),/-1\.00/);assert.match(text(tree),/75\.00/)}
    else assert.match(text(tree),/미제공/)
  }
})

test("P2 review normal Observed reasons use the estimate, never the absent or stale scalar",()=>{
  const h=harness()
  Object.assign(h.modules,{
    "@/lib/client":{useChillQuery:(_path:string,options:{enabled?:boolean})=>({data:options.enabled===false?undefined:{items:[]},dataUpdatedAt:Date.parse("2026-11-03T00:00:00Z"),error:null,refetch(){}})},
    "@/components/measurement-filters":{MeasurementFilters:"filters"},
    "@/components/data-table":{DataTable:"data-table"},
  })
  const feature=load("../features/measurement-projections.tsx",h.modules,["ProjectionResults"])
  const table=nodes(h.render(feature.ProjectionResults,{kind:"dispatch-occupancy-measurements",namespace:"ns"})).find(node=>node.type==="data-table")!
  const column=(table.props.columns as {key:string;value:(item:unknown)=>unknown}[]).find(column=>column.key==="reason")!
  for(const d of [null,{perRequestNanos:-999,throughput:0,idleFraction:1,verdict:"Insufficient"}]) {
    const projected={...dom,schemaVersion:"chill.dacs.io/dispatch-occupancy-measurement.v1alpha2",phase:"Complete",verdict:"Observed",estimate:{pairs:10,statistics:{median:-1}},d} as DispatchOccupancyMeasurement
    assert.equal(occupancy.occupancyDisplayReason(projected),"해당 없음")
    assert.equal(text(column.value(projected)),"해당 없음")
    assert.equal(projected.d,d,"reason rendering must not synthesize a scalar")
  }
})

test("P2 review direct detail exposes execution and evidence causes without quality verdicts",()=>{
  const h=harness(),feature=load("../features/measurement-projections.tsx",h.modules,["OccupancyResult"])
  for(const [phase,reason,label] of [["Failed","NativeExecutionError","실행 오류"],["Insufficient","TooFewPairs","TooFewPairs"],["Complete","OccupancyEvidenceMissing","점유시간 근거 파일 없음"]] as const) {
    const projected={...dom,schemaVersion:"chill.dacs.io/dispatch-occupancy-measurement.v1alpha2",phase,reason,minimumPairs:10,estimate:null}
    const tree=h.render(feature.OccupancyResult,{data:projected})
    const facts=nodes(tree).filter(node=>node.type==="facts").flatMap(node=>node.props.items as [string,unknown][])
    assert.match(String(facts.find(([key])=>key==="사유")?.[1]),new RegExp(label))
    assert.doesNotMatch(text(tree),/유휴|포화|판정|Accepted/)
  }
  const projected={...dom,schemaVersion:"chill.dacs.io/dispatch-occupancy-measurement.v1alpha2",phase:"Complete",estimate:null,assessment:{status:"failed",displayStatus:"problem",label:"근거 오류",execution:null,evidence:{available:false,accepted:false,reasons:[]},checklist:{criteriaMet:null,version:"result-checklist.v1",checks:[]},scope:{schedulerUsed:null,reused:false,facts:[]},reasons:[{code:"OccupancyEvidenceMissing",message:"missing burst evidence",sourceIds:[]},{code:"OccupancyEvidenceMismatch",message:"different request identity",sourceIds:[]}]}} as DispatchOccupancyMeasurement
  const tree=h.render(feature.OccupancyResult,{data:projected})
  const facts=nodes(tree).filter(node=>node.type==="facts").flatMap(node=>node.props.items as [string,unknown][])
  const reason=String(facts.find(([key])=>key==="사유")?.[1])
  for(const label of ["점유시간 근거 파일 없음","missing burst evidence","요청·점유시간 근거 불일치","different request identity"])assert.ok(reason.includes(label),label)
})

test("P2 review occupancy periods accept positive sub-millisecond windows and preserve raw URLs",()=>{
  for(const [from,to] of [
    ["2026-11-03T00:00:00.000000001Z","2026-11-03T00:00:00.000000002Z"],
    ["2026-11-03T09:00:00.000000001+09:00","2026-11-03T00:00:00.000000002Z"],
  ]) {
    const path=occupancy.occupancyObservationPath(from,to,1)
    assert.ok(path)
    const query=new URL(path,"https://dashboard.invalid").searchParams
    assert.equal(query.get("from"),from)
    assert.equal(query.get("to"),to)
    assert.equal(occupancy.occupancyObservationPath(to,from,1),null)
    assert.equal(occupancy.occupancyObservationPath(from,from,1),null)
  }
  for(const from of ["2026-02-30T00:00:00Z","2026-11-03T00:00:00+24:00","2026-11-03T00:00:00.0000000001Z"])assert.equal(occupancy.occupancyObservationPath(from,"2026-11-04T00:00:00Z",undefined),null)
})

test("P2 review occupancy period rejects a server cap exceeded by one nanosecond",()=>{
  const from="2026-11-03T00:00:00.000000001Z",atCap="2026-11-03T00:01:00.000000001Z",overCap="2026-11-03T00:01:00.000000002Z"
  assert.ok(occupancy.occupancyObservationPath(from,atCap,60))
  assert.ok(occupancy.occupancyObservationPath(from,overCap,undefined))
  assert.equal(occupancy.occupancyObservationPath(from,overCap,60),null)
})

test("P2 review occupancy period query stays enabled for a positive nanosecond interval",()=>{
  const h=harness(),paths:string[]=[]
  const window={start:"2026-11-03T00:00:00.000000001Z",end:"2026-11-03T00:00:00.000000002Z"}
  const cell={model:{name:"model"},powerMode:"fixed",executionPath:"cpu",occupancy:[{schedulerD:{window}}]} as MeasurementCoverageCell
  Object.assign(h.modules.react as object,{useEffect:(effect:()=>void)=>effect()})
  h.modules["./measurement-coverage.css"]={}
  h.modules["@tanstack/react-query"]={}
  h.modules["@/lib/client"]={useChillQuery:(path:string)=>{paths.push(path);return {isFetching:false,refetch(){}}}}
  const feature=load("../features/measurement-coverage.tsx",h.modules)
  const tree=h.render(feature.CoverageOccupancyPeriod,{cell})
  assert.equal(button(tree,"관측 조회").props.disabled,false)
  const query=new URL(paths[0],"https://dashboard.invalid").searchParams
  assert.equal(query.get("from"),window.start)
  assert.equal(query.get("to"),window.end)
})

test("dry-run protocol retains uint64 values and rejects unreadable previews before submission",()=>{
  const h=harness();click(h.render(),"검사")
  h.calls[0][0].options?.onSuccess?.({body:'{"request":"NOT_SUBMITTED","task":{"metadata":{"name":"precision"},"spec":{"seed":9007199254740993}}}'})
  const protocol=nodes(h.render()).find(node=>Object.hasOwn(node.props,"task"))!
  const preview=protocol.props.task as OccupancyTask
  assert.equal(preview.spec.seed,9007199254740993n)
  assert.match(text(protocol),/9007199254740993/)
  click(h.render(),"검사")
  h.calls[0][1].options?.onSuccess?.({body:"not-json"})
  assert.equal(button(h.render(),"측정 요청").props.disabled,true)
})

test("P2 d custom names remeasure failed conditions and changing the name invalidates the check",()=>{
  const h=harness()
  h.responses["v1/namespaces/ns/dispatch-occupancy-measurements"]={items:[{...dom,phase:"Failed"}]}
  const nameInput=(tree:unknown)=>nodes(tree).find(node=>node.type==="input"&&node.props.type!=="number")!
  const changeName=(value:string)=>{const input=nameInput(h.render());assert.ok(input,"optional request name input");(input.props.onChange as (event:unknown)=>void)({target:{value}})}
  changeName("occ-remeasure.2")
  click(h.render(),"검사")
  assert.deepEqual(h.calls[0][0].input.body,{name:"occ-remeasure.2",node:"node",batchEnergyOptimization:"beo",workload:"workload",batch:4})
  const fresh={...task,metadata:{...task.metadata,name:"occ-remeasure.2"}}
  h.calls[0][0].options?.onSuccess?.({body:JSON.stringify({request:"NOT_SUBMITTED",task:fresh})})
  assert.equal(button(h.render(),"측정 요청").props.disabled,false)
  changeName("occ-remeasure.3")
  assert.equal(button(h.render(),"측정 요청").props.disabled,true)
  click(h.render(),"검사")
  const newer={...fresh,metadata:{...fresh.metadata,name:"occ-remeasure.3"}}
  h.calls[0][1].options?.onSuccess?.({body:JSON.stringify({request:"NOT_SUBMITTED",task:newer})})
  click(h.render(),"측정 요청")
  assert.deepEqual(h.calls[1][0].input.body,{name:"occ-remeasure.3",node:"node",batchEnergyOptimization:"beo",workload:"workload",batch:4})
  assert.equal(h.calls.flat().some(call=>call.input.method==="DELETE"),false)
  for(const value of ["Bad_Name","-bad","bad..name","a".repeat(201)]) {
    changeName(value);const count=h.calls[0].length;click(h.render(),"검사")
    assert.equal(h.calls[0].length,count)
    assert.match(text(h.render()),/이름.*200자.*소문자/)
  }
})

test("P2 d resolves only the BEO target in a multi-node class and rejects pinned target mismatches",()=>{
  const inventory={useCoverageInventory:()=>({isPending:false,data:{classes:[{name:"class",nodes:[{name:"representative",uid:"representative-uid",ready:"True"},{name:"node",uid:"node-uid",ready:"True",resourceVersion:"1"}]}]}})}
  for(const suffix of ["","&node=representative&nodeUID=representative-uid","&node=node&nodeUID=old-uid","&nodeUID=old-uid"]) {
    const h=harness(`namespace=ns&measurementID=ns%2Fbeo%2Fbeo-uid&workloadUID=workload-uid&batch=4${suffix}`)
    Object.assign(h.modules["@/hooks/use-measurement-data"] as object,inventory)
    const tree=h.render(),label=nodes(tree).find(node=>node.type==="label"&&text(node).startsWith("Node"))!
    const select=nodes(label).find(node=>node.type==="select")!
    assert.equal(select.props.value,"node")
    assert.deepEqual(nodes(select).filter(node=>node.type==="option"&&node.props.value).map(node=>node.props.value),["node"])
    click(tree,"검사")
    if(suffix) {
      assert.equal(h.calls[0].length,0)
      assert.match(text(h.render()),/식별자 불일치/)
    } else assert.deepEqual(h.calls[0][0].input.body,{node:"node",batchEnergyOptimization:"beo",workload:"workload",batch:4})
  }
})

test("P2 d kind transition remains blocked while a mutation is pending or submission is uncertain",()=>{
  const h=harness()
  const selector=(tree:unknown)=>{const node=nodes(tree).find(node=>typeof node.type==="function"&&node.props.kind==="dispatch-occupancy")!;return nodes((node.type as Component)(node.props)).find(node=>node.type==="select")!}
  assert.equal(selector(h.render()).props.disabled,false)
  for(const mutation of h.mutations) {
    mutation.isPending=true
    assert.equal(selector(h.render()).props.disabled,true)
    mutation.isPending=false
  }
  click(h.render(),"검사");h.calls[0][0].options?.onSuccess?.({body:JSON.stringify({request:"NOT_SUBMITTED",task})})
  click(h.render(),"측정 요청");h.calls[1][0].options?.onError?.({status:0,unknownOutcome:true})
  assert.equal(selector(h.render()).props.disabled,true)
})
