import * as comparisonHelpers from "./experiment-comparison.ts"
import * as routeHelpers from "./dashboard-routes.ts"
import assert from "node:assert/strict"
import test from "node:test"
import { elements, hookRunner, load, type Tree } from "./evaluation-author-test-helpers.ts"
import { setImmediate } from "node:timers/promises"
import * as author from "./evaluation-author.ts"
import * as request from "./evaluation-request.ts"
import * as scheduler from "./scheduler.ts"
import * as activeHelpers from "./evaluation-active.ts"
import * as experimentHelpers from "./experiments.ts"
import * as measurementHelpers from "./measurements.ts"
import * as icons from "lucide-react"
import * as settings from "./scheduler-settings.ts"
import { createElement, type ComponentType } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import type { ReactNode, ReactElement } from "react"

const containers={Dialog:"dialog",DialogContent:"div",DialogHeader:"header",DialogTitle:"h2",DialogDescription:"p"}
const primitives={"next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({replace:()=>{}})},"@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{}})},"@/components/screen":{Screen:"main"},"@/components/read-region":{QueryGate:"div"},"@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},"@/components/ui/dialog":containers,"@/components/ui/sheet":{Sheet:"aside",SheetContent:"div",SheetHeader:"header",SheetTitle:"h2"},"@/lib/evaluation-author":author,"@/lib/evaluation-request":request,"@/lib/scheduler":scheduler}
Object.assign(primitives,{"@/lib/scheduler-settings":settings,"@/lib/experiments":experimentHelpers,"@/lib/experiment-comparison":comparisonHelpers})
test("R19: publication copy selector pages without losing its selected publication",()=>{
  const runner=hookRunner(),calls:string[]=[]
  const modules={...primitives,react:{...runner.react,useEffect:()=>{}}, "@/lib/experiment-comparison":comparisonHelpers,"@/lib/dashboard-routes":routeHelpers,"@/lib/experiments":experimentHelpers,"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:(path:string)=>{
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
    const modules={...primitives,"lucide-react":icons,react:{...runner.react,useEffect:()=>{}},"next/navigation":{useSearchParams:()=>params},"@/lib/experiment-comparison":comparisonHelpers,"@/lib/dashboard-routes":routeHelpers,"@/lib/experiments":experimentHelpers,"@/lib/measurements":measurementHelpers,"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:(path:string,options?:{enabled?:boolean})=>{
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
  const modules={...primitives,react:{...runner.react,useEffect:()=>{}},"@/lib/experiment-comparison":comparisonHelpers,"@/lib/dashboard-routes":routeHelpers,"@/lib/experiments":experimentHelpers,"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:()=>({data})},"@/features/evaluation-request":{}}
  const {EvaluationAuthor}=load("../features/evaluation-author.tsx",modules)
  const tree=elements(runner.render(EvaluationAuthor))
  const picker=tree.find(item=>item.props.label==="게시에서 복사")!
  assert.deepEqual(elements(picker).filter(item=>item.type==="option"&&item.props.value).map(item=>item.props.value),["first","second"])
  assert.ok(!tree.some(item=>item.type==="nav"))
})
test("publication copy and historical comparison render unreadable reports without accumulating pages",()=>{
  const unreadable=[{id:"unreadable-store",reason:"저장소 불일치"},{id:"unreadable-format",reason:"요약 형식 결함"}]
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  for(const historical of [false,true]) {
    const runner=hookRunner()
    let extra:Record<string,unknown>={unreadable}
    const modules={...primitives,react:{...runner.react,useEffect:()=>{}},
      "@/components/ui/tooltip":Object.fromEntries(["TooltipProvider","Tooltip","TooltipTrigger","TooltipContent"].map(name=>[name,container])),
      "@/features/evaluation-request":{EvaluationPermission:()=>null},
      "@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:(path:string)=>{
        const offset=Number(new URLSearchParams(path.split("?")[1]).get("offset")??0)
        return {data:path.startsWith("v1/experiments?")?experimentHelpers.decodeExperimentPage(JSON.stringify({items:[],...extra,page:{offset,limit:5,total:6,...(offset===0?{nextOffset:5}:{})}})):undefined}
      }}}
    const components=load("../features/evaluation-author.tsx",modules),Component=historical?components.HistoricalComparison:components.EvaluationAuthor
    const props=historical?{spec:{},path:["historical"],onChange:()=>{}}:{}
    const render=()=>runner.render(Component,props)
    for(const page of [0,1]) {
      const tree=render(),html=renderToStaticMarkup(tree as ReactElement)
      assert.match(html,/<summary[^>]*>[^<]*2[^<]*<\/summary>/)
      for(const {id,reason} of unreadable) {
        assert.equal(html.split(id).length-1,1)
        assert.ok(html.includes(reason))
      }
      if(page===0)(elements(tree).find(item=>item.type==="button"&&item.props.children==="다음")!.props.onClick as ()=>void)()
    }
    for(const next of [{unreadable:[]},{unreadable:null},{}]) {
      extra=next
      const html=renderToStaticMarkup(render() as ReactElement)
      for(const {id} of unreadable)assert.ok(!html.includes(id))
      assert.doesNotMatch(html,/<summary[^>]*>[^<]*2[^<]*<\/summary>/)
    }
  }
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
  const modules={...primitives,react:parent.react,"@/lib/client":client,"@/components/evaluation-author-fields":{AuthorSelect:"select",AuthorField:"input",AuthorJSON:"textarea",AuthorHelp:"help",EvidencePicker:"select",useUnsavedAuthor:()=>{}}}
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

test("invalid definition JSON keeps the editor on the original until repaired",()=>{
  const runner=hookRunner()
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:()=>({data:{items:[]}})},"@/features/evaluation-request":{}}
  const {EvaluationSpecEditor}=load("../features/evaluation-author.tsx",modules)
  const validity:boolean[]=[]
  const props={spec:{stages:[{id:"a",row:"a"},{id:"b",row:"b"}]},onChange:()=>{},onValidityChange:(valid:boolean)=>validity.push(valid)}
  let tree=runner.render(EvaluationSpecEditor,props)
  ;(elements(tree).find(element=>element.type==="button"&&element.props.children==="원문")!.props.onClick as ()=>void)()
  tree=runner.render(EvaluationSpecEditor,props)
  const json=elements(tree).find(element=>element.props.label==="실험 정의 원문")!
  ;(json.props.onValidityChange as (valid:boolean)=>void)(false)
  tree=runner.render(EvaluationSpecEditor,props)
  assert.ok(elements(tree).filter(element=>element.type==="button"&&["단계","비교","측정 기준"].includes(String(element.props.children))).every(element=>element.props.disabled))
  assert.equal(validity.at(-1),false)
  ;(elements(tree).find(element=>element.props.label==="실험 정의 원문")!.props.onValidityChange as (valid:boolean)=>void)(true)
  tree=runner.render(EvaluationSpecEditor,props)
  assert.ok(elements(tree).filter(element=>element.type==="button"&&element.props.children==="단계").every(element=>!element.props.disabled))
  assert.equal(validity.at(-1),true)
})

test("execution resources remain in the raw draft, not structured controls",()=>{
  const runner=hookRunner()
  const {EvaluationSpecEditor}=load("../features/evaluation-author.tsx",{...primitives,react:runner.react,"@/lib/client":{useChillQuery:()=>({data:{items:[]}})}})
  const props={spec:{loadGenerator:{resources:{requests:{memory:"256Mi"},limits:{memory:"512Mi"}}}},onChange:()=>{}}
  let tree=elements(runner.render(EvaluationSpecEditor,props))
  assert.ok(!tree.some(element=>Array.isArray(element.props.path)&&element.props.path[0]==="loadGenerator"))
  ;(tree.find(element=>element.type==="button"&&element.props.children==="원문")!.props.onClick as ()=>void)()
  tree=elements(runner.render(EvaluationSpecEditor,props))
  assert.deepEqual(tree.find(element=>element.props.label==="실험 정의 원문")?.props.value,props.spec)
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
    assert.ok(tree.some(element=>element.type==="div"&&Array.isArray(element.props.children)&&element.props.children[0]==="정리 실패"))
    assert.ok(cells.some(cell=>cell.props.children===label),`${status}: ${label}`)
  }
})

