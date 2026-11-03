import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import { setImmediate } from "node:timers/promises"
import * as author from "./evaluation-author.ts"
import * as request from "./evaluation-request.ts"
import * as scheduler from "./scheduler.ts"
import * as activeHelpers from "./evaluation-active.ts"
import * as experimentHelpers from "./experiments.ts"
import * as measurementHelpers from "./measurements.ts"
import * as icons from "lucide-react"

type Tree={type:unknown;props:Record<string,unknown>}
const require=createRequire(import.meta.url)
function elements(node:unknown):Tree[] {
  if(Array.isArray(node))return node.flatMap(elements)
  if(!node||typeof node!=="object"||!("props" in node))return []
  const element=node as Tree
  return [element,...elements(element.props.children)]
}
function hookRunner() {
  const values:unknown[]=[]
  let cursor=0
  return {
    react:{useState(initial:unknown) {
      const index=cursor++
      if(!Object.hasOwn(values,index))values[index]=typeof initial==="function"?initial():initial
      return [values[index],(next:unknown)=>{values[index]=typeof next==="function"?next(values[index]):next}]
    }},
    render(Component:(props:Record<string,unknown>)=>unknown,props:Record<string,unknown>={}) {cursor=0;return Component(props)},
  }
}
function load(path:string,modules:Record<string,unknown>):Record<string,(props:Record<string,unknown>)=>unknown> {
  const source=readFileSync(new URL(path,import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  new Function("require","exports",compiled)((name:string)=>Object.hasOwn(modules,name)?modules[name]:name.startsWith("@/")?{}:require(name),exports)
  return exports
}
const containers={Dialog:"dialog",DialogContent:"div",DialogHeader:"header",DialogTitle:"h2",DialogDescription:"p"}
const primitives={"next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({replace:()=>{}})},"@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{}})},"@/components/screen":{Screen:"main"},"@/components/read-region":{QueryGate:"div"},"@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},"@/components/ui/dialog":containers,"@/components/ui/sheet":{Sheet:"aside",SheetContent:"div",SheetHeader:"header",SheetTitle:"h2"},"@/lib/evaluation-author":author,"@/lib/evaluation-request":request,"@/lib/scheduler":scheduler}
test("R19: publication copy selector pages without losing its selected publication",()=>{
  const runner=hookRunner(),calls:string[]=[]
  const modules={...primitives,react:{...runner.react,useEffect:()=>{}}, "@/lib/experiments":experimentHelpers,"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:(path:string)=>{
    calls.push(path)
    const offset=new URLSearchParams(path.split("?")[1]).get("offset")
    return {data:path.startsWith("v1/experiments?")?{items:[{id:offset==="5"?"second":"first"}],page:{offset:Number(offset),limit:5,total:6,...(offset==="0"?{nextOffset:5}:{})}}:undefined}
  }},"@/features/evaluation-request":{}}
  const {EvaluationAuthor}=load("../features/evaluation-author.tsx",modules)
  let tree=elements(runner.render(EvaluationAuthor))
  assert.ok(calls.includes("v1/experiments?offset=0&limit=5"))
  const picker=tree.find(item=>item.props.label==="게시에서 복사")!
  ;(picker.props.onChange as (value:string)=>void)("first")
  tree=elements(runner.render(EvaluationAuthor))
  ;(tree.find(item=>item.type==="button"&&item.props.children==="다음")!.props.onClick as ()=>void)()
  tree=elements(runner.render(EvaluationAuthor))
  assert.ok(calls.includes("v1/experiments?offset=5&limit=5"))
  const selected=tree.find(item=>item.props.label==="게시에서 복사")!
  assert.equal(selected.props.value,"first")
  assert.ok(elements(selected).some(item=>item.type==="option"&&item.props.value==="first"))
  assert.equal(tree.find(item=>item.type==="button"&&item.props.children==="다음")?.props.disabled,true)
  ;(tree.find(item=>item.type==="button"&&item.props.children==="이전")!.props.onClick as ()=>void)()
  runner.render(EvaluationAuthor)
  assert.equal(calls.filter(path=>path==="v1/experiments").length,0)
})

