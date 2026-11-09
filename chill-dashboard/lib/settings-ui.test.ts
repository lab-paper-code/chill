import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { setImmediate as settle } from "node:timers/promises"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as domain from "./domain.ts"
import * as scheduler from "./scheduler.ts"
import * as sourceExport from "./source-export.ts"
import * as labels from "./labels.ts"
import * as settingDefinitions from "./scheduler-settings.ts"
import * as capacity from "./capacity.ts"
import * as schedulerSelection from "./scheduler-selection.ts"

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
  return {react:{useState(initial:unknown){const index=cursor++;if(!Object.hasOwn(values,index))values[index]=typeof initial==="function"?initial():initial;const state=values;return [state[index],(next:unknown)=>{state[index]=typeof next==="function"?next(state[index]):next}]},useRef(initial:unknown){const index=cursor++;if(!Object.hasOwn(values,index))values[index]={current:initial};return values[index]},useEffect:()=>{},useId:()=>"field"},render(Component:(props:Record<string,unknown>)=>unknown,props:Record<string,unknown>={}){cursor=0;if(!states.has(Component))states.set(Component,[]);values=states.get(Component)!;return Component(props)}}
}
function load(path:string,modules:Record<string,unknown>):Record<string,(props:Record<string,unknown>)=>unknown> {
  const compiled=transpileModule(readFileSync(new URL(path,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  modules={"@/lib/scheduler-selection":schedulerSelection,...modules}
  new Function("require","exports",compiled)((name:string)=>Object.hasOwn(modules,name)?modules[name]:name==="@/hooks/use-catalog-resource"?load("../hooks/use-catalog-resource.ts",modules):name==="@/features/catalog-editor"?load("../features/catalog-editor.tsx",modules):name==="@/lib/scheduler-settings"?settingDefinitions:name.startsWith("@/")?{}:require(name),exports)
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
    const {CatalogSettingsPage}=load("../features/management.tsx",{...base,react:runner.react,"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams}})},"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/workloads?view=summary"?[]:{items:[item]}})}})
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
  const {ModelPage}=load("../features/management.tsx",{...base,react:runner.react,"@/lib/client":{useChillMutation:()=>({isPending:false}),useChillQuery:(path:string)=>({data:path==="v1/models/registered"?current:path==="v1/workloads?view=summary"?[]:{items:[]}})}})
  const tree=runner.render(ModelPage,{name:"registered",expectedUID:"original",embedded:true}) as Tree
  assert.equal(tree.props.uid,"replacement")
  assert.equal(tree.props.kind,"models")
  assert.ok(!elements(tree).some(element=>element.props.label==="삭제"))
})

test("operating setting writes retain exact integers and omit runtime-only fields",async()=>{
  const {putSchedulerSettings}=load("../features/scheduler-settings.tsx",{...base,"@/lib/client":{}})
  const sent:{path:string;method:string;body:string}[]=[]
  const preserved={defaultOperationCost:{prepare:{seconds:1.25,joules:2.5},remove:{seconds:0,joules:0},power:{seconds:.5,joules:3}},defaultIdlePower:{nodeWatts:4.25,perInstanceWatts:.75},defaultDispatchOccupancyNanos:9223372036854775807n}
  const planner={planSearchStrategy:"v2",planSearchEvaluations:9223372036854775807n,planExactFinalists:9007199254740993n,planPolishActive:0,planPostFilterFinalists:false,planPricedStart:true}
  const settings={observationWindowNanos:9007199254740993n,controls:{placement:true},staticRouting:{service:{instance:1}},staticRoutingValidatedAt:"past",powerObservation:{series:"read-only"},meters:{secret:"runtime"},requestTimeout:10}
  const writer=putSchedulerSettings as unknown as (mutate:(input:{path:string;method:string;body:string})=>Promise<{body:string}>,client:{invalidateQueries:()=>Promise<void>},settings:unknown)=>Promise<unknown>
  await writer(async input=>{sent.push(input);return {body:'{"appliedAt":"2026-11-01T00:00:00Z"}'}},{invalidateQueries:async()=>{}},{...settings,...preserved,...planner})
  assert.equal(sent.length,1)
  assert.equal(sent[0].path,"v1/scheduler/settings")
  const body=scheduler.parseSchedulerJSON(sent[0].body) as Record<string,unknown>
  assert.equal(body.observationWindowNanos,9007199254740993n)
  for(const [key,value] of Object.entries(preserved))assert.deepEqual(body[key],value)
  for(const [key,value] of Object.entries(planner))assert.deepEqual(body[key],value)
  for(const key of ["staticRouting","staticRoutingValidatedAt","powerObservation","meters","requestTimeout"])assert.ok(!Object.hasOwn(body,key))
  assert.ok(Object.hasOwn(settings,"staticRouting"),"the source draft is not mutated")
})

test("operating settings expose service automation, minimum demand and excluded devices together",()=>{
  const runner=hooks(),settings={controls:{},autonomousWorkloads:["known","retired"],minimumDemand:{known:.3,retired:20},excludedNodes:["meter"]}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"next/navigation":{useSearchParams:()=>new URLSearchParams({section:"targets"}),useRouter:()=>({push(){},replace(){}})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/workloads?view=summary"?[{desired:{metadata:{name:"카메라",uid:"known"}}}]:path==="v1/scheduler/state"?{results:[]}:undefined})}})
  const tree=elements(runner.render(SchedulerSettingsPage))
  for(const key of ["autonomousWorkloads","minimumDemand","excludedNodes"])assert.ok(tree.some(node=>node.props.editKey===key),`${key} has an editable field`)
})

test("operating range table edits accumulate a precise draft before review",()=>{
  const runner=hooks(),settings={controls:{placement:true},observationWindowNanos:1000000000n,controlIntervalNanos:1000000000n,comparisonPeriodNanos:1000000000n,transitionReserveFraction:0,scaleDownHeadroomFraction:0}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"next/navigation":{...base["next/navigation"],useSearchParams:()=>new URLSearchParams({section:"cycle"})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({isPending:false}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/scheduler/state"?{results:[]}:undefined})}})
  let tree=elements(runner.render(SchedulerSettingsPage))
  assert.equal(tree.find(element=>element.type==="tabs")?.props.active,"configuration")
  const table=tree.find(element=>element.type==="table")!
  const field=(table.props.data as {key:string}[]).find(field=>field.key==="observationWindowNanos")!
  const edit=(table.props.columns as {key:string;value:(field:unknown)=>Tree}[]).find(column=>column.key==="edit")!.value(field)
  ;(edit.props.save as (value:string)=>void)("9007199254.740993")
  tree=elements(runner.render(SchedulerSettingsPage))
  const updated=tree.find(element=>element.type==="table")!
  const value=(updated.props.columns as {key:string;value:(field:unknown)=>unknown}[]).find(column=>column.key==="value")!.value(field)
  assert.ok(JSON.stringify(value).includes("9007199254.740993ms"))
  const action=(tree.find(element=>element.type==="main")!.props.action as Tree)
  assert.ok(JSON.stringify(action.props.children).includes("1"))
})

test("operating sections and numeric, boolean and node Sheets follow URL history and reload",()=>{
  let params=new URLSearchParams({tab:"configuration",section:"limit",namespace:"kept"})
  const history=[params.toString()]
  const navigation={useSearchParams:()=>params,useRouter:()=>({
    push:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams;history.push(params.toString())},
    replace:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams;history[history.length-1]=params.toString()},
  })}
  const settings={controls:{placement:true},maxReplicas:3,maxInstances:4,servingMemoryBytes:1048576,capacityWindows:5,excludedNodes:["meter"]}
  const runner=hooks()
  const modules={...base,react:runner.react,"next/navigation":navigation,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({isPending:false}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/scheduler/state"?{results:[]}:undefined})}}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",modules)
  let tree=elements(runner.render(SchedulerSettingsPage))
  assert.equal(tree.find(node=>node.type==="select")!.props.value,"limit")
  assert.deepEqual((tree.find(node=>node.type==="table")!.props.data as {key:string}[]).map(row=>row.key),["servingMemoryBytes","maxReplicas","maxInstances"])
  ;(tree.find(node=>node.type==="select")!.props.onChange as (event:unknown)=>void)({target:{value:"capacity"}})
  assert.equal(params.get("section"),"capacity")
  assert.equal(params.get("namespace"),"kept")
  const fresh=hooks()
  const reloaded=load("../features/scheduler-settings.tsx",{...modules,react:fresh.react}).SchedulerSettingsPage
  assert.equal(elements(fresh.render(reloaded)).find(node=>node.type==="select")!.props.value,"capacity")
  params=new URLSearchParams(history[0])
  tree=elements(runner.render(SchedulerSettingsPage))
  assert.equal(tree.find(node=>node.type==="select")!.props.value,"limit")
  for(const [section,key] of [["limit","maxReplicas"],["controls","controls.placement"],["targets","excludedNodes"]]){
    params.set("section",section)
    tree=elements(runner.render(SchedulerSettingsPage))
    const table=tree.find(node=>node.type==="table")!
    const columns=table.props.columns as {key:string;value:(row:unknown)=>Tree}[]
    const row=key==="excludedNodes"?undefined:(table.props.data as {key:string}[]).find(row=>row.key===key.replace("controls.",""))
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
    const refreshedRow=key==="excludedNodes"?undefined:(refreshedTable.props.data as {key:string}[]).find(row=>row.key===key.replace("controls.",""))
    const refreshedEditor=key==="excludedNodes"?refreshedTree.find(node=>node.props.editKey===key)!:refreshedColumns.find(column=>column.key==="edit")!.value(refreshedRow)
    assert.equal(elements(refreshed.render(refreshedEditor.type as (props:Record<string,unknown>)=>unknown,refreshedEditor.props)).find(node=>node.type==="sheet")!.props.open,true)
    params=new URLSearchParams(history[history.length-2])
    assert.equal(elements(runner.render(component,editor.props)).find(node=>node.type==="sheet")!.props.open,false)
  }
})

test("catalog lists keep technical source out of semantic summaries while retaining complete definitions",()=>{
  const secret="registry.example/very-long-address@sha256:"+"a".repeat(64)
  const specs={
    models:{artifacts:[{name:"internal-file-a",format:"ONNX"},{name:"internal-file-b",format:"ONNX"}],executionPaths:[{name:"internal-path-a",runtime:{backend:"cpu"}},{name:"internal-path-b",runtime:{backend:"cpu"}}]},
    preprocessors:{input:{fields:[{name:"image",format:"JPEG",maxBytes:1048576}]},output:{fields:[{name:"tensor",format:"Tensor"}]},artifacts:[{target:{model:"model-a",artifact:"internal-file-a"},source:{image:secret}},{target:{model:"model-a",artifact:"internal-file-b"},source:{image:secret}}]},
    datasets:{fields:[{name:"image",format:"JPEG",maxBytes:1048576}],source:{image:secret,path:"/data"}},
  }
  for(const [kind,spec] of Object.entries(specs)){
    const runner=hooks(),item={metadata:{name:"catalog",uid:"catalog-uid"},spec}
    const {CatalogSettingsPage}=load("../features/management.tsx",{...base,"@/lib/labels":labels,react:runner.react,"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/workloads?view=summary"?[]:{items:[item]}})}})
    const table=elements(runner.render(CatalogSettingsPage,{kind})).find(node=>node.type==="table")!
    const columns=table.props.columns as {key:string;numeric?:boolean;value:(item:unknown)=>unknown}[]
    const definition=columns.find(column=>column.key==="definition")!.value(item)
    const rendered=JSON.stringify(definition)
    assert.ok(!rendered.includes(secret))
    assert.ok(!rendered.includes("internal-file"))
    assert.ok(!rendered.includes("internal-path"))
    assert.equal(columns.find(column=>column.key==="services")!.value(item),0)
    const source=columns.find(column=>column.key==="source")!.value(item) as Tree
    assert.equal(source.type,"details")
    assert.ok(!source.props.open)
    assert.ok(elements(source).some(node=>node.props.value===spec))
  }
})

test("routing table edits preserve route, block and release semantics and reject invalid weights",()=>{
  const pageRunner=hooks(),initial={service:{instance:2},release:{old:1}}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:pageRunner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({isPending:false}),useChillQuery:(path:string,options?:{onResponse?:(response:{etag:null},data:unknown)=>void})=>{const data=path==="v1/scheduler/settings"?{controls:{},staticRouting:initial}:path==="v1/scheduler/state"?{results:[]}:undefined;if(path==="v1/scheduler/settings")options?.onResponse?.({etag:null},data);return {data}}}})
  pageRunner.render(SchedulerSettingsPage)
  const editor=elements(pageRunner.render(SchedulerSettingsPage)).find(node=>typeof node.props.apply==="function"&&"initial" in node.props)!
  const runner=pageRunner,applied:unknown[]=[]
  const props={...editor.props,snapshot:{workloads:[{uid:"service",name:"Service A"},{uid:"release",name:"Service B"},{uid:"new",name:"Service C"}],instances:[{id:"instance",ownerWorkloadUID:"service",workloadUID:"service",node:{name:"Device"},ready:true},{id:"new-instance",ownerWorkloadUID:"new",workloadUID:"new",node:{name:"Other device"},ready:true}]},apply:(...args:unknown[])=>applied.push(args)}
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

test("manual ready instances with no active route remain selectable in the routing table",()=>{
  const runner=hooks(),settings={controls:{},autonomousWorkloads:[],staticRouting:{}}
  const instance={id:"physical",ownerWorkloadUID:"manual",workloadUID:"",node:{name:"Device"},ready:true,stopped:false,profileID:"measured"}
  const snapshot={workloads:[{uid:"manual",name:"Manual service"}],instances:[instance,{...instance,id:"unready",ready:false},{...instance,id:"stopped",stopped:true},{...instance,id:"terminated",terminated:true},{...instance,id:"other-owner",ownerWorkloadUID:"other"}],routing:{}}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string,options?:{onResponse?:(response:{etag:null},data:unknown)=>void})=>{const data=path==="v1/scheduler/settings"?settings:path==="v1/scheduler/state"?{snapshot}:undefined;if(path==="v1/scheduler/settings")options?.onResponse?.({etag:null},data);return {data}}}})
  runner.render(SchedulerSettingsPage)
  const editor=elements(runner.render(SchedulerSettingsPage)).find(node=>typeof node.props.apply==="function")!
  const Component=editor.type as (props:Record<string,unknown>)=>unknown
  let tree=elements(runner.render(Component,editor.props))
  ;(tree.find(node=>node.type==="row-dialog")!.props.onOpenChange as (open:boolean)=>void)(true)
  tree=elements(runner.render(Component,editor.props))
  ;(tree.find(node=>node.type==="button"&&typeof node.props.onClick==="function")!.props.onClick as ()=>void)()
  tree=elements(runner.render(Component,editor.props))
  const table=tree.find(node=>node.type==="table")!
  assert.deepEqual((table.props.data as [string,unknown][]).map(([id])=>id),["physical"])
  const add=tree.find(node=>node.type==="button"&&node.props.children==="인스턴스 추가")!
  assert.equal(add.props.disabled,false)
  ;(add.props.onClick as ()=>void)()
  tree=elements(runner.render(Component,editor.props))
  assert.deepEqual(tree.find(node=>node.type==="table")!.props.data,[["physical",1]])
  const withdrawing={...editor.props,initial:{manual:{},other:{}},snapshot:{...snapshot,instances:[...snapshot.instances,{...instance,id:"foreign",ownerWorkloadUID:""}],routing:{other:{foreign:1}}}}
  tree=elements(runner.render(Component,withdrawing))
  ;(tree.find(node=>node.type==="row-dialog")!.props.onOpenChange as (open:boolean)=>void)(true)
  tree=elements(runner.render(Component,withdrawing))
  assert.deepEqual((tree.find(node=>node.type==="table")!.props.data as [string,unknown][]).map(([id])=>id),["physical"],"a draft withdrawal does not release another service's live destination")
})

