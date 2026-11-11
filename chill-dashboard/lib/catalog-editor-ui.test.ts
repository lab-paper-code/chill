import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as domain from "./domain.ts"
import * as measurementSummary from "./measurement-summary.ts"
import * as labels from "./labels.ts"

type Tree={type:unknown;props:Record<string,unknown>}
const require=createRequire(import.meta.url)
function hooks() {
  let values:unknown[]=[];let cursor=0
  const states=new Map<unknown,unknown[]>()
  return {react:{useState(initial:unknown){const index=cursor++;if(!Object.hasOwn(values,index))values[index]=typeof initial==="function"?initial():initial;const state=values;return [state[index],(next:unknown)=>{state[index]=typeof next==="function"?next(state[index]):next}]}},render(Component:(props:Record<string,unknown>)=>unknown,props:Record<string,unknown>={}){cursor=0;if(!states.has(Component))states.set(Component,[]);values=states.get(Component)!;return Component(props)}}
}
function load(path:string,modules:Record<string,unknown>):Record<string,(props:Record<string,unknown>)=>unknown> {
  const compiled=transpileModule(readFileSync(new URL(path,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  new Function("require","exports",compiled)((name:string)=>Object.hasOwn(modules,name)?modules[name]:name==="@/lib/client-transport"?modules[name]=load("./client-transport.ts",{"@/lib/measurement-summary":measurementSummary}):name==="@/hooks/use-catalog-resource"?load("../hooks/use-catalog-resource.ts",modules):name.startsWith("@/")?{}:require(name),exports)
  return exports
}
const base={"next/navigation":{useSearchParams:()=>new URLSearchParams()},"@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{}})},"@/lib/domain":domain}

test("catalog update retains If-Match and original definition and never auto-retries",async()=>{
  for(const kind of ["models","preprocessors","datasets"]){
    const runner=hooks(),sent:unknown[]=[],current={metadata:{name:"registered",uid:"original",resourceVersion:"7"},spec:{future:"original"}}
    const {ResourceEditor}=load("../features/catalog-editor.tsx",{...base,react:runner.react,"@/lib/client":{useChillMutation:()=>({isPending:false,mutateAsync:async(input:unknown)=>{sent.push(input);return {status:200,body:"{}",data:current}}}),useChillQuery:(path:string)=>({data:path===`v1/${kind}/registered`?current:path==="v1/workloads?view=summary"?[]:{items:[]},refetch:async()=>({data:current})})}})
    runner.render(ResourceEditor,{kind,name:"registered"})
    const editor=runner.render(ResourceEditor,{kind,name:"registered"}) as Tree
    const state=editor.props.r as {source:unknown;submit:(method:string,body:unknown)=>Promise<void>}
    assert.equal(state.source,current)
    await state.submit("PUT",{spec:{future:"edited"}})
    assert.deepEqual(sent,[{path:`v1/${kind}/registered`,method:"PUT",body:{spec:{future:"edited"}},version:"7"}])
  }
})

function workloadConsumer(editing = true) {
  const runner = hooks()
  const original = { metadata: { name: "registered", uid: "original", resourceVersion: "7" }, spec: { modelRef: { name: "model" }, preprocessorRef: { name: "input" }, slo: { percentile: 99.5, targetLatency: "+1s" }, admission: { policy: "Delay", maxQueuedRequests: 37 }, requestLifetime: "+1m2.000000001s" } }
  const current = { desired: original, supportedInputs: [] }
  const sent: { method: string; path: string; body: { name?: string; spec: domain.WorkloadSpec }; version?: string }[] = []
  const mutation = { isPending: false, error: null as { status: number; data: { code: string; field: string; message: string } } | null, mutateAsync: async (input: typeof sent[number]) => {
    sent.push(input)
    if (mutation.error) throw mutation.error
    return { status: editing ? 200 : 201, body: "{}", data: current }
  } }
  const modules = {
    ...base, react: runner.react, "@/lib/labels": labels,
    "@/components/ui/button": { Button: "button" }, "@/components/ui/input": { Input: "input" },
    "@/components/screen": { Screen: "main", Facts: "facts", Rows: "rows", Row: "row", RowDialog: "row-dialog" },
    "@/components/read-region": { QueryGate: "query-gate", StatusBadge: "span", SourceJSON: "pre" },
    "@/components/data-table": { DataTable: "table" },
    "@/lib/client": { useChillMutation: () => mutation, useChillQuery: (path: string) => ({ data:
      path === "v1/workloads/registered" ? current :
      path === "v1/models" ? { items: [{ metadata: { name: "model", uid: "model-uid" }, spec: { artifacts: [{ name: "onnx", format: "onnx", source: { image: "model:v1", architecture: "amd64", files: ["/model.onnx"] } }], executionPaths: [{ name: "cpu", artifact: "onnx", runtime: { family: "firstfloor", backend: "ort-cpu" } }] } }] } :
      path === "v1/preprocessors" ? { items: [{ metadata: { name: "input", uid: "input-uid" }, spec: { input: { fields: [] }, output: { fields: [] }, artifacts: [{ target: { model: "model", artifact: "onnx" }, source: { image: "input:v1", architecture: "amd64", path: "/input.py" } }] } }] } :
      path.includes("workloads") ? [] : { items: [] }, refetch: async () => ({ data: current }) }) },
  }
  const { ResourceEditor } = load("../features/catalog-editor.tsx", modules)
  const props = { kind: "workloads", ...(editing ? { name: "registered" } : {}) }
  if (editing) runner.render(ResourceEditor, props)
  let nodes: Tree[] = []
  function expand(value: unknown): unknown {
    if (Array.isArray(value)) return value.map(expand)
    if (!value || typeof value !== "object" || !("props" in value)) return value
    const node = value as Tree
    if (typeof node.type === "function") return expand(runner.render(node.type as (props: Record<string, unknown>) => unknown, node.props))
    const expanded = { ...node, props: { ...node.props, children: expand(node.props.children) } }
    nodes.push(expanded)
    return expanded
  }
  function view() { nodes = []; return expand(runner.render(ResourceEditor, props)) }
  function field(label: string) {
    const node = nodes.find(node => node.props["aria-label"] === label)
    assert.ok(node, `Missing field ${label}`)
    return node
  }
  function change(label: string, value: string) { (field(label).props.onChange as (event: unknown) => void)({ target: { value, valueAsNumber: value === "" ? NaN : Number(value) } }); view() }
  function next() { (nodes.find(node => node.type === "form")!.props.onSubmit as (event: unknown) => void)({ preventDefault() {} }); view() }
  function toSLO() {
    view()
    if (!editing) { change("서비스 이름", "new-service"); next() }
    if (!editing) { const model = nodes.find(node => node.type === "input" && node.props.value === "model")!; (model.props.onChange as () => void)(); view() }
    next()
    if (!editing) { const pre = nodes.find(node => node.type === "input" && node.props.value === "input")!; (pre.props.onChange as () => void)(); view() }
    next(); next()
  }
  return { original, sent, mutation, view, field, change, next, toSLO, nodes: () => nodes }
}

test("service editing round-trips frozen draft admission and lifetime through the actual PUT", () => {
  const h = workloadConsumer()
  h.toSLO()
  assert.equal(h.field("과부하 정책").props.value, "Delay")
  assert.equal(h.field("대기 상한").props.value, 37)
  assert.equal(h.field("요청 최종 기한").props.value, "+1m2.000000001s")
  h.next()
  const review = h.nodes().find(node => node.type === "facts")!.props.items as [string, unknown][]
  assert.equal(new Map(review).get("대기 상한"), "37건")
  h.next()
  assert.deepEqual(h.sent, [{ path: "v1/workloads/registered", method: "PUT", version: "7", body: { spec: h.original.spec } }])
})

test("service registration sends explicit admission defaults and omits empty final deadline", () => {
  const h = workloadConsumer(false)
  h.toSLO()
  assert.equal(h.field("과부하 정책").props.value, "Reject")
  assert.equal(h.field("대기 상한").props.value, 1024)
  assert.equal(h.field("요청 최종 기한").props.value, "")
  assert.deepEqual(h.nodes().filter(node => node.type === "option" && ["Reject", "Delay"].includes(String(node.props.value))).map(node => node.props.children), Object.values(domain.admissionPolicies))
  assert.equal(h.field("대기 상한").props.min, 1)
  assert.equal(h.field("대기 상한").props.step, 1)
  assert.equal(h.field("요청 최종 기한").props.required, undefined)
  h.next(); h.next()
  assert.equal(h.sent[0].method, "POST")
  assert.equal(h.sent[0].body.name, "new-service")
  assert.deepEqual(h.sent[0].body.spec.admission, { policy: "Reject", maxQueuedRequests: 1024 })
  assert.equal(Object.hasOwn(h.sent[0].body.spec, "requestLifetime"), false)
})

test("invalid service admission inputs never reach review or a mutation", () => {
  for (const [label, value, error] of [
    ["과부하 정책", "Keep", "과부하 정책"], ["대기 상한", "0", "대기 상한"],
    ["대기 상한", "1.5", "대기 상한"], ["대기 상한", "", "대기 상한"],
    ["요청 최종 기한", "1day", "요청 최종 기한"],
  ]) {
    const h = workloadConsumer()
    h.toSLO(); h.change(label, value); h.next()
    assert.ok(h.nodes().some(node => node.props.role === "alert" && String(node.props.children).includes(error)))
    assert.ok(h.field(label))
    assert.deepEqual(h.sent, [])
  }
})

test("service lifetime bounds stay server-owned and rejection text stays visible", async () => {
  const h = workloadConsumer()
  h.toSLO(); h.change("요청 최종 기한", "130s"); h.next()
  const message = "requestLifetime must not exceed 129s"
  h.mutation.error = { status: 422, data: { code: "InvalidWorkload", field: "spec.requestLifetime", message } }
  h.next()
  await Promise.resolve()
  h.view()
  assert.equal(h.sent.length, 1)
  assert.equal(h.sent[0].body.spec.requestLifetime, "130s")
  assert.ok(h.nodes().some(node => node.type === "p" && node.props.children === message))
})