test("active progress preserves selected UID isolation without exposing duplicate stop controls",()=>{
  const runner=hookRunner(),active={name:"run",uid:"new",stageIndex:1,stagesTotal:1}
  const modules={...primitives,react:runner.react,"@/lib/evaluation-active":activeHelpers,"@/lib/client":{useChillQuery:()=>({data:{items:[active]}})}}
  const {EvaluationActiveDetails}=load("../features/evaluation-active.tsx",modules)
  let tree=elements(runner.render(EvaluationActiveDetails,{active:{data:{items:[active]}},name:"run",uid:"old",now:0}))
  assert.ok(!tree.some(element=>element.type==="details"),"same-name active replacement must not display under the old selection")
  tree=elements(runner.render(EvaluationActiveDetails,{active:{data:{items:[active]}},name:"run",uid:"new",now:0}))
  assert.ok(!tree.some(element=>element.type==="button"&&["실험 중지","중지 요청"].includes(String(element.props.children))))
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
    const {useUnsavedAuthor}=load("../components/evaluation-author-fields.tsx",modules)
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
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/workloads?view=summary"?[]:source}),useChillMutation:()=>({isPending:false})},"@/components/evaluation-author-fields":{AuthorSelect:"select",AuthorField:"input",AuthorJSON:"textarea",AuthorHelp:"help",EvidencePicker:"select",useUnsavedAuthor:()=>{}}}
  const {LoadScenarioAuthorPage}=load("../features/load-scenario-author.tsx",modules)
  const page=elements(runner.render(LoadScenarioAuthorPage,{name:"load"}))
  const editor=page.find(element=>element.props.editing===true)!
  const parent=elements(hookRunner().render(editor.type as (props:Record<string,unknown>)=>unknown,editor.props))
  const specEditor=parent.find(element=>element.props.spec&&typeof element.props.onValidityChange==="function")!
  const partRunner=hookRunner()
  modules.react.useState=partRunner.react.useState
  const form=elements(partRunner.render(specEditor.type as (props:Record<string,unknown>)=>unknown,specEditor.props))
  assert.ok(form.some(element=>element.type==="section"))
  assert.ok(form.some(element=>element.props.label==="camera (req/s)"))
  assert.ok(form.some(element=>element.props.label==="plc (req/s)"))
  assert.ok(!form.some(element=>element.type==="table"))
})