test("automation Sheet submission renders unapplied changes and their pending count without saving",()=>{
  const runner=hooks(),settings={controls:{},autonomousWorkloads:[]},sent:unknown[]=[]
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async(input:unknown)=>sent.push(input)}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/workloads?view=summary"?[{desired:{metadata:{uid:"manual",name:"Manual service"}}}]:undefined})}})
  let tree=elements(runner.render(SchedulerSettingsPage))
  const editor=tree.find(node=>node.props.editKey==="autonomousWorkloads")!,Component=editor.type as (props:Record<string,unknown>)=>unknown
  let form=elements(runner.render(Component,editor.props))
  ;(form.find(node=>node.type==="input"&&node.props.type==="checkbox")!.props.onChange as (event:unknown)=>void)({target:{checked:true}})
  form=elements(runner.render(Component,editor.props))
  ;(form.find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  tree=elements(runner.render(SchedulerSettingsPage))
  const action=tree.find(node=>node.type==="main")!.props.action as Tree
  const pending=elements(action).find(node=>node.props.role==="status"&&/1/.test(JSON.stringify(node.props.children)))
  assert.ok(pending,"staged changes have a visible pending count")
  assert.ok(tree.some(node=>node.type==="span"&&/적용 전/.test(String(node.props.children))),"the edited table value is distinguished from saved state")
  assert.equal(action.props.disabled,false)
  assert.deepEqual(sent,[])
})

