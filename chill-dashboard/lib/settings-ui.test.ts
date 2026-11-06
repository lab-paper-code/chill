import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as domain from "./domain.ts"
import * as scheduler from "./scheduler.ts"
import * as sourceExport from "./source-export.ts"
import * as labels from "./labels.ts"
import * as capacity from "./capacity.ts"

type Tree={type:unknown;props:Record<string,unknown>}
const require=createRequire(import.meta.url)
function elements(node:unknown):Tree[] {
  if(Array.isArray(node))return node.flatMap(elements)
  if(!node||typeof node!=="object"||!("props" in node))return []
  const element=node as Tree
  return [element,...elements(element.props.children)]
}
function hooks() {
  let values:unknown[]=[];let cursor=0
  const states=new Map<unknown,unknown[]>()
  return {react:{useState(initial:unknown){const index=cursor++;if(!Object.hasOwn(values,index))values[index]=typeof initial==="function"?initial():initial;const state=values;return [state[index],(next:unknown)=>{state[index]=typeof next==="function"?next(state[index]):next}]},useRef:()=>({current:null}),useEffect:()=>{},useId:()=>"field"},render(Component:(props:Record<string,unknown>)=>unknown,props:Record<string,unknown>={}){cursor=0;if(!states.has(Component))states.set(Component,[]);values=states.get(Component)!;return Component(props)}}
}
function load(path:string,modules:Record<string,unknown>):Record<string,(props:Record<string,unknown>)=>unknown> {
  const compiled=transpileModule(readFileSync(new URL(path,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  new Function("require","exports",compiled)((name:string)=>Object.hasOwn(modules,name)?modules[name]:name.startsWith("@/")?{}:require(name),exports)
  return exports
}
const base={"next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({push:()=>{},replace:()=>{}})},"@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{}})},"@/lib/domain":domain,"@/lib/scheduler":scheduler,"@/lib/source-export":sourceExport,"@/components/screen":{Screen:"main",Facts:"facts",Rows:"rows",Row:"row",RowDialog:"row-dialog"},"@/components/data-table":{DataTable:"table"},"@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},"@/components/ui/sheet":{Sheet:"sheet",SheetContent:"aside",SheetHeader:"header",SheetTitle:"h2",SheetTrigger:"trigger"},"@/components/read-region":{QueryGate:"query",StatusBadge:"status"},"@/components/settings-navigation":{SettingsNavigation:"tabs"},"@/features/service-hub":{ServiceAdmission:"admission"}}

test("settings tabs open actual screens, with operating settings as default",()=>{
  let params=new URLSearchParams()
  const {SettingsPage}=load("../features/settings.tsx",{"next/navigation":{useSearchParams:()=>params},"@/features/management":{CatalogSettingsPage:"catalog"},"@/features/scheduler-settings":{SchedulerSettingsPage:"operating"}})
  assert.equal((SettingsPage({}) as Tree).type,"operating")
  for(const tab of ["models","preprocessors","datasets"]){params=new URLSearchParams({tab});const tree=SettingsPage({}) as Tree;assert.equal(tree.type,"catalog");assert.equal(tree.props.kind,tab)}
})

test("catalog selection pins the exact UID in a Sheet and exposes dedicated edit actions",()=>{
  for(const kind of ["models","preprocessors","datasets"]){
    const runner=hooks(),item={metadata:{name:"registered",uid:"original"},spec:kind==="models"?{artifacts:[],executionPaths:[]}:kind==="datasets"?{source:{image:"registry@sha256:1",path:"/data"},fields:[]}:{input:{fields:[]},output:{fields:[]},artifacts:[]}}
    let params=new URLSearchParams({tab:kind,namespace:"selected"})
    const {CatalogSettingsPage}=load("../features/management.tsx",{...base,react:runner.react,"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams}})},"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/workloads"?[]:{items:[item]}})}})
    let tree=elements(runner.render(CatalogSettingsPage,{kind}))
    const table=tree.find(element=>element.type==="table")!
    const columns=table.props.columns as {key:string;value:(item:unknown)=>Tree}[]
    const edit=columns.find(column=>column.key==="actions")!.value(item)
    assert.equal(edit.props.href,domain.resourceHref(kind,item.metadata.name,item.metadata.uid,"edit"))
    params=new URL(String(columns.find(column=>column.key==="name")!.value(item).props.href),"https://dashboard.test").searchParams
    assert.equal(params.get("uid"),"original")
    assert.equal(params.get("namespace"),"selected")
    tree=elements(runner.render(CatalogSettingsPage,{kind}))
    assert.equal(tree.find(element=>element.type==="sheet")?.props.open,true)
    assert.ok(tree.some(element=>element.props.name==="registered"&&element.props.expectedUID==="original"&&element.props.embedded===true))
    ;(tree.find(element=>element.type==="sheet")!.props.onOpenChange as (open:boolean)=>void)(false)
    assert.equal(elements(runner.render(CatalogSettingsPage,{kind})).find(element=>element.type==="sheet")?.props.open,false)
    assert.equal(params.get("namespace"),"selected")
  }
})

test("catalog Sheet refuses a same-name replacement instead of exposing its delete actions",()=>{
  const runner=hooks(),current={metadata:{name:"registered",uid:"replacement",resourceVersion:"2"},spec:{artifacts:[],executionPaths:[]}}
  const {ModelPage}=load("../features/management.tsx",{...base,react:runner.react,"@/lib/client":{useChillMutation:()=>({isPending:false}),useChillQuery:(path:string)=>({data:path==="v1/models/registered"?current:path==="v1/workloads"?[]:{items:[]}})}})
  const tree=runner.render(ModelPage,{name:"registered",expectedUID:"original",embedded:true}) as Tree
  assert.equal(tree.props.uid,"replacement")
  assert.equal(tree.props.kind,"models")
  assert.ok(!elements(tree).some(element=>element.props.label==="삭제"))
})

test("catalog update retains If-Match and original definition and never auto-retries",async()=>{
  for(const kind of ["models","preprocessors","datasets"]){
    const runner=hooks(),sent:unknown[]=[],current={metadata:{name:"registered",uid:"original",resourceVersion:"7"},spec:{future:"original"}}
    const {ResourceEditor}=load("../features/management.tsx",{...base,react:runner.react,"@/lib/client":{useChillMutation:()=>({isPending:false,mutateAsync:async(input:unknown)=>{sent.push(input);return {status:200,body:"{}",data:current}}}),useChillQuery:(path:string)=>({data:path===`v1/${kind}/registered`?current:path==="v1/workloads"?[]:{items:[]},refetch:async()=>({data:current})})}})
    runner.render(ResourceEditor,{kind,name:"registered"})
    const editor=runner.render(ResourceEditor,{kind,name:"registered"}) as Tree
    const state=editor.props.r as {source:unknown;submit:(method:string,body:unknown)=>Promise<void>}
    assert.equal(state.source,current)
    await state.submit("PUT",{spec:{future:"edited"}})
    assert.deepEqual(sent,[{path:`v1/${kind}/registered`,method:"PUT",body:{spec:{future:"edited"}},version:"7"}])
  }
})

test("operating setting writes retain exact integers and omit runtime-only fields",async()=>{
  const {putSchedulerSettings}=load("../features/scheduler-settings.tsx",{...base,"@/lib/client":{}})
  const sent:{path:string;method:string;body:string}[]=[]
  const settings={observationWindowNanos:9007199254740993n,controls:{placement:true},staticRouting:{service:{instance:1}},staticRoutingValidatedAt:"past",meters:{secret:"runtime"},requestTimeout:10}
  const writer=putSchedulerSettings as unknown as (mutate:(input:{path:string;method:string;body:string})=>Promise<{body:string}>,client:{invalidateQueries:()=>Promise<void>},settings:unknown)=>Promise<unknown>
  await writer(async input=>{sent.push(input);return {body:'{"appliedAt":"2026-11-01T00:00:00Z"}'}},{invalidateQueries:async()=>{}},settings)
  assert.equal(sent.length,1)
  assert.equal(sent[0].path,"v1/scheduler/settings")
  const body=scheduler.parseSchedulerJSON(sent[0].body) as Record<string,unknown>
  assert.equal(body.observationWindowNanos,9007199254740993n)
  for(const key of ["staticRouting","staticRoutingValidatedAt","meters","requestTimeout"])assert.ok(!Object.hasOwn(body,key))
  assert.ok(Object.hasOwn(settings,"staticRouting"),"the source draft is not mutated")
})

test("operating range table edits accumulate a precise draft before review",()=>{
  const runner=hooks(),settings={controls:{placement:true},observationWindowNanos:1000000000n,controlIntervalNanos:1000000000n,comparisonPeriodNanos:1000000000n,replacementDwellNanos:0n,scaleDownStabilizationNanos:0n,transitionReserveFraction:0,scaleDownHeadroomFraction:0,maxObservationAgeNanos:1000000000n}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({isPending:false}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/scheduler/state"?{results:[]}:undefined})}})
  let tree=elements(runner.render(SchedulerSettingsPage))
  assert.equal(tree.find(element=>element.type==="tabs")?.props.active,"configuration")
  const table=tree.find(element=>element.type==="table")!
  const field=(table.props.data as {key:string}[]).find(field=>field.key==="observationWindowNanos")!
  const edit=(table.props.columns as {key:string;value:(field:unknown)=>Tree}[]).find(column=>column.key==="edit")!.value(field)
  ;(edit.props.save as (value:string)=>void)("9007199254.740993")
  tree=elements(runner.render(SchedulerSettingsPage))
  const updated=tree.find(element=>element.type==="table")!
  const value=(updated.props.columns as {key:string;value:(field:unknown)=>unknown}[]).find(column=>column.key==="value")!.value(field)
  assert.equal(value,"9007199254.740993ms")
  const action=(tree.find(element=>element.type==="main")!.props.action as Tree)
  assert.equal(action.props.children,"변경 1건 검토")
})

test("operating sections and numeric, boolean and node Sheets follow URL history and reload",()=>{
  let params=new URLSearchParams({tab:"configuration",section:"limit",namespace:"kept"})
  const history=[params.toString()]
  const navigation={useSearchParams:()=>params,useRouter:()=>({
    push:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams;history.push(params.toString())},
    replace:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams;history[history.length-1]=params.toString()},
  })}
  const settings={controls:{placement:true},maxReplicas:3,maxInstances:4,maxCPUs:2,servingMemoryBytes:1048576,capacityWindows:5,excludedNodes:["meter"]}
  const runner=hooks()
  const modules={...base,react:runner.react,"next/navigation":navigation,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({isPending:false}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/scheduler/state"?{results:[]}:undefined})}}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",modules)
  let tree=elements(runner.render(SchedulerSettingsPage))
  assert.equal(tree.find(node=>node.type==="select")!.props.value,"limit")
  ;(tree.find(node=>node.type==="select")!.props.onChange as (event:unknown)=>void)({target:{value:"capacity"}})
  assert.equal(params.get("section"),"capacity")
  assert.equal(params.get("namespace"),"kept")
  const fresh=hooks()
  const reloaded=load("../features/scheduler-settings.tsx",{...modules,react:fresh.react}).SchedulerSettingsPage
  assert.equal(elements(fresh.render(reloaded)).find(node=>node.type==="select")!.props.value,"capacity")
  params=new URLSearchParams(history[0])
  tree=elements(runner.render(SchedulerSettingsPage))
  assert.equal(tree.find(node=>node.type==="select")!.props.value,"limit")
  for(const [section,key] of [["limit","maxReplicas"],["controls","controls.placement"],["cycle","excludedNodes"]]){
    params.set("section",section)
    tree=elements(runner.render(SchedulerSettingsPage))
    const table=tree.find(node=>node.type==="table")!
    const columns=table.props.columns as {key:string;value:(row:unknown)=>Tree}[]
    const row=(table.props.data as {key:string}[]).find(row=>row.key===key.replace("controls.",""))
    const editor=key==="excludedNodes"?tree.find(node=>node.props.editKey===key)!:columns.find(column=>column.key==="edit")!.value(row)
    const component=editor.type as (props:Record<string,unknown>)=>unknown
    let sheet=elements(runner.render(component,editor.props)).find(node=>node.type==="sheet")!
    ;(sheet.props.onOpenChange as (open:boolean)=>void)(true)
    assert.equal(params.get("edit"),key)
    sheet=elements(runner.render(component,editor.props)).find(node=>node.type==="sheet")!
    assert.equal(sheet.props.open,true)
    const refreshed=hooks(),refreshedPage=load("../features/scheduler-settings.tsx",{...modules,react:refreshed.react}).SchedulerSettingsPage
    const refreshedTree=elements(refreshed.render(refreshedPage)),refreshedTable=refreshedTree.find(node=>node.type==="table")!
    const refreshedColumns=refreshedTable.props.columns as {key:string;value:(row:unknown)=>Tree}[]
    const refreshedRow=(refreshedTable.props.data as {key:string}[]).find(row=>row.key===key.replace("controls.",""))
    const refreshedEditor=key==="excludedNodes"?refreshedTree.find(node=>node.props.editKey===key)!:refreshedColumns.find(column=>column.key==="edit")!.value(refreshedRow)
    assert.equal(elements(refreshed.render(refreshedEditor.type as (props:Record<string,unknown>)=>unknown,refreshedEditor.props)).find(node=>node.type==="sheet")!.props.open,true)
    params=new URLSearchParams(history[history.length-2])
    assert.equal(elements(runner.render(component,editor.props)).find(node=>node.type==="sheet")!.props.open,false)
  }
})

test("catalog lists prioritize fields, models and numeric counts while retaining complete collapsed definitions",()=>{
  const secret="registry.example/very-long-address@sha256:"+"a".repeat(64)
  const specs={
    models:{artifacts:[{name:"internal-file-a",format:"ONNX"},{name:"internal-file-b",format:"ONNX"}],executionPaths:[{name:"internal-path-a",runtime:{backend:"cpu"}},{name:"internal-path-b",runtime:{backend:"cpu"}}]},
    preprocessors:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1048576}]},output:{fields:[{name:"tensor",format:"Tensor"}]},artifacts:[{target:{model:"model-a",artifact:"internal-file-a"},source:{image:secret}},{target:{model:"model-a",artifact:"internal-file-b"},source:{image:secret}}]},
    datasets:{fields:[{name:"image",format:"JPEG",maxBytes:1048576}],source:{image:secret,path:"/data"}},
  }
  for(const [kind,spec] of Object.entries(specs)){
    const runner=hooks(),item={metadata:{name:"catalog",uid:"catalog-uid"},spec}
    const {CatalogSettingsPage}=load("../features/management.tsx",{...base,"@/lib/labels":labels,react:runner.react,"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/workloads"?[]:{items:[item]}})}})
    const table=elements(runner.render(CatalogSettingsPage,{kind})).find(node=>node.type==="table")!
    const columns=table.props.columns as {key:string;numeric?:boolean;value:(item:unknown)=>unknown}[]
    const definition=columns.find(column=>column.key==="definition")!.value(item)
    const rendered=JSON.stringify(definition)
    assert.ok(!rendered.includes(secret))
    assert.ok(!rendered.includes("internal-file"))
    assert.ok(!rendered.includes("internal-path"))
    const count=columns.find(column=>column.key==="count")!
    assert.equal(count.numeric,true)
    assert.equal(count.value(item),kind==="models"?2:1)
    assert.equal(columns.find(column=>column.key==="services")!.value(item),0)
    const source=columns.find(column=>column.key==="source")!.value(item) as Tree
    assert.equal(source.type,"details")
    assert.ok(!source.props.open)
    assert.ok(elements(source).some(node=>node.props.value===spec))
  }
})

