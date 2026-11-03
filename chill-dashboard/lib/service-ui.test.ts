import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as domain from "./domain.ts"
import * as labels from "./labels.ts"
import * as scheduler from "./scheduler.ts"
import * as liveDisplay from "./live-display.ts"
import type { WorkloadView } from "./domain.ts"

type Element = { type: unknown; props: Record<string, unknown> }
type Component = (props: Record<string, unknown>) => Element
const require = createRequire(import.meta.url)
function load(file: string, modules: Record<string, unknown>) {
  const compiled = transpileModule(readFileSync(new URL(file, import.meta.url), "utf8"), { compilerOptions: { target: ScriptTarget.ES2022, module: ModuleKind.CommonJS, jsx: JsxEmit.ReactJSX } }).outputText
  const exports: Record<string, Component> = {}
  new Function("require", "exports", compiled)((name: string) => Object.hasOwn(modules, name) ? modules[name] : name.startsWith("@/") ? {} : require(name), exports)
  return exports
}
function elements(node: unknown): Element[] {
  if (Array.isArray(node)) return node.flatMap(elements)
  if (!node || typeof node !== "object" || !("props" in node)) return []
  const element = node as Element
  return [element, ...elements(element.props.children)]
}
const idle = { desired: { metadata: { name: "idle", uid: "service-uid" }, spec: { modelRef: { name: "model" }, preprocessorRef: { name: "input" }, slo: { percentile: 95, targetLatency: "1s" } } }, admission: true, readiness: "Ready", pending: 0, assigned: 0, reason: "", supportedInputs: [], observedAt: "2026-11-01T00:00:00Z" } as unknown as WorkloadView
function harness(query="", responses: Record<string, unknown>={}) {
  const paths: string[] = [], replacements: string[] = [], writes: unknown[] = []
  const modules: Record<string, unknown> = {
    react: { useState: (value: unknown) => [typeof value === "function" ? value() : value, () => {}], useMemo: (value: () => unknown) => value() },
    "next/link": { default: "a" },
    "next/navigation": { useSearchParams: () => new URLSearchParams(query), useRouter: () => ({replace:(path:string)=>replacements.push(path)}) },
    "@tanstack/react-query": { useQueryClient: () => ({}) },
    "@/lib/domain": domain, "@/lib/labels": labels, "@/lib/scheduler": scheduler,
    "@/lib/live-display":liveDisplay,
    "@/lib/use-live":{useLive:()=>responses.live??{state:{snapshot:null}}},
    "@/lib/client": { useSession: () => ({config:{inferTimeoutMs:129000,maxRequestBytes:8388608,pageSize:25}}), useChillQuery: (path: string) => { paths.push(path); return responses[path] ?? { data: path === "v1/workloads?view=summary" ? [idle] : path.endsWith("/idle") ? idle : undefined, error: null } }, useChillMutation: () => ({ isPending: false, mutateAsync: async (request:unknown) => { writes.push(request) } }) },
    "@/components/status-cell": { StatusCell: "status-cell" },
    "@/components/data-table": { DataTable: "data-table" },
    "@/components/screen": { Screen: "main", Rows: "rows", RowDialog: "row-dialog", Facts: "facts" },
    "@/components/read-region": { QueryGate: "query-gate" },
    "@/components/ui/button": { Button: "button", buttonVariants: () => "" },
    "lucide-react": { ImagePlus: "image-plus" },
    "@/components/stage-breakdown": { duration: (value: number) => `${value} ms` },
  }
  const hub = load("../features/service-hub.tsx", modules)
  modules["@/features/service-hub"] = hub
  modules["@/hooks/use-catalog-resource"]=load("../hooks/use-catalog-resource.ts",modules)
  modules["@/features/catalog-editor"]=load("../features/catalog-editor.tsx",modules)
  const services=load("../features/services.tsx", modules)
  modules["@/features/services"]=services
  return { hub, services, inference:load("../features/inference.tsx",modules), paths, replacements, writes }
}