test("rendered pending settings warn before unload and route exit, but not same-page editing",()=>{
  const runner=hooks(),settings={controls:{},autonomousWorkloads:[]},browser=new EventTarget(),navigation=new EventTarget(),document=new EventTarget()
  const previousWindow=globalThis.window,previousDocument=globalThis.document
  const effectState:{cleanup?:()=>void}={}
  let confirmed=0
  Object.assign(browser,{navigation,location:new URL("https://dashboard.test/settings"),history:{state:null},confirm:()=>{confirmed++;return false}})
  Object.defineProperty(globalThis,"window",{value:browser,configurable:true,writable:true})
  Object.defineProperty(globalThis,"document",{value:document,configurable:true,writable:true})
  const react={...runner.react,useEffect:(effect:()=>void|(()=>void))=>{effectState.cleanup?.();effectState.cleanup=effect()??undefined}}
  try {
    const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:undefined})}})
    let tree=elements(runner.render(SchedulerSettingsPage))
    const editor=tree.find(node=>node.props.editKey==="autonomousWorkloads")!
    ;(editor.props.save as (value:string[],source:unknown,version:null)=>void)(["manual"],settings,null)
    tree=elements(runner.render(SchedulerSettingsPage))
    assert.ok(elements(tree.find(node=>node.type==="main")!.props.action).some(node=>node.props.role==="status"))
    const unload=new Event("beforeunload",{cancelable:true});browser.dispatchEvent(unload)
    assert.equal(unload.defaultPrevented,true)
    for(const [url,blocked] of [["/settings?section=cycle",false],["/settings?edit=autonomousWorkloads",false],["/settings?tab=models",true],["/",true]] as const){
      const event=new Event("navigate",{cancelable:true})
      Object.assign(event,{destination:{url:`https://dashboard.test${url}`,sameDocument:true}})
      navigation.dispatchEvent(event)
      assert.equal(event.defaultPrevented,blocked)
    }
    assert.equal(confirmed,2)
    ;(tree.find(node=>node.props.editKey==="autonomousWorkloads")!.props.save as (value:string[],source:unknown,version:null)=>void)([],settings,null)
    runner.render(SchedulerSettingsPage)
    const clean=new Event("beforeunload",{cancelable:true});browser.dispatchEvent(clean)
    assert.equal(clean.defaultPrevented,false)
  } finally {
    effectState.cleanup?.()
    Object.defineProperty(globalThis,"window",{value:previousWindow,configurable:true,writable:true})
    Object.defineProperty(globalThis,"document",{value:previousDocument,configurable:true,writable:true})
  }
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
  const {ModelSectionPage}=load("../features/management.tsx",{...base,react:runner.react,"@/lib/labels":labels,"@/lib/client":{useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/models/model"?{metadata:{name:"model",uid:"uid"},spec}:path==="v1/workloads?view=summary"?[]:{items:[]}})}})
  const tree=elements(runner.render(ModelSectionPage,{name:"model",section:"files"})),dialogs=tree.filter(node=>node.type==="row-dialog")
  assert.ok(dialogs.every(node=>!String(node.props.label).includes("internal-")))
  const rows=tree.filter(node=>node.type==="tr").map(row=>elements(row).filter(cell=>cell.type==="th"||cell.type==="td").map(cell=>cell.props.children))
  assert.deepEqual(rows.find(row=>row[0]==="최대 묶음 크기 (건)"),["최대 묶음 크기 (건)",8])
  assert.deepEqual(rows.find(row=>row[0]==="서비스별 대기 한도 (건)"),["서비스별 대기 한도 (건)",16])
  assert.ok(rows.some(row=>row[0]==="응답 목표 초과 정책"&&row[1]==="실행 전 거절"))
  assert.ok(tree.some(node=>node.props.value===spec))
})


test("model connected services separate names, observed devices and admission without repeating model",()=>{
  const runner=hooks(),view={desired:{metadata:{name:"service",uid:"service-uid"},spec:{modelRef:{name:"model"},preprocessorRef:{name:"prep"},slo:{}}},admission:true}
  const {ModelSectionPage,CatalogSettingsPage}=load("../features/management.tsx",{...base,react:runner.react,"@/lib/client":{useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/models/model"?{metadata:{name:"model",uid:"model-uid"},spec:{artifacts:[],executionPaths:[]}}:path==="v1/workloads?view=summary"?[view]:path==="v1/pools"?{workloads:[{workload:{uid:"service-uid"},nodes:[{name:"device",uid:"hidden"}]}]}:{items:[]}})}})
  const table=elements(runner.render(ModelSectionPage,{name:"model",section:"services"})).find(node=>node.type==="table")!
  const columns=table.props.columns as {key:string;value:(row:unknown)=>unknown}[]
  assert.deepEqual(columns.map(column=>column.key),["service","devices","admission"])
  assert.equal(columns.find(column=>column.key==="devices")!.value(view),"device")
  assert.ok(!JSON.stringify(columns.find(column=>column.key==="service")!.value(view)).includes("model"))
  assert.notEqual(table.props.urlState,true)
  const primary=elements(runner.render(CatalogSettingsPage,{kind:"models"})).find(node=>node.type==="table")!
  assert.equal(primary.props.urlState,true)
})

test("service settings forms retain unknown UIDs, validate rates and submit one reviewed full settings body",async()=>{
  const runner=hooks(),sent:{path:string;method:string;body:string}[]=[],settings={...Object.fromEntries(settingDefinitions.settingFields.map(field=>[field.key,field.defaultValue??(field.key.endsWith("Nanos")?1000000000n:1)])),servingMemoryBytes:18446744073709551615n,capacityBisectionWidth:.005,controls:{placement:true,resources:true,replicas:true,routing:true,batching:true,power:true,membership:true},autonomousWorkloads:["retired"],minimumDemand:{known:.3,retired:20},excludedNodes:["meter"]}
  let params=new URLSearchParams({section:"targets",edit:"autonomousWorkloads",workload:"known"})
  class ApiError extends Error {}
  const {SchedulerSettingsPage,SettingChanges}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"next/navigation":{useSearchParams:()=>params,useRouter:()=>({push:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams},replace:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams}})},"@/lib/client":{ApiError,useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async(input:{path:string;method:string;body:string})=>{sent.push(input);return {body:'{"appliedAt":"2026-11-03T00:00:00Z"}'}}}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/workloads?view=summary"?[{desired:{metadata:{uid:"known",name:"카메라"}}},{desired:{metadata:{uid:"new",name:"추가 서비스"}}}]:path==="v1/scheduler/state"?{results:[],snapshot:{nodes:[]}}:undefined,refetch:async()=>({data:settings})})}})
  let tree=elements(runner.render(SchedulerSettingsPage))
  let editor=tree.find(node=>node.props.editKey==="autonomousWorkloads")!
  let form=elements(runner.render(editor.type as (props:Record<string,unknown>)=>unknown,editor.props))
  assert.equal(form.find(node=>node.type==="sheet")!.props.open,true)
  const known=form.find(node=>node.type==="input"&&node.props["aria-label"]==="카메라 CHILL 자동 운영")!
  ;(known.props.onChange as (event:unknown)=>void)({target:{checked:true}})
  form=elements(runner.render(editor.type as (props:Record<string,unknown>)=>unknown,editor.props))
  ;(form.find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  assert.equal(sent.length,0,"editing never sends PUT")
  tree=elements(runner.render(SchedulerSettingsPage));editor=tree.find(node=>node.props.editKey==="minimumDemand")!
  const Component=editor.type as (props:Record<string,unknown>)=>unknown
  const rate=(name:string,value:string)=>{
    const input=elements(runner.render(Component,editor.props)).find(node=>node.type==="input"&&node.props["aria-label"]===`${name} 최소 수요 req/s`)!
    ;(input.props.onChange as (event:unknown)=>void)({target:{value}})
  }
  const submit=()=>{
    const form=elements(runner.render(Component,editor.props)).find(node=>node.type==="form")!
    ;(form.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  }
  for(const value of ["","-1","Infinity","NaN"]){rate("카메라",value);submit();assert.ok(elements(runner.render(Component,editor.props)).some(node=>node.props.role==="alert"));assert.equal(sent.length,0)}
  rate("카메라",".75")
  const choice=elements(runner.render(Component,editor.props)).find(node=>node.type==="select")!
  ;(choice.props.onChange as (event:unknown)=>void)({target:{value:"new"}})
  rate("추가 서비스","0");submit()
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
  await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  const diff=tree.find(node=>node.type===SettingChanges)!
  const rows=elements(SettingChanges(diff.props)).filter(node=>node.type==="tr").map(row=>elements(row).filter(node=>node.type==="th"||node.type==="td").map(node=>node.props.children))
  assert.ok(rows.some(row=>row[0]==="CHILL 자동 운영 · 카메라"&&row[1]==="꺼짐"&&row[2]==="켜짐"))
  assert.ok(rows.some(row=>row[0]==="최소 수요 · 카메라"&&row[1]==="0.3 req/s"&&row[2]==="0.75 req/s"))
  assert.ok(rows.some(row=>row[0]==="최소 수요 · 추가 서비스"&&row[1]==="미설정"&&row[2]==="0 req/s"))
  assert.equal(sent.length,0,"review alone never sends PUT")
  ;(tree.find(node=>node.type==="button"&&node.props.children==="설정 적용하기")!.props.onClick as ()=>void)()
  await settle()
  assert.equal(sent.length,1)
  assert.equal(sent[0].path,"v1/scheduler/settings")
  const body=scheduler.parseSchedulerJSON(sent[0].body) as Record<string,unknown>
  assert.deepEqual(body.minimumDemand,{known:.75,retired:20,new:0})
  assert.deepEqual(body.autonomousWorkloads,["retired","known"])
  assert.deepEqual(body.excludedNodes,["meter"])
  assert.equal(body.servingMemoryBytes,18446744073709551615n)
})

test("settings detect added minimum demand before review and changes after confirmation without writing",async()=>{
  for(const timing of ["review","save"]){
    const runner=hooks(),initial={controls:{},autonomousWorkloads:[],excludedNodes:[]},sent:unknown[]=[]
    let current:typeof initial&{minimumDemand?:Record<string,number>}=initial
    const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async(input:unknown)=>{sent.push(input)}}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?initial:undefined,refetch:async()=>({data:current})})}})
    let tree=elements(runner.render(SchedulerSettingsPage))
    const editor=tree.find(node=>node.props.editKey==="excludedNodes")!
    ;(editor.props.save as (value:string[])=>void)(["meter"])
    if(timing==="review")current={...initial,minimumDemand:{other:40}}
    tree=elements(runner.render(SchedulerSettingsPage))
    ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
    await settle()
    tree=elements(runner.render(SchedulerSettingsPage))
    if(timing==="save"){
      current={...initial,minimumDemand:{other:40}}
      ;(tree.find(node=>node.type==="button"&&node.props.children==="설정 적용하기")!.props.onClick as ()=>void)()
      await settle()
      tree=elements(runner.render(SchedulerSettingsPage))
    }
    assert.equal(sent.length,0)
    assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
    assert.ok(!tree.some(node=>node.type==="button"&&node.props.children==="설정 적용하기"))
  }
})