test("routing table edits preserve route, block and release semantics and reject invalid weights",()=>{
  const pageRunner=hooks(),initial={service:{instance:2},release:{old:1}}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:pageRunner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({isPending:false}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?{controls:{},staticRouting:initial}:path==="v1/scheduler/state"?{results:[]}:undefined})}})
  const editor=elements(pageRunner.render(SchedulerSettingsPage)).find(node=>typeof node.props.apply==="function"&&"initial" in node.props)!
  const runner=pageRunner,applied:unknown[]=[]
  const props={...editor.props,snapshot:{workloads:[{uid:"service",name:"Service A"},{uid:"release",name:"Service B"},{uid:"new",name:"Service C"}],instances:[{id:"instance",workloadUID:"service",node:{name:"Device"}},{id:"new-instance",workloadUID:"new",node:{name:"Other device"}}]},apply:(...args:unknown[])=>applied.push(args)}
  const component=editor.type as (props:Record<string,unknown>)=>unknown
  let tree=elements(runner.render(component,props))
  ;(tree.find(node=>node.type==="row-dialog")!.props.onOpenChange as (open:boolean)=>void)(true)
  tree=elements(runner.render(component,props))
  assert.ok(!tree.find(node=>node.type==="row-dialog")!.props.description)
  assert.ok(tree.some(node=>node.type==="details"&&!node.props.open&&elements(node).some(child=>child.type==="textarea")))
  const reason=tree.find(node=>node.type==="input"&&String(node.props.id).endsWith("-reason"))!
  ;(reason.props.onChange as (event:unknown)=>void)({target:{value:"manual choice"}})
  const changeWeight=(value:string)=>{
    const table=elements(runner.render(component,props)).find(node=>node.type==="table")!
    const column=(table.props.columns as {key:string;value:(row:unknown)=>Tree}[]).find(column=>column.key==="weight")!
    ;(column.value((table.props.data as unknown[])[0]).props.onChange as (event:unknown)=>void)({target:{value}})
  }
  const submit=()=>{
    const form=elements(runner.render(component,props)).find(node=>node.type==="form")!
    ;(form.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  }
  for(const invalid of ["0","-1","1.5","2147483648",""]){changeWeight(invalid);submit();assert.equal(applied.length,0);assert.ok(elements(runner.render(component,props)).some(node=>node.props.role==="alert"))}
  changeWeight("7")
  tree=elements(runner.render(component,props))
  const release=tree.filter(node=>node.type==="button"&&node.props.children==="기본 분배로")[1]
  ;(release.props.onClick as ()=>void)()
  submit()
  assert.deepEqual(applied,[[initial,{service:{instance:7}},"manual choice"]])
  tree=elements(runner.render(component,props))
  ;(tree.find(node=>node.type==="button"&&node.props.children==="새 요청 차단")!.props.onClick as ()=>void)()
  submit()
  assert.deepEqual(applied[1],[initial,{service:{}},"manual choice"])
  tree=elements(runner.render(component,props))
  ;(tree.find(node=>node.type==="button"&&node.props.children==="서비스 추가")!.props.onClick as ()=>void)()
  tree=elements(runner.render(component,props))
  ;(tree.find(node=>node.type==="button"&&node.props.children==="서비스 추가")!.props.onClick as ()=>void)()
  tree=elements(runner.render(component,props))
  ;(tree.filter(node=>node.type==="button"&&node.props.children==="인스턴스 추가").at(-1)!.props.onClick as ()=>void)()
  submit()
  assert.deepEqual(applied[2],[initial,{service:{},release:{},new:{"new-instance":1}},"manual choice"])
})