test("IA: published results request URL pages without catalog hydration",()=>{
  const runner=hookRunner(),calls:string[]=[]
  let params=new URLSearchParams("view=published&offset=0")
  const run={schemaVersion:"serving-experiment.v1alpha1",id:"first",from:"2026-11-03T00:00:00Z",to:"2026-11-03T00:01:00Z",status:"FAIL",stages:[],sources:[]}
    const modules={...primitives,"lucide-react":icons,react:{...runner.react,useEffect:()=>{}},"next/navigation":{useSearchParams:()=>params},"@/lib/experiments":experimentHelpers,"@/lib/measurements":measurementHelpers,"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:(path:string,options?:{enabled?:boolean})=>{
      if(options?.enabled!==false)calls.push(path)
      const offset=new URLSearchParams(path.split("?")[1]).get("offset")
      return {data:path.startsWith("v1/experiments?")?{items:[{...run,id:offset==="5"?"second":"first"}],page:{offset:Number(offset),limit:5,total:6,...(offset==="0"?{nextOffset:5}:{})}}:undefined}
    }},"@/features/experiments-conclusion":{ExperimentResultRow:"result"}}
    const {PublishedExperimentsPage}=load("../features/experiments.tsx",modules)
    let tree=elements(runner.render(PublishedExperimentsPage))
    assert.ok(calls.includes("v1/experiments?offset=0&limit=5"))
    assert.equal(tree.find(item=>item.type==="button"&&item.props.children==="이전")?.props.disabled,true)
    const next=tree.find(item=>item.type==="a"&&item.props.children==="다음")!
    assert.equal(next.props.href,"/experiments?view=published&offset=5")
    params=new URLSearchParams(String(next.props.href).split("?")[1])
    tree=elements(runner.render(PublishedExperimentsPage))
    assert.ok(calls.includes("v1/experiments?offset=5&limit=5"))
    assert.equal(experimentHelpers.object(tree.find(item=>item.type==="result")?.props.run).id,"second")
    assert.equal(tree.find(item=>item.type==="button"&&item.props.children==="다음")?.props.disabled,true)
    const previous=tree.find(item=>item.type==="a"&&item.props.children==="이전")!
    assert.equal(previous.props.href,"/experiments?view=published&offset=0")
    params=new URLSearchParams(String(previous.props.href).split("?")[1])
    tree=elements(runner.render(PublishedExperimentsPage))
    assert.equal(experimentHelpers.object(tree.find(item=>item.type==="result")?.props.run).id,"first")
    assert.ok(calls.every(path=>path.startsWith("v1/experiments?")),"list must not hydrate publication details")
})
test("R19: old-server full catalogs stay full and do not invent pagination",()=>{
  const data=experimentHelpers.decodeExperimentPage(JSON.stringify({items:[{id:"first"},{id:"second"}]}))
  assert.equal(data.items.length,2)
  assert.equal(Object.hasOwn(data,"page"),false)
  const runner=hookRunner()
  const modules={...primitives,react:{...runner.react,useEffect:()=>{}},"@/lib/experiments":experimentHelpers,"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:()=>({data})},"@/features/evaluation-request":{}}
  const {EvaluationAuthor}=load("../features/evaluation-author.tsx",modules)
  const tree=elements(runner.render(EvaluationAuthor))
  const picker=tree.find(item=>item.props.label==="게시에서 복사")!
  assert.deepEqual(elements(picker).filter(item=>item.type==="option"&&item.props.value).map(item=>item.props.value),["first","second"])
  assert.ok(!tree.some(item=>item.type==="nav"))
})
test("load PUT denial preserves operator permission status and keeps the draft editable",async()=>{
  class ApiError extends Error {
    status:number
    body:string
    data:unknown
    constructor(status:number,body:string,data:unknown){super(`HTTP ${status}`);this.status=status;this.body=body;this.data=data}
  }
  const source={name:"load",uid:"original",spec:{future:"draft"},inUse:[]}
  let gets=0
  const client={ApiError,useChillMutation:()=>({isPending:false,mutateAsync:async(input:{path:string;method:string})=>{
    if(input.path.endsWith("/preflight"))return {data:{checks:[{id:"server",passed:true}]}}
    throw new ApiError(403,"Forbidden",null)
  }}),useChillQuery:(path:string)=>({data:path==="v1/evaluations/load-scenarios/load"?source:undefined,refetch:async()=>{gets++;return {data:source,error:null}}})}
  const parent=hookRunner(),child=hookRunner()
  const modules={...primitives,react:parent.react,"@/lib/client":client,"@/features/evaluation-author":{AuthorSelect:"select",AuthorField:"input",AuthorJSON:"textarea",AuthorHelp:"help",EvidencePicker:"select",useUnsavedAuthor:()=>{}}}
  const {LoadScenarioAuthorPage}=load("../features/load-scenario-author.tsx",modules)
  const editor=elements(parent.render(LoadScenarioAuthorPage,{name:"load"})).find(element=>element.props.editing===true&&typeof element.props.onSaved==="function")!
  // The child shares the module's mocked React bindings, but has an independent hook state.
  modules.react.useState=child.react.useState
  const Component=editor.type as (props:Record<string,unknown>)=>unknown
  let tree=child.render(Component,editor.props)
  ;(elements(tree).find(element=>element.type==="button"&&element.props.children==="사전점검")!.props.onClick as ()=>void)()
  await setImmediate()
  tree=child.render(Component,editor.props)
  const save=elements(tree).find(element=>element.type==="button"&&element.props.children==="수정")!
  assert.equal(save.props.disabled,false)
  ;(save.props.onClick as ()=>void)()
  await setImmediate()
  tree=child.render(Component,editor.props)
  assert.equal(elements(tree).find(element=>element.type==="output")?.props.children,"권한 없음(실험 운영자)")
  assert.equal(elements(tree).find(element=>element.type==="fieldset")?.props.disabled,false)
  assert.equal(gets,0,"a definitive denial is not an unknown replacement outcome")
})

test("invalid stage control JSON prevents stage removal and reordering until repaired",()=>{
  const runner=hookRunner()
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:()=>({data:{items:[]}})},"@/features/evaluation-request":{}}
  const {EvaluationSpecEditor}=load("../features/evaluation-author.tsx",modules)
  const validity:boolean[]=[]
  const props={spec:{stages:[{id:"a",control:{}},{id:"b",control:{}}]},onChange:()=>{},onValidityChange:(valid:boolean)=>validity.push(valid)}
  let tree=runner.render(EvaluationSpecEditor,props)
  const control=elements(tree).find(element=>Array.isArray(element.props.path)&&element.props.path.join("/")==="stages/0/control"&&typeof element.props.onValidityChange==="function")!
  ;(control.props.onValidityChange as (valid:boolean)=>void)(false)
  tree=runner.render(EvaluationSpecEditor,props)
  const destructive=elements(tree).filter(element=>element.type==="button"&&["단계 삭제","앞으로 이동"].includes(String(element.props.children)))
  assert.equal(destructive.length,2)
  assert.ok(destructive.every(element=>element.props.disabled===true))
  assert.equal(validity.at(-1),false)
  const invalidControl=elements(tree).find(element=>Array.isArray(element.props.path)&&element.props.path.join("/")==="stages/0/control"&&typeof element.props.onValidityChange==="function")!
  ;(invalidControl.props.onValidityChange as (valid:boolean)=>void)(true)
  tree=runner.render(EvaluationSpecEditor,props)
  assert.ok(elements(tree).filter(element=>element.type==="button"&&element.props.children==="단계 삭제").every(element=>!element.props.disabled))
  assert.equal(validity.at(-1),true)
})