test("R17b: author help uses constraint rows and evidence options use server names",()=>{
  const runner=hookRunner(),input={kind:"trace",store:"internal-store",key:"experiments/private-trace.json",size:512,name:"요청 시각",set:"부하 설정"}
  const {AuthorHelp,EvidencePicker}=load("../components/evaluation-author-fields.tsx",{...primitives,react:{...runner.react,useId:()=>"inputs"},"@/lib/client":{useSession:()=>({config:{maxPageSize:200}}),useChillQuery:()=>({data:{items:[input],page:{offset:0,limit:25,total:1}}})},"@/components/ui/tooltip":{TooltipProvider:"div",Tooltip:"div",TooltipTrigger:"div",TooltipContent:"aside"}})
  const help=elements(runner.render(AuthorHelp))
  assert.ok(help.some(element=>element.type==="table"))
  const picker=elements(runner.render(EvidencePicker,{label:"요청 시각",value:input,onChange:()=>{}}))
  const options=picker.filter(element=>element.type==="option")
  assert.ok(!JSON.stringify(options.map(option=>option.props.children)).includes("internal-store"))
  assert.ok(!JSON.stringify(options.map(option=>option.props.children)).includes("private-trace.json"))
  assert.ok(options.some(option=>option.props.children==="요청 시각"))
  assert.ok(picker.some(element=>element.type==="optgroup"&&element.props.label==="부하 설정"))
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

test("R17b: result detail retains preparation outcome and separates candidate and baseline results",()=>{
  const runner=hookRunner(),params=new URLSearchParams("a=publication")
  const stage=(id:string)=>({id,from:"2026-11-01T00:00:00Z",to:"2026-11-01T00:01:00Z",kpis:{wholePoolJoules:id==="candidate"?60:100,requests:{camera:{offered:600,deadlineSuccessFraction:id==="candidate"?0.9:0.8,completedLatencyNS:{p95:id==="candidate"?1000000:2000000,samples:37}},sensor:{offered:100,deadlineSuccessFraction:0.5,completedLatencyNS:{p95:3000000,samples:8}}}}})
  let run:Record<string,unknown>={id:"publication",stages:[],outcome:{execution:{status:"Failed"},measurement:{verdict:"NOT_EVALUATED"},cleanup:{status:"Complete"}}}
  const modules={...primitives,react:{...runner.react,useEffect:()=>{},useMemo:(fn:()=>unknown)=>fn(),useRef:()=>({current:null})},"lucide-react":{TriangleAlert:"svg"},recharts:{},"next/navigation":{useSearchParams:()=>params},"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:()=>({data:run})},"@/lib/experiment-comparison":comparisonHelpers,"@/lib/dashboard-routes":routeHelpers,"@/lib/experiments":experimentHelpers,"@/lib/measurements":measurementHelpers,"@/features/experiments-conclusion":{ExperimentOutcome:"outcome",ExperimentVerdict:"verdict",ExperimentStageResults:"result-matrix",usePublishedExperimentFacts:()=>({data:null}),useHistoricalExperiments:()=>({runs:[run],pending:false,error:null})}}
  const {ExperimentsPage}=load("../features/experiments.tsx",modules)
  const emptyPage=elements(runner.render(ExperimentsPage)),emptyDetail=emptyPage.find(element=>element.props.run===run&&typeof element.type==="function")!
  assert.ok(elements(runner.render(emptyDetail.type as (props:Record<string,unknown>)=>unknown,emptyDetail.props)).some(element=>element.type==="outcome"))
  run={...run,stages:[stage("candidate"),stage("baseline")],comparisons:[{left:{stageID:"candidate"},right:{stageID:"baseline"},reason:"저장된 비교"}]}
  const page=elements(runner.render(ExperimentsPage))
  const detail=page.find(element=>element.props.run===run&&typeof element.type==="function")!
  const tree=elements(runner.render(detail.type as (props:Record<string,unknown>)=>unknown,detail.props))
  const sections=tree.filter(element=>element.type==="section").map(element=>element.props["aria-label"])
  assert.deepEqual(sections.slice(0,3),["게시 결과 요약","선택 단계 수치","차트와 구간 선택"])
  const values=tree.find(element=>element.props.row&&element.props.onSelect&&typeof element.type==="function")!
  const cells=elements(runner.render(values.type as (props:Record<string,unknown>)=>unknown,values.props))
  const conclusion=load("../features/experiments-conclusion.tsx",{...modules,"@/lib/client":{},"next/link":{default:"a"}})
  Object.assign(modules["@/features/experiments-conclusion"],conclusion)
  const rendered=renderToStaticMarkup(createElement(values.type as ComponentType,values.props))
  for(const value of ["서비스별 결과","90.00%","80.00%","에너지 상대 차이","-40.00 %","+10.00 %p","×0.50"])assert.ok(rendered.includes(value),value)
  const matrices=cells.filter(element=>element.props.stage&&typeof element.props.onSelect==="function")
  let selectedService=""
  const matrix=elements(runner.render(conclusion.ExperimentStageResults,{...matrices[0].props,onSelect:(name:string)=>{selectedService=name}}))
  const camera=matrix.find(element=>element.type==="button"&&[element.props.children].flat().join("")==="camera 결과 자세히")!
  ;(camera.props.onClick as ()=>void)()
  assert.equal(selectedService,"camera")
  const disclosure=tree.find(element=>element.type==="details"&&elements(element.props.children).some(child=>child.type==="summary"&&child.props.children==="실행·정리·비교 조건·정의 출처"))!
  assert.ok(disclosure)
  assert.notEqual(disclosure.props.open,true)
  const drawer=tree.find(element=>element.props.onServiceChange&&element.props.onClose&&typeof element.type==="function")!
  const recorded={...run,outcome:{stages:[{id:"candidate",injections:[{id:"result-only",action:"delete-serving-pod",outcome:"Executed",reason:"recorded",actualOffsetSeconds:4}]}]}}
  const drawerTree=elements(runner.render(drawer.type as (props:Record<string,unknown>)=>unknown,{...drawer.props,row:values.props.row,run:recorded}))
  const drawerValues=drawerTree.find(element=>element.props.row&&typeof element.type==="function")!
  const samples=renderToStaticMarkup(createElement(drawerValues.type as ComponentType<Record<string,unknown>>,{...drawerValues.props,service:"camera"}))
  assert.match(samples,/p95 표본[^<]*37/)
  assert.doesNotMatch(samples,/>sensor</)
  assert.ok(drawerTree.some(element=>element.type==="select"&&element.props.value===""))
  const resultRow=experimentHelpers.object(values.props.row)
  const owner={...experimentHelpers.object(resultRow.leftRun),outcome:recorded.outcome}
  const withInjection=elements(runner.render(drawer.type as (props:Record<string,unknown>)=>unknown,{...drawer.props,row:{...resultRow,leftRun:owner}}))
  const injection=withInjection.find(element=>element.type===conclusion.ExperimentInjections&&element.props.run===owner&&element.props.stage===resultRow.stage)
  assert.ok(injection,"recorded injection without a parsed definition must reach the rendered drawer")
  const injectionHTML=renderToStaticMarkup(createElement(conclusion.ExperimentInjections as ComponentType<Record<string,unknown>>,{run:owner,stage:resultRow.stage}))
  for(const value of ["result-only","실행","recorded","4"])assert.ok(injectionHTML.includes(value),value)
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
  const first={store:"s",key:"one",size:1,kind:"trace",set:"scenario-a",name:"서버 이름"}
  const second={store:"s",key:"two",size:2,kind:"trace",set:"scenario-b",name:""}
  const {EvidencePicker}=load("../components/evaluation-author-fields.tsx",{...primitives,react:{...runner.react,useId:()=>"sets"},"@/lib/client":{useSession:()=>({config:{maxPageSize}}),useChillQuery:(path:string)=>{
    paths.push(path)
    const params=new URLSearchParams(path.split("?")[1])
    return {data:{items:params.get("offset")==="7"?[second]:[first],page:{offset:Number(params.get("offset")??0),limit:25,total:30,nextOffset:params.get("offset")==="7"?undefined:7}}}
  }}})
  const props={label:"요청 시각",value:{store:"old",key:"saved",size:99},onChange:(value:unknown)=>changes.push(value)}
  let tree=elements(runner.render(EvidencePicker,props))
  assert.equal(new URLSearchParams(paths.at(-1)!.split("?")[1]).get("kind"),"trace")
  assert.equal(new URLSearchParams(paths.at(-1)!.split("?")[1]).get("limit"),"25")
  assert.ok(tree.some(item=>item.type==="option"&&item.props.children==="서버 이름"))
  assert.ok(tree.some(item=>item.type==="option"&&item.props.children==="saved"))
  ;(tree.find(item=>item.props.label==="요청 시각")!.props.onChange as (value:string)=>void)(String(tree.find(item=>item.type==="option"&&item.props.children==="서버 이름")!.props.value))
  assert.deepEqual(changes.at(-1),{store:"s",key:"one",size:1})
  ;(tree.find(item=>item.type==="button"&&item.props.children==="다음")!.props.onClick as ()=>void)()
  tree=elements(runner.render(EvidencePicker,{...props,value:{store:"s",key:"one",size:900}}))
  assert.equal(new URLSearchParams(paths.at(-1)!.split("?")[1]).get("offset"),"7")
  assert.ok(tree.some(item=>item.type==="option"&&item.props.children==="서버 이름"))
  assert.ok(tree.some(item=>item.type==="option"&&item.props.children==="two"))
  assert.ok(tree.find(item=>item.type==="button"&&item.props.children==="다음")!.props.disabled)
  ;(tree.find(item=>item.type==="input"&&item.props.list==="sets")!.props.onChange as (event:unknown)=>void)({target:{value:"scenario-b"}})
  tree=elements(runner.render(EvidencePicker,props))
  const params=new URLSearchParams(paths.at(-1)!.split("?")[1])
  assert.equal(params.get("set"),"scenario-b")
  assert.equal(params.get("offset"),"0")
  assert.equal(params.get("kind"),"trace")
  assert.ok(!tree.some(item=>item.props.label==="입력 종류"))
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
  const modules={...primitives,react:runner.react,"@/lib/experiment-comparison":comparisonHelpers,"@/lib/dashboard-routes":routeHelpers,"@/lib/experiments":experimentHelpers,"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:(path:string)=>{
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
  }})},"@/components/evaluation-author-fields":{useUnsavedAuthor:()=>{}}}
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
  const props={name:"run",spec:{question:"before"}}
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

test("IA review: load completion respects closed or replaced Sheet sessions and patches only the latest active draft",async()=>{
  for(const phase of ["create","reconcile","list","active","reopened"]) {
    const parent=hookRunner(),child=hookRunner()
    const {promise:pending,resolve:finish}=Promise.withResolvers<void>()
    const saved={name:"created",uid:"confirmed",spec:{workloads:[],parts:[],arrivalDistribution:{}}}
    const client={useChillQuery:()=>({data:{items:[]},refetch:async()=>{if(phase==="reconcile")await pending;return {data:saved}}}),useChillMutation:()=>({isPending:false,mutateAsync:async(input:{path:string})=>{
      if(input.path.endsWith("/preflight"))return {data:{checks:[{id:"valid",passed:true}]}}
      if(phase==="create")await pending
      return {data:phase==="reconcile"?{}:{uid:saved.uid}}
    }})}
    const {LoadEditor}=load("../features/load-scenario-author.tsx",{...primitives,react:child.react,"@/lib/client":client,"@/components/evaluation-author-fields":{useUnsavedAuthor:()=>{}}})
    const {EvaluationSpecEditor}=load("../features/evaluation-author.tsx",{...primitives,react:parent.react,"@/lib/client":{useChillQuery:()=>({data:{items:[]},refetch:async()=>{if(["list","active","reopened"].includes(phase))await pending}})},"@/features/load-scenario-author":{LoadEditor}})
    const props={spec:{question:"before",stages:[{id:"stage"}]} as author.Spec,onChange:(value:author.Spec)=>{props.spec=value}}
    let tree=elements(parent.render(EvaluationSpecEditor,props))
    ;(tree.find(element=>element.type==="button"&&element.props.children==="새 부하")!.props.onClick as ()=>void)()
    tree=elements(parent.render(EvaluationSpecEditor,props))
    const editor=tree.find(element=>element.type===LoadEditor)!
    let childTree=elements(child.render(LoadEditor,editor.props))
    ;(childTree.find(element=>element.type==="input")!.props.onChange as (event:unknown)=>void)({target:{value:"created"}})
    childTree=elements(child.render(LoadEditor,editor.props))
    ;(childTree.find(element=>element.type==="button"&&element.props.children==="사전점검")!.props.onClick as ()=>void)()
    await setImmediate()
    childTree=elements(child.render(LoadEditor,editor.props))
    ;(childTree.find(element=>element.type==="button"&&element.props.children==="생성")!.props.onClick as ()=>void)()
    await setImmediate()
    if(phase!=="active")(tree.find(element=>element.type==="aside")!.props.onOpenChange as (open:boolean)=>void)(false)
    props.spec={...props.spec,question:"edited while save was pending",stages:[{id:"later"}]}
    tree=elements(parent.render(EvaluationSpecEditor,props))
    if(phase==="reopened")(tree.find(element=>element.type==="button"&&element.props.children==="새 부하")!.props.onClick as ()=>void)()
    finish();await setImmediate();await setImmediate()
    assert.equal(props.spec.question,"edited while save was pending",phase)
    assert.deepEqual(props.spec.stages,[{id:"later",...(phase==="active"?{loadScenarioRef:{name:"created",uid:"confirmed"}}:{})}],phase)
  }
})

test("cutover stage assembly adds only id, row and the pinned load reference",()=>{
  const runner=hookRunner()
  const props={spec:{future:9007199254740993n,stages:[]} as author.Spec,onChange:(value:author.Spec)=>{props.spec=value}}
  const {EvaluationSpecEditor}=load("../features/evaluation-author.tsx",{...primitives,react:runner.react,"@/lib/client":{useChillQuery:()=>({data:{items:[{name:"load",uid:"load-uid"}]}})}})
  let tree=elements(runner.render(EvaluationSpecEditor,props))
  ;(tree.find(item=>item.props.children==="단계 추가")!.props.onClick as ()=>void)()
  tree=elements(runner.render(EvaluationSpecEditor,props))
  ;(tree.find(item=>item.props.label==="단계 ID")!.props.onChange as (value:author.Spec)=>void)(author.patchSpec(props.spec,["stages",0,"id"],"candidate"))
  tree=elements(runner.render(EvaluationSpecEditor,props))
  ;(tree.find(item=>item.props.label==="행 이름")!.props.onChange as (value:author.Spec)=>void)(author.patchSpec(props.spec,["stages",0,"row"],"후보"))
  tree=elements(runner.render(EvaluationSpecEditor,props))
  ;(tree.find(item=>item.props.label==="부하 설정")!.props.onChange as (uid:string)=>void)("load-uid")
  assert.deepEqual(props.spec,{future:9007199254740993n,stages:[{id:"candidate",row:"후보",loadScenarioRef:{name:"load",uid:"load-uid"}}]})
  tree=elements(runner.render(EvaluationSpecEditor,props))
  assert.ok(tree.some(item=>Array.isArray(item.props.path)&&item.props.path.join(".")==="stages.0.settingsPatch"))
  ;(tree.find(item=>item.props.children==="측정 기준")!.props.onClick as ()=>void)()
  tree=elements(runner.render(EvaluationSpecEditor,props))
  assert.ok(tree.some(item=>Array.isArray(item.props.path)&&item.props.path.join(".")==="settingsPatch"))
  assert.ok(tree.some(item=>Array.isArray(item.props.path)&&item.props.path.join(".")==="loadGenerator.nodeName"))
})

test("cutover load inputs serialize max uint64 seed as a string and pinned dataset name and UID",()=>{
  const runner=hookRunner(),fieldRunner=hookRunner()
  const fields=load("../components/evaluation-author-fields.tsx",{...primitives,react:fieldRunner.react})
  const modules={...primitives,react:runner.react,"@/lib/client":{useChillQuery:(path:string)=>({data:path==="v1/datasets"?{items:[{metadata:{name:"images",uid:"dataset-uid"}}]}:[]})},"@/components/evaluation-author-fields":fields,"@/features/evaluation-author":{EvidencePicker:"trace"}}
  const {LoadSpecEditor}=load("../features/load-scenario-author.tsx",modules)
  const props={spec:{workloads:[{name:"camera",uid:"workload",generation:1}],parts:[{id:"steady",durationSeconds:10,rates:{camera:7}}],arrivalDistribution:{camera:"poisson"},traceRefs:{camera:{store:"archive",key:"trace",size:9007199254740993n}},future:true} as author.Spec,onChange:(value:author.Spec)=>{props.spec=value},onValidityChange:()=>{}}
  let tree=elements(runner.render(LoadSpecEditor,props))
  assert.ok(tree.some(item=>item.props.label==="camera (req/s)"))
  ;(tree.find(item=>item.props.children==="입력 객체")!.props.onClick as ()=>void)()
  tree=elements(runner.render(LoadSpecEditor,props))
  const seed=tree.find(item=>item.props.label==="camera 난수 (uint64)")!
  const input=elements(fieldRunner.render(fields.AuthorField,seed.props)).find(item=>item.type==="input")!
  ;(input.props.onChange as (event:unknown)=>void)({target:{value:"18446744073709551615"}})
  tree=elements(runner.render(LoadSpecEditor,props))
  ;(tree.find(item=>item.props.label==="camera 데이터셋")!.props.onChange as (uid:string)=>void)("dataset-uid")
  const roundtrip=author.parseSpec(author.specJSON(props.spec))
  assert.deepEqual(roundtrip.seeds,{camera:"18446744073709551615"})
  assert.deepEqual(roundtrip.datasetRefs,{camera:{name:"images",uid:"dataset-uid"}})
  assert.ok(!tree.some(item=>item.props.label==="camera 데이터셋 이름"||item.props.label==="camera 데이터셋 UID"))
  assert.deepEqual(roundtrip.traceRefs,props.spec.traceRefs)
  assert.equal(roundtrip.future,true)
  assert.deepEqual(Object.keys(roundtrip).sort(),["arrivalDistribution","datasetRefs","future","parts","seeds","traceRefs","workloads"])
})


test("black-box navigation and header label autonomous operation separately from external evaluation",()=>{
  const sidebar=load("../components/app-sidebar.tsx",{"next/navigation":{usePathname:()=>"/experiments"},"next/link":{default:"a"},"lucide-react":icons,"@/components/ui/sidebar":{useSidebar:()=>({setOpenMobile:()=>{}}),Sidebar:"aside",SidebarContent:"div",SidebarHeader:"header",SidebarMenu:"ul",SidebarMenuButton:"button",SidebarMenuItem:"li"},"@/components/brand":{BrandMark:"svg"}})
  const text=elements(sidebar.AppSidebar({})).flatMap(element=>element.props.children).filter(value=>typeof value==="string")
  assert.ok(text.includes("CHILL 자율 제어"))
  assert.ok(text.includes("외부 평가"))
  let pathname="/experiments/new"
  const header=load("../components/site-header.tsx",{...primitives,"next/navigation":{usePathname:()=>pathname},"next-themes":{useTheme:()=>({resolvedTheme:"light",setTheme:()=>{}})},"lucide-react":icons,"@/components/ui/sidebar":{SidebarTrigger:"button"},"@/lib/client":{AccountControl:"account"},"@/features/evaluation-active":{EvaluationStatusCell:"status"}})
  assert.ok(elements(header.SiteHeader({})).some(element=>element.props.children==="외부 평가"))
  pathname="/"
  assert.ok(elements(header.SiteHeader({})).some(element=>element.props.children==="CHILL 자율 제어"))
})

test("cutover load preflight and creation send the same complete external load contract",async()=>{
  const runner=hookRunner(),writes:{path:string;method:string;body:string}[]=[]
  const spec={workloads:[{name:"camera",uid:"workload-uid",generation:9007199254740993n}],parts:[{id:"steady",durationSeconds:10,rates:{camera:7}}],arrivalDistribution:{camera:"poisson"},seeds:{camera:"18446744073709551615"},datasetRefs:{camera:{name:"images",uid:"dataset-uid"}},traceRefs:{camera:{store:"archive",key:"trace",size:9007199254740993n}}}
  const {LoadEditor}=load("../features/load-scenario-author.tsx",{...primitives,react:runner.react,"@/components/evaluation-author-fields":{useUnsavedAuthor:()=>{}},"@/lib/client":{useChillQuery:()=>({}),useChillMutation:()=>({isPending:false,mutateAsync:async(input:{path:string;method:string;body:string})=>{writes.push(input);return {data:input.path.endsWith("/preflight")?{checks:[{id:"spec",passed:true}]}:{uid:"created-uid"}}}})}})
  const props={source:{name:"source",uid:"source-uid",spec,inUse:[]},editing:false,onSaved:async()=>{}}
  let tree=elements(runner.render(LoadEditor,props))
  ;(tree.find(item=>item.type==="input")!.props.onChange as (event:unknown)=>void)({target:{value:"new-load"}})
  tree=elements(runner.render(LoadEditor,props))
  ;(tree.find(item=>item.props.children==="사전점검")!.props.onClick as ()=>void)()
  await setImmediate()
  tree=elements(runner.render(LoadEditor,props))
  ;(tree.find(item=>item.props.children==="생성")!.props.onClick as ()=>void)()
  await setImmediate()
  assert.deepEqual(writes.map(item=>[item.path,item.method]),[["v1/evaluations/load-scenarios/preflight","POST"],["v1/evaluations/load-scenarios","POST"]])
  for(const write of writes)assert.deepEqual(author.parseSpec(write.body),{name:"new-load",spec})
})

test("P2 author: deleting the selected stage cannot apply its buffered patch to the next stage",()=>{
  const parent=hookRunner()
  const modules={...primitives,react:{...parent.react},"@/lib/client":{useChillQuery:()=>({data:{items:[]}})}}
  const {SettingsPatchEditor}=load("../features/settings-patch-editor.tsx",modules)
  const {EvaluationSpecEditor}=load("../features/evaluation-author.tsx",{...modules,"@/features/settings-patch-editor":{SettingsPatchEditor}})
  const props={spec:{stages:[{id:"a",settingsPatch:{maxReplicas:4}},{id:"b",settingsPatch:{maxReplicas:9}}]} as author.Spec,onChange:(value:author.Spec)=>{props.spec=value}}
  let child=hookRunner(),identity:unknown
  function renderPatch(tree:Tree[]) {
    const section=tree.find(item=>item.type==="section"&&elements(item).some(item=>item.type===SettingsPatchEditor))!
    const patch=elements(section).find(item=>item.type===SettingsPatchEditor)!
    const key=`${(section as Tree&{key?:string}).key}/${(patch as Tree&{key?:string}).key}`
    if(identity!==key){child=hookRunner();identity=key}
    modules.react.useState=child.react.useState
    const rendered=elements(child.render(SettingsPatchEditor,patch.props))
    modules.react.useState=parent.react.useState
    return rendered
  }
  let tree=elements(parent.render(EvaluationSpecEditor,props)),patch=renderPatch(tree)
  ;(patch.find(item=>item.props.label==="설정 항목")!.props.onChange as (key:string)=>void)("maxReplicas")
  patch=renderPatch(tree)
  assert.ok(patch.some(item=>item.props.children==="항목 반영"))
  ;(tree.find(item=>item.props.children==="단계 삭제")!.props.onClick as ()=>void)()
  tree=elements(parent.render(EvaluationSpecEditor,props));patch=renderPatch(tree)
  const apply=patch.find(item=>item.props.children==="항목 반영")
  if(apply)(apply.props.onClick as ()=>void)()
  assert.deepEqual(author.specValue(props.spec,["stages",0]),{id:"b",settingsPatch:{maxReplicas:9}})
  assert.equal(patch.find(item=>item.props.label==="설정 항목")?.props.value,"")
})


test("P2 author: copied legacy run preflight excludes deleted fields and visibly lists exclusions while edit preserves them",()=>{
  const legacy={question:"legacy",control:{old:true},target:{components:[{name:"server",image:"old",sourceSHA:"sha",resource:{name:"server"},future:true}]},harness:{image:"retained",sourceSHA:"retained"},stages:[{id:"a",mode:"fixed",startState:{old:true},control:{old:true},settingsPatch:{controls:{power:false}},future:true}],future:9007199254740993n}
  for(const [fromPublication,existing] of [[false,undefined],[false,{name:"old",uid:"old-uid"}],[true,undefined]] as const) {
    const runner=hookRunner(),writes:{body:string}[]=[]
    const {EvaluationAuthor}=load("../features/evaluation-author.tsx",{...primitives,react:{...runner.react,useEffect:()=>{}},"@/features/evaluation-request":{EvaluationSubmit:"submit"},"@/lib/client":{useSession:()=>({config:{maxPageSize:25}}),useChillQuery:(path:string)=>({data:path.startsWith("v1/experiments?")?{items:[]}:path==="v1/experiments/publication"?{definition:{run:{name:"old"},spec:legacy}}:undefined})}})
    const props={...(fromPublication?{}:{initialSpec:legacy,initialName:"old"}),existing}
    let tree=elements(runner.render(EvaluationAuthor,props))
    if(fromPublication) {
      ;(tree.find(item=>item.props.label==="게시에서 복사")!.props.onChange as (value:string)=>void)("publication")
      tree=elements(runner.render(EvaluationAuthor,props))
      ;(tree.find(item=>item.props.children==="게시 복사")!.props.onClick as ()=>void)()
      tree=elements(runner.render(EvaluationAuthor,props))
    }
    if(!existing) {
      ;(tree.find(item=>item.type==="input")!.props.onChange as (event:unknown)=>void)({target:{value:"copy"}})
      tree=elements(runner.render(EvaluationAuthor,props))
    }
    const submit=tree.find(item=>item.type==="submit")!
    const submitRunner=hookRunner()
    const {EvaluationSubmit}=load("../features/evaluation-request.tsx",{...primitives,react:submitRunner.react,"@/lib/client":{useChillQuery:()=>({}),useChillMutation:()=>({isPending:false,mutate:(value:{body:string})=>writes.push(value)})}})
    tree=elements(submitRunner.render(EvaluationSubmit,submit.props))
    ;(tree.find(item=>item.props.children==="사전 점검")!.props.onClick as ()=>void)()
    assert.equal(writes.length,1,"actual enabled EvaluationSubmit preflight must send exactly one request")
    const body=author.parseSpec(writes[0].body),spec=author.object(body.spec)
    assert.equal(Object.hasOwn(spec,"control"),!!existing)
    assert.equal(Object.hasOwn(author.object(author.specValue(spec,["stages",0])),"mode"),!!existing)
    assert.equal(Object.hasOwn(author.object(author.specValue(spec,["stages",0])),"startState"),!!existing)
    assert.equal(Object.hasOwn(author.object(author.specValue(spec,["stages",0])),"control"),!!existing)
    assert.equal(Object.hasOwn(author.object(author.specValue(spec,["target","components",0])),"image"),!!existing)
    assert.equal(Object.hasOwn(author.object(author.specValue(spec,["target","components",0])),"sourceSHA"),!!existing)
    assert.deepEqual(spec.harness,legacy.harness)
    assert.equal(spec.future,9007199254740993n)
    const parentTree=elements(runner.render(EvaluationAuthor,props))
    assert.equal(parentTree.some(item=>item.props.children==="복사에서 제외됨"),!existing)
    if(!existing)for(const path of ["control","stages[0].mode","stages[0].startState","stages[0].control","target.components[0].image","target.components[0].sourceSHA"])assert.ok(parentTree.some(item=>item.props.children===path),path)
  }
  assert.ok(Object.hasOwn(legacy,"control"))
})

test("P2 author: copied legacy scenario preflight strips only obsolete load fields and preserves exact seeds",async()=>{
  const legacy={workloads:[{name:"camera",uid:"w",generation:1}],parts:[{id:"p",durationSeconds:1,rates:{camera:1},alpha:{camera:1},future:true}],alpha:{camera:1},kappaRef:{key:"old"},rateBasisRef:{key:"old"},seedRef:{key:"old"},payloadManifestRef:{key:"old"},seeds:{camera:"18446744073709551615"},datasetRefs:{camera:{name:"images",uid:"dataset"}},future:9007199254740993n}
  for(const editing of [false,true]) {
    const runner=hookRunner(),writes:{body:string}[]=[]
    const {LoadEditor}=load("../features/load-scenario-author.tsx",{...primitives,react:runner.react,"@/components/evaluation-author-fields":{useUnsavedAuthor:()=>{}},"@/lib/client":{useChillQuery:()=>({}),useChillMutation:()=>({isPending:false,mutateAsync:async(input:{body:string})=>{writes.push(input);return {data:{checks:[{id:"spec",passed:true}]}}}})}})
    const props={source:{name:"old",uid:"uid",spec:legacy,inUse:[]},editing,onSaved:async()=>{}}
    let tree=elements(runner.render(LoadEditor,props))
    if(!editing){(tree.find(item=>item.type==="input")!.props.onChange as (event:unknown)=>void)({target:{value:"copy"}});tree=elements(runner.render(LoadEditor,props))}
    assert.equal(tree.some(item=>item.props.children==="복사에서 제외됨"),!editing)
    ;(tree.find(item=>item.props.children==="사전점검")!.props.onClick as ()=>void)()
    await setImmediate()
    const spec=author.object(author.parseSpec(writes[0].body).spec)
    for(const key of ["alpha","kappaRef","rateBasisRef","seedRef","payloadManifestRef"])assert.equal(Object.hasOwn(spec,key),editing,key)
    assert.equal(Object.hasOwn(author.object(author.specValue(spec,["parts",0])),"alpha"),editing)
    assert.deepEqual(spec.seeds,{camera:"18446744073709551615"})
    assert.equal(spec.future,9007199254740993n)
    assert.equal(author.specValue(spec,["parts",0,"future"]),true)
    if(!editing)for(const path of ["parts[0].alpha","alpha","kappaRef","rateBasisRef","seedRef","payloadManifestRef"])assert.ok(tree.some(item=>item.props.children===path),path)
  }
  assert.ok(Object.hasOwn(legacy,"seedRef"))
})