test("excluded node selection retains unknown names and saves selected known names without UIDs",()=>{
  const runner=hooks(),settings={controls:{},excludedNodes:["retired"]},saved:unknown[]=[]
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/scheduler/state"?{snapshot:{nodes:[{node:{name:"worker",uid:"hidden-uid"}}]}}:undefined})}})
  const editor=elements(runner.render(SchedulerSettingsPage)).find(node=>node.props.editKey==="excludedNodes")!
  const component=editor.type as (props:Record<string,unknown>)=>unknown,props={...editor.props,save:(value:unknown)=>saved.push(value)}
  let tree=elements(runner.render(component,props))
  assert.ok(!JSON.stringify(tree).includes("hidden-uid"))
  const choice=tree.find(node=>node.type==="input"&&node.props.type==="checkbox"&&node.props.value==="worker")!
  ;(choice.props.onChange as (event:unknown)=>void)({target:{checked:true}})
  tree=elements(runner.render(component,props))
  const unknown=tree.find(node=>node.type==="input"&&node.props["aria-label"]==="현재 목록에 없는 장비 1")!
  ;(unknown.props.onChange as (event:unknown)=>void)({target:{value:"renamed"}})
  tree=elements(runner.render(component,props))
  ;(tree.find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  assert.deepEqual(saved,[["renamed","worker"]])
})

test("routing lookup failure remains visible beside its action even with stale state",()=>{
  const runner=hooks(),error={status:502,data:{code:"UpstreamUnavailable",message:"분배 저장소 응답 없음"}}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>path==="v1/scheduler/settings"?{data:{controls:{}}}:path==="v1/scheduler/state"?{data:{results:[]},error}:{}}})
  const row=elements(runner.render(SchedulerSettingsPage)).find(node=>node.props.label==="설정한 요청 분배")!
  assert.match(JSON.stringify(row.props.value),/502/)
  assert.match(JSON.stringify(row.props.value),/UpstreamUnavailable/)
  assert.match(JSON.stringify(row.props.value),/분배 저장소 응답 없음/)
})

