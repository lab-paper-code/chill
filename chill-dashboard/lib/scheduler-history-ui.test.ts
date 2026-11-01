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
    "@/lib/client":{useSession:()=>({token:"token",config:{pageSize:25,maxPageSize:100,maxWindowHours:24}}),useChillQuery:(path:string)=>{calls.push(path);return responses[path]??(path==="v1/workloads"?{data:[{desired:{metadata:{name:"서비스 이름",uid:"service-uid"}}}]}:{data:path.startsWith("v1/observations/scheduler?")?{events:[]}:undefined})}},
    "@/lib/scheduler":scheduler,"@/lib/scheduler-history":history,"@/lib/capacity":capacity,"@/lib/live-display":liveDisplay,"@/lib/date-time":dates,"@/lib/workload-identity":identity,"@/lib/labels":labels,
    "@/components/read-region":{QueryGate:"gate",SourceJSON:"source",SourceExport:"export",StatusBadge:"badge"},
    "@/components/screen":{Screen:"main",Rows:"rows",Row:"row",RowDialog:"dialog",Facts:"facts",Empty:"empty"},
    "@/components/data-table":{DataTable:"table"},"@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},
    "@/components/ui/sheet":{},"@/components/records-navigation":{RecordsNavigation:"navigation",RecordsRange:"range"},
  }
  function load(file:string){
    const exports:Record<string,(props:Record<string,unknown>)=>Element>={}
    const compiled=transpileModule(readFileSync(new URL(file,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
    new Function("require","exports",compiled)((name:string)=>modules[name]??(name.startsWith("@/")||name.startsWith("./")?{}:require(name)),exports)
    return exports
  }
  return {load,calls,urls,modules}
}

test("judgment history defaults to decision/action on the server, not mixed power records",()=>{
  const h=harness(),screen=h.load("../features/scheduler-history.tsx")
  screen.SchedulerHistoryPage({})
  const request=new URL(h.calls.find(path=>path.startsWith("v1/observations/scheduler?"))!,"https://dashboard.invalid")
  assert.equal(request.searchParams.get("filter"),"decision-action")
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