test("evaluation load generator resources expose memory only",()=>{
  const runner=hookRunner(),changes:unknown[]=[]
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:()=>({data:{items:[]}})},"@/features/evaluation-request":{}}
  const {EvaluationSpecEditor}=load("../features/evaluation-author.tsx",modules)
  const props={spec:{loadGenerator:{resources:{requests:{memory:"256Mi"},limits:{memory:"512Mi"}}}},onChange:(value:unknown)=>changes.push(value)}
  let tree=elements(runner.render(EvaluationSpecEditor,props))
  ;(tree.find(element=>element.type==="button"&&element.props.children==="실행 환경")!.props.onClick as ()=>void)()
  tree=elements(runner.render(EvaluationSpecEditor,props))
  const fields=tree.filter(element=>Array.isArray(element.props.path)&&element.props.path.slice(0,2).join("/")==="loadGenerator/resources")
  assert.deepEqual(fields.map(element=>element.props.path),[["loadGenerator","resources","requests","memory"],["loadGenerator","resources","limits","memory"]])
  const field=fields[0],input=(field.type as (props:Record<string,unknown>)=>unknown)(field.props)
  ;(elements(input).find(element=>element.type==="input")!.props.onChange as (event:unknown)=>void)({target:{value:"1Gi"}})
  assert.deepEqual((changes.at(-1) as typeof props.spec).loadGenerator.resources,{requests:{memory:"1Gi"},limits:{memory:"512Mi"}})
})