test("idle admission remains permission, not actual traffic, in service list and detail", () => {
  const h = harness()
  const admission = h.hub.ServiceAdmission({ view: idle })
  assert.equal(admission.props.axis, "접수 허용")
  assert.equal(admission.props.label, "허용")
  assert.equal(h.hub.ServiceAdmission({ view: { ...idle, admission: false } }).props.label, "차단")
  const list=h.services.ServicesPage({})
  const table=elements(list).find(element=>element.type==="data-table")!
  const columns=table.props.columns as {key:string;value:(view:WorkloadView)=>Element}[]
  const listAdmission=columns.find(column=>column.key==="admission")!.value(idle)
  const detail=elements(h.services.ServicePage({name:"idle"})).find(element=>element.type==="facts")!
  const detailAdmission=(detail.props.items as [string,Element][]).find(([label])=>label==="접수 허용")![1]
  for(const cell of [listAdmission,detailAdmission]){
    assert.equal(cell.type,h.hub.ServiceAdmission)
    assert.equal(h.hub.ServiceAdmission(cell.props).props.label,"허용")
  }
  assert.ok(h.paths.includes("v1/workloads?view=summary"))
  assert.ok(h.paths.includes("v1/workloads/idle"))
})

test("integrated detail retains three regions and UID-specific current observation", () => {
  const h = harness(), tree = h.services.ServicePage({ name: "idle" })
  assert.equal(tree.props.title, "idle")
  assert.equal(tree.props.lede, undefined)
  assert.equal(elements(tree).filter(element => element.props.id === "application").length, 1)
  assert.equal(elements(tree).filter(element => element.type === h.hub.ServiceDefinition).length, 1)
  const placement = h.hub.ServicePlacement({ view: idle, signals: { pools: {}, scheduler: {}, instances: [] } })
  assert.ok(elements(placement).some(element => element.props.href === "/?open=service%3Aservice-uid"))
  assert.ok(elements(tree.props.action).some(element => element.props.href === "/workloads/idle/infer?uid=service-uid"))
})

test("service detail links and back retain the URL-backed list investigation",()=>{
  const query="filter=allowed&search=idle&page=2&limit=10&sort=service&order=desc"
  const h=harness(query),list=h.services.ServicesPage({})
  const table=elements(list).find(element=>element.type==="data-table")!
  const column=(table.props.columns as {key:string;value:(view:WorkloadView)=>Element}[]).find(column=>column.key==="service")!
  const link=column.value(idle),url=new URL(link.props.href as string,"https://dashboard.invalid")
  assert.equal(url.searchParams.get("returnTo"),`/workloads?${query}`)
  const detail=harness(url.searchParams.toString()).services.ServicePage({name:"idle"})
  assert.deepEqual(detail.props.back,[`/workloads?${query}`,"서비스"])
  assert.equal(elements(detail).filter(element=>element.type==="data-table").length,0)
  for(const unsafe of ["//outside.invalid","https://outside.invalid","/workloads/../settings","/\\outside.invalid","/workloads%0a"]){
    assert.deepEqual(harness(new URLSearchParams({returnTo:unsafe}).toString()).services.ServicePage({name:"idle"}).props.back,["/workloads","서비스"])
  }
})

test("service definition exposes applied state and measured paths before raw evidence",()=>{
  const h=harness(),tree=h.hub.ServiceDefinition({view:idle})
  const dialog=elements(tree).find(element=>element.type==="row-dialog")!
  const summary=elements(dialog).find(element=>element.type===h.hub.ServiceAppliedFacts)!
  assert.ok(summary)
  const facts=h.hub.ServiceAppliedFacts({view:idle})
  assert.deepEqual((facts.props.items as [string,string][]).slice(0,1),[["적용 상태","미적용"]])
})