test("minimum demand removal is distinct from an explicit zero",()=>{
  const runner=hooks(),saved:unknown[]=[],settings={controls:{},minimumDemand:{known:0,retired:20}}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:undefined})}})
  const editor=elements(runner.render(SchedulerSettingsPage)).find(node=>node.props.editKey==="minimumDemand")!,Component=editor.type as (props:Record<string,unknown>)=>unknown,props={...editor.props,save:(value:unknown)=>saved.push(value)}
  let tree=elements(runner.render(Component,props))
  const remove=tree.filter(node=>node.type==="button"&&node.props.children==="미설정으로")[1]
  ;(remove.props.onClick as ()=>void)()
  tree=elements(runner.render(Component,props))
  ;(tree.find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  assert.deepEqual(saved,[{known:0}])
})

test("settings conflict and uncertain outcomes retain draft and never automatically resubmit",async()=>{
  for(const error of [{status:409,unknownOutcome:false},{status:504,unknownOutcome:true}]){
    const runner=hooks(),settings={controls:{},excludedNodes:[]},sent:unknown[]=[],mutation:{error?:typeof error;mutateAsync:(input:unknown)=>Promise<void>;reset:()=>void}={mutateAsync:async input=>{sent.push(input)},reset:()=>{delete mutation.error}}
    const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>mutation,useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:undefined,refetch:async()=>({data:settings})})}})
    let tree=elements(runner.render(SchedulerSettingsPage))
    ;(tree.find(node=>node.props.editKey==="excludedNodes")!.props.save as (value:string[])=>void)(["meter"])
    mutation.error=error
    tree=elements(runner.render(SchedulerSettingsPage))
    assert.equal(tree.find(node=>node.type==="main")!.props.title,error.status===409?"설정 변경 충돌":"적용 결과 미확인")
    assert.equal(sent.length,0)
    if(error.status===409){
      ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
      await settle()
      tree=elements(runner.render(SchedulerSettingsPage))
      assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
    } else assert.ok(JSON.stringify(tree).includes("자동 재제출"))
  }
})

test("settings writer passes the exact ETag only as If-Match metadata and returns the new ETag",async()=>{
  const {putSchedulerSettings}=load("../features/scheduler-settings.tsx",{...base,"@/lib/client":{}})
  const sent:{body:string;version?:string}[]=[]
  const writer=putSchedulerSettings as unknown as (mutate:(input:{body:string;version?:string})=>Promise<{body:string;etag?:string}>,client:{invalidateQueries:()=>Promise<void>},settings:unknown,version?:string)=>Promise<{etag:string|null}>
  const result=await writer(async input=>{sent.push(input);return {body:'{"appliedAt":"2026-11-03T00:00:00Z"}',etag:'"8"'}},{invalidateQueries:async()=>{}},{controls:{},excludedNodes:[],staticRouting:{},powerObservation:{},meters:[],requestTimeout:1},'"7"')
  assert.equal(sent[0].version,'"7"')
  const body=JSON.parse(sent[0].body)
  assert.deepEqual(Object.keys(body).sort(),["controls","excludedNodes"])
  assert.equal(result.etag,'"8"')
})