test("dedicated author pages restore copy and edit identity from bookmarked URLs",()=>{
  const runner=hookRunner(),spec={question:"frozen input",future:9007199254740993n}
  let params=new URLSearchParams("copy=original"),phase="Pending",uid="old",navigation=""
  const modules={...primitives,react:runner.react,"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:(path:string)=>{navigation=path}})},"@/lib/client":{useChillQuery:()=>({data:{name:"original",uid,phase,spec}})},"@/features/evaluation-request":{}}
  const {EvaluationAuthorPage}=load("../features/evaluation-author.tsx",modules)
  for(let reload=0;reload<2;reload++) {
    const tree=elements(runner.render(EvaluationAuthorPage))
    const author=tree.find(element=>element.props.initialName==="original")!
    assert.equal(author.props.initialSpec,spec)
    assert.equal(author.props.existing,undefined)
    ;(author.props.onSubmitted as (value:{name:string;uid:string})=>void)({name:"original",uid:"confirmed-new"})
    assert.equal(navigation,"/experiments/runs/original?uid=confirmed-new")
  }
  params=new URLSearchParams("uid=old")
  let tree=elements(runner.render(EvaluationAuthorPage,{name:"original"}))
  assert.deepEqual(tree.find(element=>element.props.existing)?.props.existing,{name:"original",uid:"old"})
  uid="new"
  tree=elements(runner.render(EvaluationAuthorPage,{name:"original"}))
  assert.ok(tree.some(element=>element.props.children==="실험 교체 확인"))
  assert.ok(!tree.some(element=>element.props.existing))
  uid="old";phase="Running"
  tree=elements(runner.render(EvaluationAuthorPage,{name:"original"}))
  assert.ok(tree.some(element=>element.props.children==="시작 전 수정만 허용"))
})

test("selected run replacement blocks actions until the current UID is explicitly adopted",()=>{
  const runner=hookRunner(),params=new URLSearchParams("name=run&uid=old")
  const run={name:"run",uid:"new",phase:"Running",spec:{},conditions:[{type:"PoolAcquired",status:"True"}]}
  const modules={...primitives,react:{...runner.react,useEffect:()=>{}},"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:()=>{}})},"@/lib/client":{useChillQuery:()=>({data:run})}}
  const {EvaluationRunPage}=load("../features/evaluation-request.tsx",modules)
  const tree=elements(runner.render(EvaluationRunPage,{name:"run"}))
  assert.ok(tree.some(element=>element.props.children==="실행 교체됨"))
  assert.ok(!tree.some(element=>element.props.run===run),"replacement must not expose run mutations or release")
  assert.ok(!tree.some(element=>element.props.name==="run"&&"now" in element.props),"replacement must not expose active-run stop")
  const adopt=tree.find(element=>element.type==="a"&&element.props.children==="현재 실행 선택")!
  assert.equal(adopt.props.href,"/experiments/runs/run?uid=new")
})

test("pool occupancy is observed independently from the cleanup failure phase",()=>{
  for(const [status,label] of [["True","점유 중"],["False","반환 관측"],["Unknown","미확인"],[undefined,"미확인"]] as const) {
    const runner=hookRunner(),run={name:"run",uid:"uid",phase:"Blocked",spec:{},conditions:status?[{type:"PoolAcquired",status}]:[]}
    const modules={...primitives,react:{...runner.react,useEffect:()=>{}},"@/lib/client":{useChillQuery:()=>({data:{items:[run]}})}}
    const {EvaluationsPage}=load("../features/evaluation-request.tsx",modules)
    const tree=elements(runner.render(EvaluationsPage))
    const cells=tree.filter(element=>element.type==="td")
    assert.ok(cells.some(cell=>cell.props.children==="정리 실패"))
    assert.ok(cells.some(cell=>cell.props.children===label),`${status}: ${label}`)
  }
})

test("active stop uses the selected UID when active and detail queries disagree",()=>{
  const runner=hookRunner(),active={name:"run",uid:"new",stageIndex:1,stagesTotal:1}
  const writes:unknown[]=[]
  const modules={...primitives,react:runner.react,"@/lib/evaluation-active":activeHelpers,"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/evaluations/active"?{items:[active]}:{items:[]}}),useChillMutation:()=>({reset:()=>{},mutate:(input:unknown)=>writes.push(input)})}}
  const {EvaluationActive}=load("../features/evaluation-active.tsx",modules)
  let tree=elements(runner.render(EvaluationActive,{name:"run",uid:"old",now:0}))
  assert.ok(!tree.some(element=>element.type==="details"),"same-name active replacement must not display under the old selection")
  tree=elements(runner.render(EvaluationActive,{name:"run",uid:"new",now:0}))
  ;(tree.find(element=>element.type==="button"&&element.props.children==="실험 중지")!.props.onClick as ()=>void)()
  tree=elements(runner.render(EvaluationActive,{name:"run",uid:"old",now:0}))
  ;(tree.find(element=>element.type==="button"&&element.props.children==="중지 요청")!.props.onClick as ()=>void)()
  assert.equal(writes.length,0,"a pending stop cannot cross a changed selected UID")
})

