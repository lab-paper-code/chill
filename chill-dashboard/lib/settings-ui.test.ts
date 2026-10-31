import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as domain from "./domain.ts"
import * as scheduler from "./scheduler.ts"
import * as sourceExport from "./source-export.ts"

type Tree={type:unknown;props:Record<string,unknown>}
const require=createRequire(import.meta.url)
function elements(node:unknown):Tree[] {
  if(Array.isArray(node))return node.flatMap(elements)
  if(!node||typeof node!=="object"||!("props" in node))return []
  const element=node as Tree
  return [element,...elements(element.props.children)]
}
function hooks() {
  const values:unknown[]=[];let cursor=0
  return {react:{useState(initial:unknown){const index=cursor++;if(!Object.hasOwn(values,index))values[index]=typeof initial==="function"?initial():initial;return [values[index],(next:unknown)=>{values[index]=typeof next==="function"?next(values[index]):next}]},useRef:()=>({current:null}),useEffect:()=>{},useId:()=>"field"},render(Component:(props:Record<string,unknown>)=>unknown,props:Record<string,unknown>={}){cursor=0;return Component(props)}}
}
function load(path:string,modules:Record<string,unknown>):Record<string,(props:Record<string,unknown>)=>unknown> {
  const compiled=transpileModule(readFileSync(new URL(path,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  new Function("require","exports",compiled)((name:string)=>Object.hasOwn(modules,name)?modules[name]:name.startsWith("@/")?{}:require(name),exports)
  return exports
}
const base={"next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams()},"@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{}})},"@/lib/domain":domain,"@/lib/scheduler":scheduler,"@/lib/source-export":sourceExport,"@/components/screen":{Screen:"main",Facts:"facts",Rows:"rows",Row:"row",RowDialog:"row-dialog"},"@/components/data-table":{DataTable:"table"},"@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},"@/components/ui/sheet":{Sheet:"sheet",SheetContent:"aside",SheetHeader:"header",SheetTitle:"h2",SheetTrigger:"trigger"},"@/components/read-region":{QueryGate:"query",StatusBadge:"status"},"@/components/settings-navigation":{SettingsNavigation:"tabs"},"@/features/service-hub":{ServiceAdmission:"admission"}}

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