test("untouched service Sheets follow GET, while edited Sheets preserve the displayed snapshot and ETag",()=>{
  for(const dirty of [false,true]){
    const runner=hooks(),initial={controls:{},minimumDemand:{known:.3}},latest={...initial,minimumDemand:{known:.3,other:40}},saved:unknown[][]=[]
    const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?initial:undefined})}})
    const editor=elements(runner.render(SchedulerSettingsPage)).find(node=>node.props.editKey==="minimumDemand")!,Component=editor.type as (props:Record<string,unknown>)=>unknown
    const props={...editor.props,version:'"7"',services:[{uid:"known",name:"A"},{uid:"other",name:"B"}],save:(...args:unknown[])=>saved.push(args)}
    let tree=elements(runner.render(Component,props))
    if(dirty)(tree.find(node=>node.type==="input"&&node.props["aria-label"]==="A 최소 수요 req/s")!.props.onChange as (event:unknown)=>void)({target:{value:".75"}})
    const refreshed={...props,initial:latest,version:'"8"'}
    tree=elements(runner.render(Component,refreshed))
    assert.equal(tree.some(node=>node.type==="input"&&node.props["aria-label"]==="B 최소 수요 req/s"),!dirty)
    ;(tree.find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
    assert.deepEqual(saved[0],[dirty?{known:.75}:{known:.3,other:40},dirty?initial:latest,dirty?'"7"':'"8"'])
  }
})

test("minimum demand values are visible in the current table without opening an editor",()=>{
  const runner=hooks(),settings={controls:{},minimumDemand:{known:0,other:.3}}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/workloads?view=summary"?[{desired:{metadata:{uid:"known",name:"카메라"}}}]:undefined})}})
  const tree=elements(runner.render(SchedulerSettingsPage)),tables=tree.filter(node=>node.type==="table")
  assert.ok(tables.some(table=>elements(table).some(node=>node.type==="th"&&node.props.children==="req/s")))
  const cells=tables.flatMap(table=>elements(table).filter(node=>node.type==="td").map(node=>node.props.children))
  assert.ok(cells.includes(0));assert.ok(cells.includes(.3))
})

test("an edited service Sheet cannot adopt a newer GET baseline or ETag behind the operator",async()=>{
  const runner=hooks(),original={controls:{},minimumDemand:{known:.3}},sent:unknown[]=[]
  let current:typeof original&{minimumDemand:Record<string,number>}=original,tag='"7"',publish:((response:{etag:string},data:unknown)=>void)|undefined
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async(input:unknown)=>{sent.push(input)}}),useChillQuery:(path:string,options?:{onResponse?:(response:{etag:string},data:unknown)=>void})=>{
    if(path==="v1/scheduler/settings"){publish=options?.onResponse;publish?.({etag:tag},current);return {data:current,refetch:async()=>{publish?.({etag:tag},current);return {data:current}}}}
    return {refetch:async()=>({})}
  }}})
  runner.render(SchedulerSettingsPage)
  let tree=elements(runner.render(SchedulerSettingsPage)),editor=tree.find(node=>node.props.editKey==="minimumDemand")!
  const Component=editor.type as (props:Record<string,unknown>)=>unknown
  let form=elements(runner.render(Component,editor.props))
  ;(form.find(node=>node.type==="input")!.props.onChange as (event:unknown)=>void)({target:{value:".75"}})
  current={...original,minimumDemand:{known:.3,other:40}};tag='"8"';publish?.({etag:tag},current)
  tree=elements(runner.render(SchedulerSettingsPage));editor=tree.find(node=>node.props.editKey==="minimumDemand")!
  form=elements(runner.render(Component,editor.props))
  ;(form.find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
  await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  assert.equal(sent.length,0)
  assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
  assert.ok(!tree.some(node=>node.type==="button"&&node.props.children==="설정 적용하기"))
})

test("versioned settings use their baseline ETag, refresh on 409 and require explicit recovery on 428",async()=>{
  for(const status of [200,409,428]){
    const runner=hooks(),initial={controls:{},excludedNodes:[]},sent:{version?:string;body:string}[]=[]
    let tag:string|null=status===428?null:'"7"',reads=0
    class ApiError extends Error {status:number;unknownOutcome=false;constructor(){super();this.status=status}}
    const mutation:{error?:ApiError;reset:()=>void;mutateAsync:(input:{version?:string;body:string})=>Promise<{body:string;etag:string}>}={reset:()=>{delete mutation.error},mutateAsync:async input=>{
      sent.push(input);tag='"8"'
      if(status!==200){mutation.error=new ApiError();throw mutation.error}
      return {body:'{"appliedAt":"2026-11-03T00:00:00Z"}',etag:tag}
    }}
    const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{ApiError,useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>mutation,useChillQuery:(path:string,options?:{onResponse?:(response:{etag:string|null},data:unknown)=>void})=>{
      if(path==="v1/scheduler/settings"){if(!reads){reads++;options?.onResponse?.({etag:tag},initial)}return {data:initial,refetch:async()=>{reads++;options?.onResponse?.({etag:tag},initial);return {data:initial}}}}
      return {refetch:async()=>({})}
    }}})
    runner.render(SchedulerSettingsPage)
    let tree=elements(runner.render(SchedulerSettingsPage))
    ;(tree.find(node=>node.props.editKey==="excludedNodes")!.props.save as (value:string[])=>void)(["meter"])
    tree=elements(runner.render(SchedulerSettingsPage))
    ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
    await settle()
    tree=elements(runner.render(SchedulerSettingsPage))
    const warnings=tree.filter(node=>typeof node.props.help==="string"&&node.props.help.includes("동시 변경을 완전히"))
    assert.equal(warnings.length,status===428?1:0)
    ;(tree.find(node=>node.type==="button"&&node.props.children==="설정 적용하기")!.props.onClick as ()=>void)()
    await settle()
    tree=elements(runner.render(SchedulerSettingsPage))
    assert.equal(sent.length,1)
    assert.equal(sent[0].version,status===428?undefined:'"7"')
    assert.deepEqual(JSON.parse(sent[0].body),{controls:{},excludedNodes:["meter"]})
    if(status===200){
      assert.equal(tree.find(node=>node.type==="main")!.props.title,"설정 적용 완료")
      ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
      tree=elements(runner.render(SchedulerSettingsPage))
      assert.equal(tree.find(node=>node.props.editKey==="minimumDemand")!.props.version,'"8"')
    } else if(status===409){
      assert.ok(reads>=4,"conflict triggers GET after the rejected PUT")
      assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
    } else {
      assert.equal(tree.find(node=>node.type==="main")!.props.title,"버전 없음")
      ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
      await settle()
      tree=elements(runner.render(SchedulerSettingsPage))
      assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
    }
    assert.equal(sent.length,1,"recovery never resubmits")
  }
})

test("a ConfigMap-only ETag change blocks an otherwise identical settings body",async()=>{
  const runner=hooks(),settings={controls:{},excludedNodes:[]},sent:unknown[]=[]
  let tag='"7"'
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async(input:unknown)=>{sent.push(input)}}),useChillQuery:(path:string,options?:{onResponse?:(response:{etag:string},data:unknown)=>void})=>{
    if(path==="v1/scheduler/settings"){options?.onResponse?.({etag:tag},settings);return {data:settings,refetch:async()=>{options?.onResponse?.({etag:tag},settings);return {data:settings}}}}
    return {refetch:async()=>({})}
  }}})
  runner.render(SchedulerSettingsPage)
  let tree=elements(runner.render(SchedulerSettingsPage))
  ;(tree.find(node=>node.props.editKey==="excludedNodes")!.props.save as (value:string[])=>void)(["meter"])
  tag='"8"'
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
  await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  assert.equal(sent.length,0)
  assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
})