test("selected detail passes the URL UID to the independent active query",()=>{
  const runner=hookRunner(),params=new URLSearchParams("name=run&uid=old")
  const run={name:"run",uid:"old",phase:"Running",spec:{}}
  const modules={...primitives,react:{...runner.react,useEffect:()=>{}},"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:()=>{}})},"@/lib/client":{useChillQuery:()=>({data:run})}}
  const {EvaluationRunPage}=load("../features/evaluation-request.tsx",modules)
  const active=elements(runner.render(EvaluationRunPage,{name:"run"})).find(element=>element.props.name==="run"&&"now" in element.props)!
  assert.equal(active.props.uid,"old")
})

test("R17: unsaved author edits install and remove a cancellable beforeunload guard",()=>{
  const events=new EventTarget(),previous=globalThis.window
  let cleanup:(()=>void)|undefined
  Object.defineProperty(globalThis,"window",{value:events,configurable:true,writable:true})
  try {
    const modules={...primitives,react:{useEffect:(effect:()=>undefined|(()=>void))=>{cleanup=effect()}}}
    const {useUnsavedAuthor}=load("../features/evaluation-author.tsx",modules)
    useUnsavedAuthor(true as unknown as Record<string,unknown>)
    const event=new Event("beforeunload",{cancelable:true})
    events.dispatchEvent(event)
    assert.equal(event.defaultPrevented,true)
    cleanup?.()
    const saved=new Event("beforeunload",{cancelable:true})
    events.dispatchEvent(saved)
    assert.equal(saved.defaultPrevented,false)
    useUnsavedAuthor(false as unknown as Record<string,unknown>)
    const pristine=new Event("beforeunload",{cancelable:true})
    events.dispatchEvent(pristine)
    assert.equal(pristine.defaultPrevented,false)
  } finally {Object.defineProperty(globalThis,"window",{value:previous,configurable:true,writable:true})}
})

test("R17: list rows do not expose rerun or removal until a run is selected",()=>{
  const runner=hookRunner(),run={name:"run",uid:"uid",phase:"Done",spec:{}}
  const modules={...primitives,react:{...runner.react,useEffect:()=>{}},"@/lib/client":{useChillQuery:()=>({data:{items:[run]}})}}
  const {EvaluationsPage}=load("../features/evaluation-request.tsx",modules)
  const tree=elements(runner.render(EvaluationsPage))
  assert.ok(!tree.some(element=>element.props.run===run))
  assert.ok(tree.some(element=>element.type==="a"&&element.props.href==="/experiments/runs/run?uid=uid"))
})

test("R17: load parts expose every service rate as labeled fields without a wide table",()=>{
  const runner=hookRunner(),source={name:"load",uid:"load-uid",spec:{parts:[{id:"zero",durationSeconds:1,rates:{camera:0,plc:0}}],workloads:[{name:"camera",uid:"camera-uid"},{name:"plc",uid:"plc-uid"}]},inUse:["original"]}
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/workloads?view=summary"?[]:source}),useChillMutation:()=>({isPending:false})},"@/features/evaluation-author":{AuthorSelect:"select",AuthorField:"input",AuthorJSON:"textarea",AuthorHelp:"help",EvidencePicker:"select",useUnsavedAuthor:()=>{}}}
  const {LoadScenarioAuthorPage}=load("../features/load-scenario-author.tsx",modules)
  const page=elements(runner.render(LoadScenarioAuthorPage,{name:"load"}))
  const editor=page.find(element=>element.props.editing===true)!
  const parent=elements(hookRunner().render(editor.type as (props:Record<string,unknown>)=>unknown,editor.props))
  const specEditor=parent.find(element=>element.props.spec&&typeof element.props.onValidityChange==="function")!
  const partRunner=hookRunner()
  modules.react.useState=partRunner.react.useState
  const form=elements(partRunner.render(specEditor.type as (props:Record<string,unknown>)=>unknown,specEditor.props))
  assert.ok(form.some(element=>element.type==="section"))
  assert.ok(form.some(element=>element.props.label==="camera (요청/s)"))
  assert.ok(form.some(element=>element.props.label==="plc (요청/s)"))
  assert.ok(!form.some(element=>element.type==="table"))
})

test("R17b: author help uses constraint rows and evidence options use server names",()=>{
  const runner=hookRunner(),input={kind:"seed",store:"internal-store",key:"experiments/private-seed.json",size:512,name:"실험 난수",set:"실험 묶음"}
  const {AuthorHelp,EvidencePicker}=load("../features/evaluation-author.tsx",{...primitives,react:{...runner.react,useId:()=>"inputs"},"@/lib/client":{useSession:()=>({config:{maxPageSize:200}}),useChillQuery:()=>({data:{items:[input],page:{offset:0,limit:25,total:1}}})},"@/components/ui/tooltip":{TooltipProvider:"div",Tooltip:"div",TooltipTrigger:"div",TooltipContent:"aside"}})
  const help=elements(runner.render(AuthorHelp))
  assert.ok(help.some(element=>element.type==="table"))
  const picker=elements(runner.render(EvidencePicker,{label:"난수",value:input,onChange:()=>{}}))
  const options=picker.filter(element=>element.type==="option")
  assert.ok(!JSON.stringify(options.map(option=>option.props.children)).includes("internal-store"))
  assert.ok(!JSON.stringify(options.map(option=>option.props.children)).includes("private-seed.json"))
  assert.ok(options.some(option=>option.props.children==="실험 난수"))
  assert.ok(picker.some(element=>element.type==="optgroup"&&element.props.label==="실험 묶음"))
  assert.ok(picker.some(element=>element.type==="details"))
})

test("R17b: evaluation writes disclose the required role without claiming an unverified permission",()=>{
  const {EvaluationPermission}=load("../features/evaluation-request.tsx",primitives)
  const unknown=elements(EvaluationPermission({}))
  assert.ok(unknown.some(element=>element.props.children==="평가 운영자"))
  assert.ok(unknown.some(element=>element.props.children==="미확인"))
  const denied=elements(EvaluationPermission({denied:true}))
  assert.ok(denied.some(element=>element.props.children==="권한 없음"))
  assert.ok(!denied.some(element=>element.props.children==="허용"))
})

test("R17b: result detail keeps outcome independent and labels every mobile comparison value",()=>{
  const runner=hookRunner(),params=new URLSearchParams("a=publication")
  const stage=(id:string)=>({id,from:"2026-11-01T00:00:00Z",to:"2026-11-01T00:01:00Z",kpis:{wholePoolJoules:60,requests:{camera:{offered:600,deadlineSuccessFraction:.9,completedLatencyNS:{p95:1000000}}}}})
  let run:Record<string,unknown>={id:"publication",stages:[],outcome:{execution:{status:"Failed"},measurement:{verdict:"NOT_EVALUATED"},cleanup:{status:"Complete"}}}
  const modules={...primitives,react:{...runner.react,useEffect:()=>{},useMemo:(fn:()=>unknown)=>fn(),useRef:()=>({current:null})},"lucide-react":{TriangleAlert:"svg"},recharts:{},"next/navigation":{useSearchParams:()=>params},"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:()=>({data:run})},"@/lib/experiments":experimentHelpers,"@/lib/measurements":measurementHelpers,"@/features/experiments-conclusion":{ExperimentOutcome:"outcome",useHistoricalExperiments:()=>({runs:[run],pending:false,error:null})}}
  const {ExperimentsPage}=load("../features/experiments.tsx",modules)
  const emptyPage=elements(runner.render(ExperimentsPage)),emptyDetail=emptyPage.find(element=>element.props.run===run&&typeof element.type==="function")!
  assert.ok(elements(runner.render(emptyDetail.type as (props:Record<string,unknown>)=>unknown,emptyDetail.props)).some(element=>element.type==="outcome"))
  run={...run,stages:[stage("candidate"),stage("baseline")],comparisons:[{left:{stageID:"candidate"},right:{stageID:"baseline"},reason:"저장된 비교"}]}
  const page=elements(runner.render(ExperimentsPage))
  const detail=page.find(element=>element.props.run===run&&typeof element.type==="function")!
  const tree=elements(runner.render(detail.type as (props:Record<string,unknown>)=>unknown,detail.props))
  const sections=tree.filter(element=>element.type==="section").map(element=>element.props["aria-label"])
  assert.deepEqual(sections.slice(0,3),["비교 맥락","차트와 구간 선택","선택 단계 수치"])
  const values=tree.find(element=>element.props.row&&element.props.onSelect&&typeof element.type==="function")!
  const cells=elements(runner.render(values.type as (props:Record<string,unknown>)=>unknown,values.props))
  const labels=cells.filter(element=>element.type==="td").map(element=>element.props["data-label"])
  for(const label of ["후보 원값","기준 원값","차이"])assert.ok(labels.includes(label),label)
})

test("R17b: template missing inputs stay visible while copied question text is folded as original",async()=>{
  const runner=hookRunner(),modules={...primitives,react:{...runner.react,useEffect:()=>{}},"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:()=>({data:{items:[],page:{offset:0,limit:25,total:0}},refetch:async()=>({data:{spec:{question:""},sources:{target:"none",harness:"latestPublication"},sourcePublication:{uid:"published-uid",name:"published-name"}}})})},"@/features/evaluation-request":{EvaluationPermission:"permission",EvaluationSubmit:"submit"}}
  const {EvaluationAuthor}=load("../features/evaluation-author.tsx",modules)
  let tree=elements(runner.render(EvaluationAuthor))
  await (tree.find(element=>element.type==="button"&&element.props.children==="빈 템플릿")!.props.onClick as ()=>Promise<void>)()
  await setImmediate()
  tree=elements(runner.render(EvaluationAuthor))
  assert.ok(tree.some(element=>element.type==="table"&&element.props["aria-label"]==="입력 필요 항목"))
  assert.ok(tree.some(element=>element.props.children==="대상 구성요소"))
  assert.ok(tree.some(element=>element.props.children==="최근 게시"))
  assert.ok(tree.some(element=>element.type==="dd"&&element.props.children==="published-uid"))
  assert.ok(tree.some(element=>element.type==="dd"&&element.props.children==="published-name"))
  const copied=hookRunner(),copyModules={...modules,react:{...copied.react,useEffect:()=>{}}}
  const CopyAuthor=load("../features/evaluation-author.tsx",copyModules).EvaluationAuthor
  const copyTree=elements(copied.render(CopyAuthor,{initialName:"source",initialSpec:{question:"Retained English question"}}))
  const original=copyTree.find(element=>element.type==="details"&&elements(element.props.children).some(child=>child.type==="summary"&&child.props.children==="질문 원문"))!
  assert.ok(original)
  assert.notEqual(original.props.open,true)
})

test("evidence picker filters and pages on the server and preserves off-page selections",()=>{
  const runner=hookRunner(),paths:string[]=[],changes:unknown[]=[]
  let maxPageSize=200
  const first={store:"s",key:"one",size:1,kind:"seed",set:"set-a",name:"서버 이름"}
  const second={store:"s",key:"two",size:2,kind:"seed",set:"set-b",name:""}
  const {EvidencePicker}=load("../features/evaluation-author.tsx",{...primitives,react:{...runner.react,useId:()=>"sets"},"@/lib/client":{useSession:()=>({config:{maxPageSize}}),useChillQuery:(path:string)=>{
    paths.push(path)
    const params=new URLSearchParams(path.split("?")[1])
    return {data:{items:params.get("offset")==="7"?[second]:[first],page:{offset:Number(params.get("offset")??0),limit:25,total:30,nextOffset:params.get("offset")==="7"?undefined:7}}}
  }}})
  const props={label:"난수",value:{store:"old",key:"saved",size:99},onChange:(value:unknown)=>changes.push(value)}
  let tree=elements(runner.render(EvidencePicker,props))
  assert.equal(new URLSearchParams(paths.at(-1)!.split("?")[1]).get("kind"),"seed")
  assert.equal(new URLSearchParams(paths.at(-1)!.split("?")[1]).get("limit"),"25")
  assert.ok(tree.some(item=>item.type==="option"&&item.props.children==="서버 이름"))
  assert.ok(tree.some(item=>item.type==="option"&&item.props.children==="saved"))
  ;(tree.find(item=>item.props.label==="난수")!.props.onChange as (value:string)=>void)(String(tree.find(item=>item.type==="option"&&item.props.children==="서버 이름")!.props.value))
  assert.deepEqual(changes.at(-1),{store:"s",key:"one",size:1})
  ;(tree.find(item=>item.type==="button"&&item.props.children==="다음")!.props.onClick as ()=>void)()
  tree=elements(runner.render(EvidencePicker,{...props,value:{store:"s",key:"one",size:900}}))
  assert.equal(new URLSearchParams(paths.at(-1)!.split("?")[1]).get("offset"),"7")
  assert.ok(tree.some(item=>item.type==="option"&&item.props.children==="서버 이름"))
  assert.ok(tree.some(item=>item.type==="option"&&item.props.children==="two"))
  assert.ok(tree.find(item=>item.type==="button"&&item.props.children==="다음")!.props.disabled)
  ;(tree.find(item=>item.type==="input"&&item.props.list==="sets")!.props.onChange as (event:unknown)=>void)({target:{value:"set-b"}})
  tree=elements(runner.render(EvidencePicker,props))
  const params=new URLSearchParams(paths.at(-1)!.split("?")[1])
  assert.equal(params.get("set"),"set-b")
  assert.equal(params.get("offset"),"0")
  ;(tree.find(item=>item.props.label==="입력 종류")!.props.onChange as (value:string)=>void)("payload")
  runner.render(EvidencePicker,props)
  assert.equal(new URLSearchParams(paths.at(-1)!.split("?")[1]).get("kind"),"payload")
  maxPageSize=10
  runner.render(EvidencePicker,props)
  assert.equal(new URLSearchParams(paths.at(-1)!.split("?")[1]).get("limit"),"10")
})

test("IA: only the selected stage is edited and dependent load creation preserves the draft",async()=>{
  const runner=hookRunner(),changes:author.Spec[]=[]
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:()=>({data:{items:[]},refetch:async()=>({})})},"@/features/load-scenario-author":{LoadEditor:"load-editor"}}
  const {EvaluationSpecEditor}=load("../features/evaluation-author.tsx",modules)
  const spec={question:"unsaved",future:9007199254740993n,stages:[{id:"a",loadScenarioRef:{future:true}},{id:"b"}]}
  const props={spec,onChange:(value:author.Spec)=>changes.push(value)}
  let tree=elements(runner.render(EvaluationSpecEditor,props))
  assert.equal(tree.filter(item=>item.props.label==="단계 ID").length,1)
  ;(tree.find(item=>item.type==="button"&&item.props.children==="새 부하")!.props.onClick as ()=>void)()
  tree=elements(runner.render(EvaluationSpecEditor,props))
  const editor=tree.find(item=>item.type==="load-editor")!
  await (editor.props.onConfirmed as (value:{name:string;uid:string})=>Promise<void>)({name:"created",uid:"actual-new"})
  assert.deepEqual(author.specValue(changes.at(-1)!,["stages",0,"loadScenarioRef"]),{future:true,name:"created",uid:"actual-new"})
  assert.equal(changes.at(-1)!.question,"unsaved")
  assert.equal(changes.at(-1)!.future,9007199254740993n)
  assert.deepEqual(author.specValue(changes.at(-1)!,["stages",1]),{id:"b"})
})

test("IA: historical references query pages and the selected detail without hiding missing references",()=>{
  const runner=hookRunner(),paths:string[]=[],changes:author.Spec[]=[]
  let missing=false
  const modules={...primitives,react:runner.react,"@/lib/experiments":experimentHelpers,"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:(path:string)=>{
    paths.push(path)
    return path.includes("?")?{data:{items:[{id:"visible"}],page:{nextOffset:5}}}:missing?{error:{status:404}}:{data:{stages:[{id:"saved-stage"},{id:"other"}]}}
  }}}
  const {HistoricalComparison}=load("../features/evaluation-author.tsx",modules)
  const props={spec:{comparisons:[{left:{historical:{publicationID:"off-page",stageID:"saved-stage"}}}]},path:["comparisons",0,"left","historical"],onChange:(value:author.Spec)=>changes.push(value)}
  let tree=elements(runner.render(HistoricalComparison,props))
  assert.ok(paths.includes("v1/experiments/off-page"))
  assert.ok(tree.some(item=>item.type==="option"&&item.props.value==="off-page"))
  ;(tree.find(item=>item.type==="button"&&item.props.children==="다음")!.props.onClick as ()=>void)()
  missing=true
  tree=elements(runner.render(HistoricalComparison,props))
  assert.ok(paths.includes("v1/experiments?offset=5&limit=5"))
  assert.ok(tree.some(item=>item.type==="option"&&item.props.value==="saved-stage"))
  assert.ok(tree.some(item=>item.props.children==="대상 게시 없음"))
  assert.equal(changes.length,0)
})

