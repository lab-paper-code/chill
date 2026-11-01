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
  assert.equal(destructive.length,4)
  assert.ok(destructive.every(element=>element.props.disabled===true))
  assert.equal(validity.at(-1),false)
  const invalidControl=elements(tree).find(element=>Array.isArray(element.props.path)&&element.props.path.join("/")==="stages/0/control"&&typeof element.props.onValidityChange==="function")!
  ;(invalidControl.props.onValidityChange as (valid:boolean)=>void)(true)
  tree=runner.render(EvaluationSpecEditor,props)
  assert.ok(elements(tree).filter(element=>element.type==="button"&&element.props.children==="단계 삭제").every(element=>!element.props.disabled))
  assert.equal(validity.at(-1),true)
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
    assert.equal(navigation,"/experiments?name=original&uid=confirmed-new")
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
  const modules={...primitives,react:{...runner.react,useEffect:()=>{}},"next/navigation":{useSearchParams:()=>params},"@/lib/client":{useChillQuery:()=>({data:run})}}
  const {EvaluationsPage}=load("../features/evaluation-request.tsx",modules)
  const tree=elements(runner.render(EvaluationsPage))
  assert.ok(tree.some(element=>element.props.children==="교체됨"))
  assert.ok(!tree.some(element=>element.props.run===run),"replacement must not expose run mutations or release")
  assert.ok(!tree.some(element=>element.props.name==="run"&&"now" in element.props),"replacement must not expose active-run stop")
  const adopt=tree.find(element=>element.type==="a"&&element.props.children==="현재 실행 선택")!
  assert.equal(adopt.props.href,"/experiments?name=run&uid=new")
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
  const modules={...primitives,react:{...runner.react,useEffect:()=>{}},"next/navigation":{useSearchParams:()=>params},"@/lib/client":{useChillQuery:()=>({data:run})}}
  const {EvaluationsPage}=load("../features/evaluation-request.tsx",modules)
  const active=elements(runner.render(EvaluationsPage)).find(element=>element.props.name==="run"&&"now" in element.props)!
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
  assert.ok(tree.some(element=>element.type==="a"&&element.props.href==="/experiments?name=run&uid=uid"))
})

test("R17: load parts expose every service rate as labeled fields without a wide table",()=>{
  const runner=hookRunner(),source={name:"load",uid:"load-uid",spec:{parts:[{id:"zero",durationSeconds:1,rates:{camera:0,plc:0}}],workloads:[{name:"camera",uid:"camera-uid"},{name:"plc",uid:"plc-uid"}]},inUse:["original"]}
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/workloads"?[]:source}),useChillMutation:()=>({isPending:false})},"@/features/evaluation-author":{AuthorSelect:"select",AuthorField:"input",AuthorJSON:"textarea",AuthorHelp:"help",EvidencePicker:"select",useUnsavedAuthor:()=>{}}}
  const {LoadScenarioAuthorPage}=load("../features/load-scenario-author.tsx",modules)
  const page=elements(runner.render(LoadScenarioAuthorPage,{name:"load"}))
  const editor=page.find(element=>element.props.editing===true)!
  const parent=elements(hookRunner().render(editor.type as (props:Record<string,unknown>)=>unknown,editor.props))
  const specEditor=parent.find(element=>element.props.spec&&typeof element.props.onValidityChange==="function")!
  const partRunner=hookRunner()
  modules.react.useState=partRunner.react.useState
  const form=elements(partRunner.render(specEditor.type as (props:Record<string,unknown>)=>unknown,specEditor.props))
  assert.ok(form.some(element=>element.type==="fieldset"))
  assert.ok(form.some(element=>element.props.label==="camera (요청/s)"))
  assert.ok(form.some(element=>element.props.label==="plc (요청/s)"))
  assert.ok(!form.some(element=>element.type==="table"))
})