test("inference waiting controls separate target, wait limit and request size into facts",()=>{
  const h=harness()
  const page=h.inference.InferencePage({name:"idle"}),flow=page.type as Component
  const view={...idle,supportedInputs:[{executionPath:"cpu",artifact:"onnx",profiles:[]}],appliedDefinition:{
    preprocessor:{name:"input",input:[{name:"image",format:"JPEG",maxBytes:1024}],output:[]},
    slo:{percentile:90,targetLatencyNanos:1500000000},
  }}
  const tree=flow({...page.props,view}),dialog=elements(tree).find(element=>element.props.title==="응답 대기 시간")!
  const facts=elements(dialog).find(element=>element.type==="facts")!
  const values=new Map(facts.props.items as [string,unknown][])
  assert.equal(values.get("응답 목표 비율 (%)"),90)
  assert.equal(values.get("요청 크기 한도"),"8 MiB")
  assert.equal(values.get("대기 제한 · 응답 목표"),"별도")
  assert.ok(elements(tree).some(element=>element.type===h.hub.ServiceAppliedFacts))
})

test("distribution rows and weights come from one scheduler snapshot despite a later pools observation", () => {
  const h = harness()
  const snapshotAt = "2026-10-31T23:59:00Z"
  const instance = { id: "instance", workloadUID: "service-uid", node: { name: "scheduler-device" }, ready: true }
  const shared = { id: "shared", workloadUID: "another-service", node: { name: "shared-device" }, ready: true }
  const unrelated = { id: "unrelated", workloadUID: "another-service", node: { name: "other-device" }, ready: true }
  const unweighted = { id: "unweighted", workloadUID: "service-uid", node: { name: "unweighted-device" }, ready: false }
  const poolInstance = { id: "pool-only", node: { name: "pools-device" }, ready: true }
  const placement = h.hub.ServicePlacement({ view: idle, signals: {
    pools: { data: { observedAt: idle.observedAt } },
    scheduler: { data: { state: { snapshot: { at: snapshotAt, instances: [instance, shared, unrelated, unweighted], routing: { "service-uid": { instance: 2, shared: 0 } } } } } },
    instances: [poolInstance],
  } })
  const table = elements(placement).find(element => element.type === "data-table")!
  assert.deepEqual(table.props.data, [instance, shared, unweighted])
  const columns = table.props.columns as {key: string; value: (row: unknown) => unknown}[]
  const weight = columns.find(column => column.key === "weight")!
  assert.equal(weight.value(instance), 2)
  assert.equal(weight.value(shared), 0)
  assert.equal(weight.value(unweighted), "미제공")
  const poolTable = elements(placement).filter(element => element.type === "data-table")[1]
  assert.deepEqual(poolTable.props.data, [poolInstance])
  const facts = elements(placement).filter(element => element.type === "facts").map(element => element.props.items)
  assert.deepEqual(facts, [
    [["분배 기준 시각 (KST)", new Date(snapshotAt).toLocaleString("ko-KR", { timeZone: "Asia/Seoul" })]],
    [["배치 관측 시각 (KST)", new Date(idle.observedAt).toLocaleString("ko-KR", { timeZone: "Asia/Seoul" })]],
  ])
})

test("service readiness preserves distinct reasons without repeating the same preparation label",()=>{
  const h=harness()
  for(const reason of ["Preparing","NativeNotReady",""]){
    const tree=h.hub.ServiceReadiness({view:{...idle,readiness:"Preparing",reason}})
    assert.equal(elements(tree).filter(node=>node.type==="span").length,0)
  }
  const tree=h.hub.ServiceReadiness({view:{...idle,readiness:"Preparing",reason:"MissingEvidence"}})
  assert.ok(elements(tree).some(node=>node.props.children===labels.statusLabel("MissingEvidence")))
  const table=elements(h.services.ServicesPage({})).find(node=>node.type==="data-table")!
  const ready=(table.props.columns as {key:string;value:(view:WorkloadView)=>Element}[]).find(column=>column.key==="ready")!
  assert.equal(ready.value(idle).type,h.hub.ServiceReadiness)
})