test("IA: shared LoadEditor returns the saved UID and rejects stale preflight after an edit",async()=>{
  const runner=hookRunner(),writes:string[]=[],confirmed:unknown[]=[]
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:()=>({}),useChillMutation:()=>({isPending:false,mutateAsync:async(input:{path:string;method:string})=>{
    writes.push(input.path)
    return {data:input.path.endsWith("/preflight")?{checks:[{id:"server",passed:true}]}:{uid:"actual-uid"}}
  }})},"@/features/evaluation-author":{useUnsavedAuthor:()=>{}}}
  const {LoadEditor}=load("../features/load-scenario-author.tsx",modules)
  const props={editing:false,onSaved:async()=>{},onConfirmed:(value:unknown)=>confirmed.push(value)}
  let tree=elements(runner.render(LoadEditor,props))
  ;(tree.find(item=>item.type==="input")!.props.onChange as (event:unknown)=>void)({target:{value:"created"}})
  tree=elements(runner.render(LoadEditor,props))
  ;(tree.find(item=>item.type==="button"&&item.props.children==="사전점검")!.props.onClick as ()=>void)()
  await setImmediate()
  tree=elements(runner.render(LoadEditor,props))
  assert.equal(tree.find(item=>item.type==="button"&&item.props.children==="생성")!.props.disabled,false)
  const specEditor=tree.find(item=>item.props.spec&&typeof item.props.onValidityChange==="function")!
  ;(specEditor.props.onChange as (value:author.Spec)=>void)({parts:[{id:"changed"}]})
  tree=elements(runner.render(LoadEditor,props))
  const stale=tree.find(item=>item.type==="button"&&item.props.children==="생성")!
  assert.equal(stale.props.disabled,true)
  ;(stale.props.onClick as ()=>void)()
  await setImmediate()
  assert.deepEqual(writes,["v1/evaluations/load-scenarios/preflight"])
  ;(tree.find(item=>item.type==="button"&&item.props.children==="사전점검")!.props.onClick as ()=>void)()
  await setImmediate()
  tree=elements(runner.render(LoadEditor,props))
  ;(tree.find(item=>item.type==="button"&&item.props.children==="생성")!.props.onClick as ()=>void)()
  await setImmediate()
  assert.deepEqual(confirmed,[{name:"created",uid:"actual-uid"}])
})