test("a newer GET after successful PUT remains the settings and ETag baseline for the next edit",async()=>{
  const runner=hooks(),initial={controls:{},excludedNodes:[]},sent:{version?:string}[]=[]
  let current:Record<string,unknown>=initial,tag='"7"',published=false,publish:((response:{etag:string},data:unknown)=>void)|undefined
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{current={controls:{},excludedNodes:["meter"],minimumDemand:{service:9}};tag='"9"';publish?.({etag:tag},current)}})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async(input:{version?:string})=>{sent.push(input);return {body:'{"appliedAt":"2026-11-03T00:00:00Z","after":{"controls":{},"excludedNodes":["meter"]}}',etag:'"8"'}}}),useChillQuery:(path:string,options?:{onResponse?:(response:{etag:string},data:unknown)=>void})=>{
    if(path==="v1/scheduler/settings"){publish=options?.onResponse;if(!published){published=true;publish?.({etag:tag},current)}return {data:current,refetch:async()=>{publish?.({etag:tag},current);return {data:current}}}}
    return {refetch:async()=>({})}
  }}})
  runner.render(SchedulerSettingsPage)
  let tree=elements(runner.render(SchedulerSettingsPage))
  ;(tree.find(node=>node.props.editKey==="excludedNodes")!.props.save as (value:string[])=>void)(["meter"])
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  ;(tree.find(node=>node.type==="button"&&node.props.children==="설정 적용하기")!.props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
  tree=elements(runner.render(SchedulerSettingsPage))
  assert.equal(tree.find(node=>node.props.editKey==="minimumDemand")!.props.version,'"9"')
  ;(tree.find(node=>node.props.editKey==="excludedNodes")!.props.save as (value:string[])=>void)(["meter","other"])
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="설정 적용하기"))
  assert.ok(!tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
  assert.deepEqual(sent,[{path:"v1/scheduler/settings",method:"PUT",body:'{"controls":{},"excludedNodes":["meter"]}',version:'"7"'}])
})

test("unknown-outcome recovery adopts the fetched settings and ETag together for the next review",async()=>{
  const runner=hooks(),sent:unknown[]=[]
  let current:Record<string,unknown>={controls:{},excludedNodes:[]},tag='"7"',published=false,updatedAt=0
  class ApiError extends Error {status=504;unknownOutcome=true}
  const mutation:{error?:ApiError;reset:()=>void;mutateAsync:(input:unknown)=>Promise<never>}={reset:()=>{delete mutation.error},mutateAsync:async input=>{sent.push(input);mutation.error=new ApiError();throw mutation.error}}
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{ApiError,useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>mutation,useChillQuery:(path:string,options?:{onResponse?:(response:{etag:string},data:unknown)=>void})=>{
    if(path==="v1/scheduler/settings"){if(!published){published=true;options?.onResponse?.({etag:tag},current)}return {data:current,dataUpdatedAt:updatedAt,isFetching:false,refetch:async()=>{updatedAt=Date.now()+1000;options?.onResponse?.({etag:tag},current);return {data:current}}}}
    return {data:path.startsWith("v1/observations/scheduler")?{events:[]}:undefined,isSuccess:true,isFetching:false,refetch:async()=>({})}
  }}})
  runner.render(SchedulerSettingsPage)
  let tree=elements(runner.render(SchedulerSettingsPage))
  ;(tree.find(node=>node.props.editKey==="excludedNodes")!.props.save as (value:string[])=>void)(["meter"])
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  ;(tree.find(node=>node.type==="button"&&node.props.children==="설정 적용하기")!.props.onClick as ()=>void)();await settle()
  current={controls:{},excludedNodes:["meter"],minimumDemand:{service:9}};tag='"9"'
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  const adopt=tree.find(node=>node.type==="main")!.props.action as Tree
  assert.equal(adopt.props.children,"조회한 현재 값으로 다시 검토하기")
  ;(adopt.props.onClick as ()=>void)()
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="설정 적용하기"))
  assert.ok(!tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
  assert.equal(sent.length,1,"recovery never retries the unknown PUT")
})

test("successful PUT followed by failed GET retains all last-read projections and the routing declaration",async()=>{
  const preserved={defaultOperationCost:{prepare:{seconds:1.25,joules:2.5},remove:{seconds:0,joules:0},power:{seconds:.5,joules:3}},defaultIdlePower:{nodeWatts:4.25,perInstanceWatts:.75},defaultDispatchOccupancyNanos:123}
  const runner=hooks(),settings={...preserved,controls:{},excludedNodes:[],staticRouting:{service:{instance:2}},powerObservation:{series:"power_watts"},meters:[{id:"meter"}],requestTimeout:1000000000},sent:{body:string}[]=[]
  let loaded=false,failed=false
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{failed=true}})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async(input:{body:string})=>{sent.push(input);return {body:'{"appliedAt":"2026-11-03T00:00:00Z","after":{"controls":{},"excludedNodes":["meter"]}}',etag:'"8"'}}}),useChillQuery:(path:string,options?:{onResponse?:(response:{etag:string},data:unknown)=>void})=>{
    if(path==="v1/scheduler/settings"){if(!loaded){loaded=true;options?.onResponse?.({etag:'"7"'},settings)}return {data:settings,error:failed?{status:500}:undefined,refetch:async()=>{if(failed)return {data:settings,error:{status:500}};options?.onResponse?.({etag:'"7"'},settings);return {data:settings}}}}
    return {refetch:async()=>({})}
  }}})
  runner.render(SchedulerSettingsPage)
  let tree=elements(runner.render(SchedulerSettingsPage))
  ;(tree.find(node=>node.props.editKey==="excludedNodes")!.props.save as (value:string[])=>void)(["meter"])
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  ;(tree.find(node=>node.type==="button"&&node.props.children==="설정 적용하기")!.props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
  tree=elements(runner.render(SchedulerSettingsPage))
  const editor=tree.find(node=>typeof node.props.apply==="function")!
  assert.deepEqual(editor.props.initial,settings.staticRouting)
  assert.equal(editor.props.disabled,false)
  assert.equal(tree.find(node=>node.props.editKey==="minimumDemand")!.props.version,'"8"')
  const exported=tree.find(node=>node.props.filename==="chill-scheduler-settings.json")!.props.value as Record<string,unknown>
  for(const key of ["staticRouting","powerObservation","meters","requestTimeout"] as const)assert.deepEqual(exported[key],settings[key])
  for(const key of settingDefinitions.preservedSettingKeys)assert.equal(Object.hasOwn(exported,key),false,"omitted after fields are not restored from the old snapshot")
  assert.deepEqual(Object.keys(JSON.parse(sent[0].body)).sort(),["controls","excludedNodes",...settingDefinitions.preservedSettingKeys].sort())
  const Component=editor.type as (props:Record<string,unknown>)=>unknown
  let form=elements(runner.render(Component,editor.props))
  ;(form.find(node=>node.type==="row-dialog")!.props.onOpenChange as (open:boolean)=>void)(true)
  form=elements(runner.render(Component,editor.props))
  assert.equal(form.filter(node=>node.type==="button"&&node.props.children==="기본 분배로").length,1)
})

test("routing declaration not yet read is distinct from a successful GET with no override",()=>{
  for(const known of [false,true]){
    const runner=hooks(),settings={controls:{}}
    let loaded=false
    const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string,options?:{onResponse?:(response:{etag:string},data:unknown)=>void})=>{
      if(path==="v1/scheduler/settings"){if(known&&!loaded){loaded=true;options?.onResponse?.({etag:'"7"'},settings)}return {data:settings}}
      return {}
    }}})
    runner.render(SchedulerSettingsPage)
    const tree=elements(runner.render(SchedulerSettingsPage))
    assert.equal(tree.some(node=>typeof node.props.apply==="function"),known)
    if(!known){assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="요청 분배 바꾸기"&&node.props.disabled===true));assert.ok(tree.some(node=>node.props.role==="status"&&node.props.children==="조회 필요"))}
  }
})

