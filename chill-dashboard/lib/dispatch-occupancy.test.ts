import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as occupancy from "./dispatch-occupancy.ts"
import * as measurements from "./measurements.ts"
import * as scheduler from "./scheduler.ts"
import * as summary from "./measurement-summary.ts"
import type { CoverageOccupancy, DispatchOccupancyMeasurement, MeasurementCoverageCell, OccupancyMutation, OccupancyTask } from "./read-types.ts"

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
    "@/features/measurement-coverage":{useMeasurementCoverage:()=>({data:{cells:[{currentMeasurement:{id:"ns/beo/beo-uid"}}]},isPending:false}),useCoverageInventory:()=>({isPending:false,data:{classes:[{name:"class",nodes:[{name:"node",uid:"node-uid",resourceVersion:"1",ready:"True",schedulingDisabled:false}]}]}})},
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
  assert.equal(call.input.version,"7")
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
    "@/features/measurement-coverage":{MeasurementFilters:"filters"},
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
  const coverage=h.modules["@/features/measurement-coverage"] as {useMeasurementCoverage:()=>unknown}
  coverage.useMeasurementCoverage=()=>({data:{cells:[{currentMeasurement:{id:"ns/beo/beo-uid"},variants:[{profileIds:[profileID]}]}]},isPending:false})
  click(h.render(),"검사")
  assert.deepEqual(h.calls[0][0].input.body,{node:"node",batchEnergyOptimization:"beo",workload:"workload",batch:4})
  const stale=harness("namespace=ns&measurementID=ns%2Fbeo%2Freplaced-uid&workloadUID=workload-uid&batch=4")
  click(stale.render(),"검사")
  assert.equal(stale.calls[0].length,0)
  assert.match(text(stale.render()),/식별자 불일치/)
  assert.equal(occupancy.occupancyMetrics({...dom,d:null,status:{perRequestNanos:999}}),null)
  assert.equal(occupancy.occupancyNumber(-1),"미제공")
  assert.equal(occupancy.occupancyNumber(1.1,true),"미제공")
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