test("IA: experiment definition changes cannot reuse an earlier successful preflight",()=>{
  const runner=hookRunner(),writes:unknown[]=[]
  let mutationIndex=0
  const pre:{data?:{data:unknown};isPending:boolean;mutate:()=>void}={isPending:false,mutate:()=>{pre.data={data:{checks:[{id:"server",passed:true}],liveTarget:[]}}}}
  const send={isPending:false,mutateAsync:async(value:unknown)=>{writes.push(value);return {data:{name:"run",uid:"new"}}}}
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:()=>({}),useChillMutation:()=>mutationIndex++===0?pre:send}}
  const {EvaluationSubmit}=load("../features/evaluation-request.tsx",modules)
  const props={name:"run",spec:{question:"before"},onSpecChange:()=>{}}
  let tree=elements(runner.render(EvaluationSubmit,props))
  ;(tree.find(item=>item.type==="button"&&item.props.children==="사전 점검")!.props.onClick as ()=>void)()
  mutationIndex=0
  tree=elements(runner.render(EvaluationSubmit,props))
  assert.equal(tree.find(item=>item.type==="button"&&item.props.children==="실험 시작")!.props.disabled,false)
  mutationIndex=0
  tree=elements(runner.render(EvaluationSubmit,{...props,spec:{question:"after"}}))
  const start=tree.find(item=>item.type==="button"&&item.props.children==="실험 시작")!
  assert.equal(start.props.disabled,true)
  ;(start.props.onClick as ()=>void)()
  assert.equal(writes.length,0)
})

test("IA: a bookmarked replaced load UID requires explicit current identity selection",()=>{
  const runner=hookRunner(),item={name:"load",uid:"current",spec:{parts:[{id:"p"}]},inUse:[]}
  const modules={...primitives,react:runner.react,"next/navigation":{useSearchParams:()=>new URLSearchParams("name=load&uid=old")},"@/lib/client":{useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path==="v1/evaluations/load-scenarios"?{items:[item]}:item})}}
  const {LoadScenarioPage}=load("../features/load-scenario-author.tsx",modules)
  const tree=elements(runner.render(LoadScenarioPage))
  assert.ok(tree.some(element=>element.props.children==="설정 교체 확인"))
  assert.equal(tree.find(element=>element.type==="a"&&element.props.children==="현재 설정 선택")?.props.href,"/experiments/load-scenarios?name=load&uid=current")
  assert.ok(!tree.some(element=>element.props.spec===item.spec),"an old UID must not silently display its replacement definition")
})