test("omitted editable fields stay unset after PUT success and failed GET without causing the next review to conflict",async()=>{
  const runner=hooks(),projections={staticRouting:{service:{instance:2}},powerObservation:{series:"power_watts"},meters:[{id:"meter"}],requestTimeout:1000000000},initial={controls:{},excludedNodes:[],minimumDemand:{known:40},...projections}
  let current:Record<string,unknown>=initial,tag='"7"',loaded=false,failed=false
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{failed=true}})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async()=>{tag='"8"';return {body:'{"appliedAt":"2026-11-03T00:00:00Z","after":{"controls":{},"excludedNodes":[]}}',etag:tag}}}),useChillQuery:(path:string,options?:{onResponse?:(response:{etag:string},data:unknown)=>void})=>{
    if(path==="v1/scheduler/settings"){if(!loaded){loaded=true;options?.onResponse?.({etag:tag},current)}return {data:current,error:failed?{status:500}:undefined,refetch:async()=>{if(failed)return {data:current,error:{status:500}};options?.onResponse?.({etag:tag},current);return {data:current}}}}
    return {refetch:async()=>({})}
  }}})
  runner.render(SchedulerSettingsPage)
  let tree=elements(runner.render(SchedulerSettingsPage))
  const editor=tree.find(node=>node.props.editKey==="minimumDemand")!,Component=editor.type as (props:Record<string,unknown>)=>unknown
  let form=elements(runner.render(Component,editor.props))
  ;(form.find(node=>node.type==="button"&&node.props.children==="미설정으로")!.props.onClick as ()=>void)()
  form=elements(runner.render(Component,editor.props))
  ;(form.find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  ;(tree.find(node=>node.type==="button"&&node.props.children==="설정 적용하기")!.props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
  tree=elements(runner.render(SchedulerSettingsPage))
  const stored=tree.find(node=>node.props.editKey==="minimumDemand")!.props.initial as Record<string,unknown>
  assert.equal(Object.hasOwn(stored,"minimumDemand"),false)
  for(const [key,value] of Object.entries(projections))assert.deepEqual(stored[key],value)
  ;(tree.find(node=>node.props.editKey==="excludedNodes")!.props.save as (value:string[])=>void)(["other"])
  failed=false;current={controls:{},excludedNodes:[],...projections}
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)();await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="설정 적용하기"))
  assert.ok(!tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
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
      for(const pair of [["탐색 전략","CHILL v2"],["가지치기 · ZeroCapacity","2"],["최종 후보 수","16"],["대체 탐색 사유","ExactKeep"],["출발점","priced"],["증분·정확 에너지 차이 (J)","0"],["후처리 복귀 수","0"]])assert.ok(facts.some(([label,value])=>label===pair[0]&&value===pair[1]))
    }
    const table=tree.find(node=>node.type==="table")!
    const columns=table.props.columns as {key:string;value:(row:unknown)=>unknown}[]
    assert.equal(columns.find(column=>column.key==="dominated")!.value((table.props.data as unknown[])[0]),"ZeroCapacity")
  }
  assert.equal(PlanSearchEvidence({}),null)
})

test("scheduler selection stages named choices and preserves unknown historical values",()=>{
  const runner=hooks(),saved:string[]=[]
  const {SchedulerSelection}=load("../features/scheduler-selection.tsx",{...base,react:runner.react})
  const props={current:"future-planner",value:"future-planner",disabled:false,save:(value:string)=>saved.push(value)}
  let tree=elements(runner.render(SchedulerSelection,props))
  const select=tree.find(node=>node.type==="select")!
  assert.equal(select.props.value,"future-planner")
  assert.deepEqual(tree.filter(node=>node.type==="option").map(node=>[node.props.value,node.props.children]),[["future-planner","future-planner"],["v1","CHILL v1"],["v2","CHILL v2"]])
  ;(select.props.onChange as (event:unknown)=>void)({target:{value:"v2"}})
  tree=elements(runner.render(SchedulerSelection,props))
  ;(tree.find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  assert.deepEqual(saved,["v2"])
  const {SettingChanges,SettingsSummary}=load("../features/scheduler-settings.tsx",{...base,react:runner.react})
  const before={controls:{},planSearchStrategy:"future-planner"},after={...before,planSearchStrategy:"v2"}
  const changes=elements(SettingChanges({before,after})).filter(node=>node.type==="td").map(node=>node.props.children)
  assert.deepEqual(changes,["future-planner","CHILL v2"])
  assert.ok(((SettingsSummary({settings:after}) as Tree).props.items as [string,string][]).some(([label,value])=>label==="스케줄러"&&value==="CHILL v2"))
})

test("advanced search settings validate budgets and boolean switches with zero polish allowed",()=>{
  const runner=hooks(),settings={controls:{},planSearchWorkers:1,planSearchStrategy:"v2",planSearchEvaluations:500000,planExactFinalists:16,planPolishActive:8,planPostFilterFinalists:false,planPricedStart:true}
  const {SchedulerSettingsPage,SettingChanges,SettingsSummary}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"next/navigation":{...base["next/navigation"],useSearchParams:()=>new URLSearchParams({section:"advanced"})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({isPending:false}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/scheduler/state"?{results:[]}:undefined})}})
  const table=()=>elements(runner.render(SchedulerSettingsPage)).find(node=>node.type==="table")!
  const fields=table().props.data as {key:string}[]
  const editor=(key:string)=>{
    const columns=table().props.columns as {key:string;value:(field:unknown)=>Tree}[]
    return columns.find(column=>column.key==="edit")!.value(fields.find(field=>field.key===key)).props
  }
  for(const key of ["planSearchEvaluations","planExactFinalists","planPolishActive"]){
    const save=editor(key).save as (value:string)=>void
    for(const text of ["-1","1.5","Infinity","9223372036854775808"])assert.throws(()=>save(text))
    if(key!=="planPolishActive")assert.throws(()=>save("0"))
  }
  for(const [key,value] of Object.entries({planSearchEvaluations:"9007199254740993",planExactFinalists:"19",planPolishActive:"0",planPostFilterFinalists:true,planPricedStart:false})){
    ;(editor(key).save as (value:string|boolean)=>void)(value)
  }
  const after={...settings,planSearchEvaluations:9007199254740993n,planExactFinalists:19n,planPolishActive:0n,planPostFilterFinalists:true,planPricedStart:false}
  const diff=elements(SettingChanges({before:settings,after})).filter(node=>node.type==="tr").slice(1)
  assert.equal(diff.length,5)
  const summary=(SettingsSummary({settings:after}) as Tree).props.items as [string,string][]
  const updated=table(),columns=updated.props.columns as {key:string;value:(field:unknown)=>Tree}[]
  for(const [key,expected] of Object.entries({planSearchEvaluations:"9007199254740993회",planExactFinalists:"19개",planPolishActive:"0개",planPostFilterFinalists:"사용",planPricedStart:"사용 안 함"})){
    const field=fields.find(field=>field.key===key)!
    const value=columns.find(column=>column.key==="value")!.value(field)
    assert.equal((value.props.children as unknown[])[0],expected)
    assert.ok(summary.some(([label,value])=>label===settingDefinitions.settingFields.find(field=>field.key===key)!.label&&value===expected))
    assert.equal(editor(key).changed,true)
  }
  assert.equal(editor("planPolishActive").initial,"0")
  assert.equal(editor("planPricedStart").initial,false)
})

test("advanced switch Sheets submit booleans through native controls",()=>{
  for(const [key,initial,next] of [["planPostFilterFinalists",false,true],["planPricedStart",true,false]] as const){
    const runner=hooks(),settings={controls:{},planSearchWorkers:1,[key]:initial}
    const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"next/navigation":{...base["next/navigation"],useSearchParams:()=>new URLSearchParams({section:"advanced"})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:undefined})}})
    const table=elements(runner.render(SchedulerSettingsPage)).find(node=>node.type==="table")!
    const field=(table.props.data as {key:string}[]).find(field=>field.key===key)!
    const editor=(table.props.columns as {key:string;value:(field:unknown)=>Tree}[]).find(column=>column.key==="edit")!.value(field)
    const Component=editor.type as (props:Record<string,unknown>)=>unknown,saved:unknown[]=[]
    const props={...editor.props,save:(value:unknown)=>saved.push(value)}
    let tree=elements(runner.render(Component,props))
    const control=tree.find(node=>node.props.role==="switch")!
    assert.equal(control.props.checked,initial)
    ;(control.props.onChange as (event:unknown)=>void)({target:{value:next,checked:next}})
    tree=elements(runner.render(Component,props))
    ;(tree.find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
    assert.deepEqual(saved,[next])
  }
})