test("disabled unplaced preparation uses the same status in the service list and detail",()=>{
  const h=harness("",{"v1/scheduler/settings":{data:{autonomousWorkloads:[]}},"v1/pools":{data:{workloads:[{workload:{uid:"service-uid"},instances:[]}],issues:[]}}})
  const view={...idle,readiness:"Preparing",reason:"Preparing"}
  const table=elements(h.services.ServicesPage({})).find(element=>element.type==="data-table")!
  const column=(table.props.columns as {key:string;value:(view:WorkloadView)=>Element}[]).find(row=>row.key==="ready")!
  const listCell=column.value(view)
  assert.equal(h.hub.ServiceReadiness(listCell.props).props.label,"미배치 · CHILL 자동 운영 꺼짐")
  assert.equal(h.hub.ServiceReadiness({view}).props.label,"미배치 · CHILL 자동 운영 꺼짐")
})

test("management readiness never substitutes stale live control or placement for REST facts",()=>{
  const workload={uid:"service-uid",autonomous:false,readiness:{ready:false,reason:"Preparing"}}
  const view={...idle,readiness:"Preparing",reason:"Preparing"}
  const live={status:"disconnected",state:{status:"disconnected",lastReceivedAt:1,snapshot:{workloads:[workload],instances:[]}}}
  for(const rest of [
    {"v1/scheduler/settings":{data:{autonomousWorkloads:["service-uid"]}},"v1/pools":{data:{workloads:[{workload:{uid:"service-uid"},instances:[]}],issues:[]}}},
    {"v1/scheduler/settings":{error:{status:503}},"v1/pools":{data:{workloads:[{workload:{uid:"service-uid"},instances:[]}],issues:[]}}},
    {"v1/scheduler/settings":{data:{autonomousWorkloads:[]}},"v1/pools":{data:{workloads:[{workload:{uid:"service-uid"},instances:null}],issues:[]}}},
  ]) {
    const h=harness("",{...rest,live})
    assert.notEqual(h.hub.ServiceReadiness({view}).props.label,"미배치 · CHILL 자동 운영 꺼짐")
  }
})

test("service empty reset clears search, state and page while preserving sort and page size",()=>{
  const h=harness("filter=blocked&search=missing&page=4&limit=10&sort=service&order=desc")
  const table=elements(h.services.ServicesPage({})).find(node=>node.type==="data-table")!
  const button=table.props.emptyAction as Element
  ;(button.props.onClick as ()=>void)()
  const params=new URL(h.replacements[0],"https://dashboard.invalid").searchParams
  for(const key of ["filter","search","page"])assert.equal(params.has(key),false)
  assert.equal(params.get("sort"),"service")
  assert.equal(params.get("limit"),"10")
})

test("autonomy distinguishes failed query, pending query, absent decision and disabled control",()=>{
  for(const [response,settings,label] of [
    [{error:{message:"offline"}},{autonomousWorkloads:["service-uid"]},"운영 판단 조회 실패"],
    [{isPending:true},{autonomousWorkloads:["service-uid"]},"운영 판단 조회 중"],
    [{data:{state:{decision:{}}}},{autonomousWorkloads:["service-uid"]},"이 서비스의 판단 기록 없음"],
    [{data:{state:{decision:{}}}},{autonomousWorkloads:[]},"CHILL 자동 운영 꺼짐"],
  ] as const){
    const h=harness("",{"v1/scheduler":response,"v1/scheduler/settings":{data:settings}})
    const facts=elements(h.services.ServicePage({name:"idle"})).find(node=>node.type==="facts")!
    const rows=(facts.props.items as [string,Element][]).find(([key])=>key==="CHILL 자동 운영")![1]
    const autonomy=elements(rows).find(node=>typeof node.type==="function")!
    const tree=(autonomy.type as Component)(autonomy.props)
    const state=elements(tree).find(node=>node.type==="facts")!
    assert.equal((state.props.items as [string,string][])[0][1],label)
  }
})