test("model details use human titles and independent execution limits while retaining raw source",()=>{
  const runner=hooks(),spec={artifacts:[{name:"internal-file",format:"onnx",source:{image:"registry@sha256:abc",architecture:"arm64",files:["/internal/model.onnx"]}}],executionPaths:[{name:"internal-path",artifact:"internal-file",runtime:{backend:"ort-cpu",settings:{firstfloor:{maxBatchSize:8,maxQueueSize:16,deadlinePolicy:"drop"}}}}]}
  const {ModelSectionPage}=load("../features/management.tsx",{...base,react:runner.react,"@/lib/labels":labels,"@/lib/client":{useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/models/model"?{metadata:{name:"model",uid:"uid"},spec}:path==="v1/workloads"?[]:{items:[]}})}})
  const tree=elements(runner.render(ModelSectionPage,{name:"model",section:"files"})),dialogs=tree.filter(node=>node.type==="row-dialog")
  assert.ok(dialogs.every(node=>!String(node.props.label).includes("internal-")))
  const rows=tree.filter(node=>node.type==="tr").map(row=>elements(row).filter(cell=>cell.type==="th"||cell.type==="td").map(cell=>cell.props.children))
  assert.deepEqual(rows.find(row=>row[0]==="최대 묶음 크기 (건)"),["최대 묶음 크기 (건)",8])
  assert.deepEqual(rows.find(row=>row[0]==="서비스별 대기 한도 (건)"),["서비스별 대기 한도 (건)",16])
  assert.ok(rows.some(row=>row[0]==="응답 목표 초과 정책"&&row[1]==="실행 전 거절"))
  assert.ok(tree.some(node=>node.props.value===spec))
})

test("preprocessor summary groups exact image references without losing target architecture or path",()=>{
  const runner=hooks(),image="registry@sha256:shared",spec={input:{fields:[]},output:{fields:[]},artifacts:[{target:{model:"model",artifact:"a"},source:{image,architecture:"arm64",path:"/arm"}},{target:{model:"model",artifact:"b"},source:{image,architecture:"amd64",path:"/x86"}},{target:{model:"model",artifact:"c"},source:{image:"registry@sha256:other",architecture:"arm64",path:"/other"}}]}
  const {CatalogPage}=load("../features/management.tsx",{...base,react:runner.react,"@/lib/client":{useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/preprocessors/prep"?{metadata:{name:"prep",uid:"uid"},spec}:path==="v1/workloads"?[]:{items:[]}})}})
  const summary=elements(runner.render(CatalogPage,{kind:"preprocessors",name:"prep"})).find(node=>typeof node.type==="function"&&node.props.spec===spec)!
  const table=elements((summary.type as (props:Record<string,unknown>)=>unknown)(summary.props)).find(node=>node.type==="table")!
  const rows=table.props.data as [string,string][]
  assert.equal(rows.filter(([,value])=>value===image).length,1)
  for(const value of ["arm64","amd64","/arm","/x86","/other"])assert.ok(rows.some(row=>row.includes(value)))
})

test("model connected services separate names, observed devices and admission without repeating model",()=>{
  const runner=hooks(),view={desired:{metadata:{name:"service",uid:"service-uid"},spec:{modelRef:{name:"model"},preprocessorRef:{name:"prep"},slo:{}}},admission:true}
  const {ModelSectionPage,CatalogSettingsPage}=load("../features/management.tsx",{...base,react:runner.react,"@/lib/client":{useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/models/model"?{metadata:{name:"model",uid:"model-uid"},spec:{artifacts:[],executionPaths:[]}}:path==="v1/workloads"?[view]:path==="v1/pools"?{workloads:[{workload:{uid:"service-uid"},nodes:[{name:"device",uid:"hidden"}]}]}:{items:[]}})}})
  const table=elements(runner.render(ModelSectionPage,{name:"model",section:"services"})).find(node=>node.type==="table")!
  const columns=table.props.columns as {key:string;value:(row:unknown)=>unknown}[]
  assert.deepEqual(columns.map(column=>column.key),["service","devices","admission"])
  assert.equal(columns.find(column=>column.key==="devices")!.value(view),"device")
  assert.ok(!JSON.stringify(columns.find(column=>column.key==="service")!.value(view)).includes("model"))
  assert.notEqual(table.props.urlState,true)
  const primary=elements(runner.render(CatalogSettingsPage,{kind:"models"})).find(node=>node.type==="table")!
  assert.equal(primary.props.urlState,true)
})

test("plan search evidence renders v2 diagnostics and historical search without new fields",()=>{
  const {PlanSearchEvidence}=load("../features/scheduler-capacity.tsx",{...base,"@/lib/capacity":capacity})
  const old={neighborhood:3,evaluated:40,durationNanos:1000}
  for(const search of [old,{...old,strategy:"v2",pruned:{ZeroCapacity:2},surrogateEvaluated:100,finalists:16,exactEvaluated:19,fallback:"ExactKeep",start:"priced",surrogateGapJoules:0,postFilterReverts:0}]){
    const tree=elements(PlanSearchEvidence({capacity:{search,plans:[{node:{name:"a"},key:"plan",eligible:false,reason:"Excluded",dominated:"ZeroCapacity"}]}}))
    const facts=tree.find(node=>node.type==="facts")!.props.items as [string,string][]
    assert.ok(facts.some(([label,value])=>label==="평가 수"&&value==="40"))
    assert.equal(facts.some(([label])=>label==="탐색 전략"),"strategy" in search)
    if("strategy" in search){
      for(const pair of [["탐색 전략","v2"],["가지치기 · ZeroCapacity","2"],["최종 후보 수","16"],["대체 탐색 사유","ExactKeep"],["출발점","priced"],["증분·정확 에너지 차이 (J)","0"],["후처리 복귀 수","0"]])assert.ok(facts.some(([label,value])=>label===pair[0]&&value===pair[1]))
    }
    const table=tree.find(node=>node.type==="table")!
    const columns=table.props.columns as {key:string;value:(row:unknown)=>unknown}[]
    assert.equal(columns.find(column=>column.key==="dominated")!.value((table.props.data as unknown[])[0]),"ZeroCapacity")
  }
  assert.equal(PlanSearchEvidence({}),null)
})

test("advanced search settings use strategy select and boolean switches with zero polish allowed",()=>{
  const runner=hooks(),settings={controls:{},planSearchWorkers:1,planSearchStrategy:"v2",planSearchEvaluations:500000,planExactFinalists:16,planPolishActive:8,planPostFilterFinalists:false,planPricedStart:true}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"next/navigation":{...base["next/navigation"],useSearchParams:()=>new URLSearchParams({section:"advanced"})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({isPending:false}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/scheduler/state"?{results:[]}:undefined})}})
  const tree=elements(runner.render(SchedulerSettingsPage)),table=tree.find(node=>node.type==="table")!
  const fields=table.props.data as {key:string}[],columns=table.props.columns as {key:string;value:(field:unknown)=>Tree}[]
  assert.equal(fields.length,7)
  const editor=(key:string)=>columns.find(column=>column.key==="edit")!.value(fields.find(field=>field.key===key)).props
  assert.deepEqual(editor("planSearchStrategy").options,["v1","v2","shadow"])
  assert.equal(editor("planPostFilterFinalists").initial,false)
  assert.equal(editor("planPricedStart").initial,true)
  assert.doesNotThrow(()=>(editor("planPolishActive").save as (value:string)=>void)("0"))
})