test("historical settings keep absent planner fields out of summaries, equality defaults and PUT bodies",async()=>{
  const runner=hooks(),settings={controls:{},planSearchWorkers:1}
  const {SchedulerSettingsPage,putSchedulerSettings,SettingsSummary}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"next/navigation":{...base["next/navigation"],useSearchParams:()=>new URLSearchParams({section:"advanced"})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:undefined})}})
  const table=elements(runner.render(SchedulerSettingsPage)).find(node=>node.type==="table")!
  const fields=(table.props.data as {key:string}[]).filter(field=>field.key!=="planSearchWorkers")
  const columns=table.props.columns as {key:string;value:(field:unknown)=>Tree}[]
  const summary=(SettingsSummary({settings}) as Tree).props.items as [string,string][]
  for(const field of fields){
    const definition=settingDefinitions.settingFields.find(item=>item.key===field.key)!
    assert.equal((columns.find(column=>column.key==="value")!.value(field).props.children as unknown[])[0],"기록 없음")
    assert.ok(summary.some(([label,value])=>label===definition.label&&value==="기록 없음"))
    const equal=scheduler.settingsEqual as (before:unknown,after:unknown)=>boolean
    assert.equal(equal(settings,{...settings,[field.key]:definition.defaultValue}),false)
  }
  const sent:{body:string}[]=[]
  const writer=putSchedulerSettings as unknown as (mutate:(input:{body:string})=>Promise<{body:string}>,client:{invalidateQueries:()=>Promise<void>},settings:unknown)=>Promise<unknown>
  await writer(async input=>{sent.push(input);return {body:"{}"}},{invalidateQueries:async()=>{}},settings)
  assert.deepEqual(scheduler.parseSchedulerJSON(sent[0].body),settings)
  assert.deepEqual(Object.keys(settings).sort(),["controls","planSearchWorkers"])
})

test("priority choice and keyboard reorder stage both fields before one reviewed settings PUT",async()=>{
  const runner=hooks(),sent:{path:string;method:string;body:string}[]=[]
  const settings={controls:{placement:true},autonomousWorkloads:["a"],minimumDemand:{a:2},excludedNodes:["meter"],defaultIdlePower:{nodeWatts:4,perInstanceWatts:1},workloadPriority:["retired","a"]}
  let params=new URLSearchParams({section:"advanced",edit:"sloPriorityStrategy"})
  const {SchedulerSettingsPage,SettingChanges}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"next/navigation":{useSearchParams:()=>params,useRouter:()=>({push:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams},replace:(url:string)=>{params=new URL(url,"https://dashboard.test").searchParams}})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async(input:{path:string;method:string;body:string})=>{sent.push(input);return {body:'{"appliedAt":"2026-11-09T00:00:00Z"}'}}}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:path==="v1/workloads?view=summary"?[{desired:{metadata:{uid:"a",name:"Alpha"}}},{desired:{metadata:{uid:"b",name:"Beta"}}}]:undefined,refetch:async()=>({data:settings})})}})
  const editor=elements(runner.render(SchedulerSettingsPage)).find(node=>node.props.editKey==="sloPriorityStrategy")!
  const Component=editor.type as (props:Record<string,unknown>)=>unknown
  const form=()=>elements(runner.render(Component,editor.props))
  assert.equal(form().find(node=>node.type==="sheet")!.props.open,true)
  ;(form().find(node=>node.type==="input"&&node.props.value==="workload")!.props.onChange as ()=>void)()
  ;(form().find(node=>node.type==="select")!.props.onChange as (event:unknown)=>void)({target:{value:"b"}})
  ;(form().find(node=>node.props["aria-label"]==="b 순위 위로")!.props.onClick as ()=>void)()
  ;(form().find(node=>node.props["aria-label"]==="retired 순위 아래로")!.props.onClick as ()=>void)()
  assert.equal(form().find(node=>node.props["aria-label"]==="b 순위 위로")!.props.disabled,true)
  ;(form().find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  assert.equal(params.has("edit"),false)
  let tree=elements(runner.render(SchedulerSettingsPage))
  const staged=tree.find(node=>node.props.editKey==="sloPriorityStrategy")!.props.initial as Record<string,unknown>
  assert.equal(staged.sloPriorityStrategy,"workload")
  assert.deepEqual(staged.workloadPriority,["b","retired","a"])
  assert.equal(sent.length,0)
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
  await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  const diff=tree.find(node=>node.type===SettingChanges)!
  assert.deepEqual((diff.props.after as Record<string,unknown>).workloadPriority,["b","retired","a"])
  assert.equal(elements(SettingChanges(diff.props)).filter(node=>node.type==="tr").length,3)
  assert.equal(sent.length,0)
  ;(tree.find(node=>node.type==="button"&&node.props.children==="설정 적용하기")!.props.onClick as ()=>void)()
  await settle()
  assert.equal(sent.length,1)
  assert.equal(sent[0].path,"v1/scheduler/settings")
  assert.equal(sent[0].method,"PUT")
  assert.deepEqual(scheduler.parseSchedulerJSON(sent[0].body),{...settings,sloPriorityStrategy:"workload",workloadPriority:["b","retired","a"]})
  assert.deepEqual(settings.workloadPriority,["retired","a"])
})

test("unavailable Workload catalog preserves stored ranks until explicit removal, even under excess policy",()=>{
  const runner=hooks(),settings={controls:{},sloPriorityStrategy:"workload",workloadPriority:["retired","offline"]},saved:unknown[][]=[]
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"next/navigation":{...base["next/navigation"],useSearchParams:()=>new URLSearchParams({section:"advanced"})},"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>path==="v1/scheduler/settings"?{data:settings}:{error:{status:503}}}})
  const editor=elements(runner.render(SchedulerSettingsPage)).find(node=>node.props.editKey==="sloPriorityStrategy")!
  const Component=editor.type as (props:Record<string,unknown>)=>unknown,props={...editor.props,save:(...args:unknown[])=>saved.push(args)}
  const form=()=>elements(runner.render(Component,props))
  assert.ok(form().some(node=>node.props.role==="status"))
  assert.ok(form().some(node=>node.props["aria-label"]==="retired 순위 제거"))
  assert.ok(form().some(node=>node.props["aria-label"]==="offline 순위 제거"))
  ;(form().find(node=>node.type==="input"&&node.props.value==="excess")!.props.onChange as ()=>void)()
  ;(form().find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  assert.deepEqual(saved[0],[{sloPriorityStrategy:"excess",workloadPriority:["retired","offline"]},settings,null])
  ;(form().find(node=>node.props["aria-label"]==="retired 순위 제거")!.props.onClick as ()=>void)()
  ;(form().find(node=>node.type==="form")!.props.onSubmit as (event:unknown)=>void)({preventDefault(){}})
  assert.deepEqual(saved[1],[{sloPriorityStrategy:"workload",workloadPriority:["offline"]},settings,null])
  assert.deepEqual(settings.workloadPriority,["retired","offline"])
})

test("a concurrent Workload rank reorder blocks an unrelated pending settings write",async()=>{
  const runner=hooks(),settings={controls:{},excludedNodes:[],workloadPriority:["a","b"]},sent:unknown[]=[]
  const {SchedulerSettingsPage}=load("../features/scheduler-settings.tsx",{...base,react:runner.react,"@/lib/client":{useSession:()=>({config:{pageSize:25}}),useChillMutation:()=>({mutateAsync:async(input:unknown)=>sent.push(input)}),useChillQuery:(path:string)=>({data:path==="v1/scheduler/settings"?settings:undefined,refetch:async()=>({data:{...settings,workloadPriority:["b","a"]}})})}})
  let tree=elements(runner.render(SchedulerSettingsPage))
  ;(tree.find(node=>node.props.editKey==="excludedNodes")!.props.save as (value:string[])=>void)(["meter"])
  tree=elements(runner.render(SchedulerSettingsPage))
  ;((tree.find(node=>node.type==="main")!.props.action as Tree).props.onClick as ()=>void)()
  await settle()
  tree=elements(runner.render(SchedulerSettingsPage))
  assert.ok(tree.some(node=>node.type==="button"&&node.props.children==="최신 값을 기준으로 다시 검토하기"))
  assert.deepEqual(sent,[])
})