test("service autonomy navigates to central settings without a switch or write",()=>{
  const uid="service /?&한글",view={...idle,desired:{...idle.desired,metadata:{...idle.desired.metadata,uid}}}
  const h=harness("",{"v1/workloads?view=summary":{data:[view]},"v1/workloads/idle":{data:view},"v1/scheduler/settings":{data:{autonomousWorkloads:[uid]}}})
  const table=elements(h.services.ServicesPage({})).find(node=>node.type==="data-table")!
  const columns=table.props.columns as {key:string;label:string;value:(view:WorkloadView)=>Element}[]
  const column=columns.find(row=>row.key==="autonomy")!
  assert.equal(column.label,"CHILL 자동 운영")
  const detail=elements(h.services.ServicePage({name:"idle"})).find(node=>node.type==="facts")!
  const rows=(detail.props.items as [string,Element][]).find(([key])=>key==="CHILL 자동 운영")![1]
  const autonomy=elements(rows).find(node=>typeof node.type==="function")!
  const tree=(autonomy.type as Component)(autonomy.props)
  assert.equal(tree.props.title,"CHILL 자동 운영")
  for(const link of [column.value(view),elements(tree).find(node=>node.type==="a")!]){
    const url=new URL(link.props.href as string,"https://dashboard.invalid")
    assert.equal(url.pathname,"/settings")
    assert.equal(url.searchParams.get("section"),"targets")
    assert.equal(url.searchParams.get("edit"),"autonomousWorkloads")
    assert.equal(url.searchParams.get("workload"),uid)
  }
  assert.equal(elements(tree).some(node=>node.props.role==="switch"||node.type==="input"||typeof node.props.onChange==="function"),false)
  assert.deepEqual(h.writes,[])
})

test("operation column appears only for ongoing changes and absent operations are muted",()=>{
  for(const operation of [undefined,{kind:"Update",phase:"Completed"},{kind:"Update",phase:"Failed"}]){
    const view={...idle,operation} as WorkloadView
    const h=harness("",{"v1/workloads?view=summary":{data:[view]}})
    const table=elements(h.services.ServicesPage({})).find(node=>node.type==="data-table")!
    assert.equal((table.props.columns as {key:string}[]).some(column=>column.key==="operation"),false)
  }
  const changing={...idle,operation:{kind:"Update",phase:"Pending"}} as WorkloadView
  const h=harness("",{"v1/workloads?view=summary":{data:[idle,changing]}})
  const table=elements(h.services.ServicesPage({})).find(node=>node.type==="data-table")!
  const operation=(table.props.columns as {key:string;label:string;value:(view:WorkloadView)=>Element|string}[]).find(column=>column.key==="operation")!
  assert.equal(operation.label,"변경 작업 진행")
  assert.equal((operation.value(idle) as Element).props.className,"text-muted-foreground")
  assert.equal(typeof operation.value(changing),"string")
  const detail=elements(h.services.ServicePage({name:"idle"})).find(node=>node.type==="facts")!
  const absent=(detail.props.items as [string,Element][]).find(([key])=>key==="변경 작업 진행")![1]
  assert.equal(absent.props.className,"text-muted-foreground")
})

test("JPEG input leads with a user label and retains its original contract key",()=>{
  const h=harness(),page=h.inference.InferencePage({name:"idle"})
  const tree=(page.type as Component)({...page.props,view:{...idle,supportedInputs:[{}],appliedDefinition:{preprocessor:{input:[{name:"image",format:"JPEG",maxBytes:1024}]},slo:{percentile:95,targetLatencyNanos:1000}}}})
  assert.ok(elements(tree).some(node=>node.props["aria-label"]==="사진 고르기"))
  assert.ok(elements(tree).some(node=>node.props.children==="계약 키 · image"))
})
