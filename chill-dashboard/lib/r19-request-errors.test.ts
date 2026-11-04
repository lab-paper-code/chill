import assert from "node:assert/strict"
import test from "node:test"
import {readFileSync} from "node:fs"
import {createRequire} from "node:module"
import {createElement,type ComponentType,type ReactNode} from "react"
import {renderToStaticMarkup} from "react-dom/server"
import {transpileModule,ModuleKind,JsxEmit,ScriptTarget} from "typescript"
import * as measurements from "./measurements.ts"
import * as requests from "./measurement-request.ts"
import type {MeasurementAssessment,MeasurementFailure,MeasurementPlan} from "./read-types.ts"

const require=createRequire(import.meta.url)
const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
function load(file:string,modules:Record<string,unknown>) {
 const exports:Record<string,ComponentType<Record<string,unknown>>>={}
 const code=transpileModule(readFileSync(new URL(file,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
 new Function("require","exports",code)((name:string)=>name in modules?modules[name]:name.startsWith("@/")?{}:require(name),exports)
 return exports
}
const firstErrorTime="2026/11/02 02:14:41"
const firstErrorLine=`${firstErrorTime} ERROR serving worker stopped error="start runner: EOF"`
const failure=(code:string,label:string):MeasurementFailure=>({requestId:"ns/request/uid",runId:code,runNamespace:"ns",runName:code,batch:1,code,label,exitCode:3,message:`${label} 설명`,relation:"unresolved"})
const first={...failure("RuntimeFailed","실행 오류"),firstErrorTime,firstErrorLine}
const assessment:MeasurementAssessment={status:"failed",displayStatus:"problem",label:"실패",reasons:[{code:"CatalogUnavailable",message:"catalog unavailable",sourceIds:[]}],execution:null,evidence:{available:false,accepted:false,reasons:[]},checklist:{criteriaMet:null,version:"result-checklist.v1",checks:[]},scope:{schedulerUsed:false,reused:false,facts:[]},failureSummary:[first,failure("GPUMemoryExhausted","GPU 메모리 부족"),failure("GPUMemoryExhausted","GPU 메모리 부족"),failure("PowerObservationTimeout","전력 관측 시간 초과")]}
const plan={metadata:{name:"request",namespace:"ns",uid:"uid"},spec:{state:{targetNode:"node",candidate:{modelSpec:"model",deviceClass:"class",executionPath:"cpu",powerMode:"fixed"}},activeProtocol:{},idleProtocol:{}},status:{phase:"Failed"},assessment} as MeasurementPlan
function render(kind:"list"|"detail") {
 const modules:Record<string,unknown>={
  "next/link":{__esModule:true,default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},
  "next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({})},
  "@tanstack/react-query":{useQueryClient:()=>({})},
  "@/lib/client":{useSession:()=>({config:{namespace:"ns"}}),useChillMutation:()=>({}),useChillQuery:(path:string)=>({data:path.endsWith("/result")?{campaign:{...plan.metadata,spec:plan.spec,phase:"Failed"},assessment,result:null,resultRows:[]}:{items:[plan]}})},
  "@/lib/measurements":measurements,"@/lib/measurement-request":requests,"@/lib/measurement-assessment":{assessmentReasons:()=>[]},
  "@/components/measurement-filters":{MeasurementFilters:()=>null},"@/hooks/use-measurement-data":{useMeasurementPlans:()=>({data:{items:[plan]}}),useMeasurementCoverage:()=>({data:{cells:[]}})},
  "@/features/measurement-runs":{MeasurementChildRuns:()=>null},
  "@/components/read-region":{QueryGate:container,SourceJSON:()=>null,SourceExport:()=>null},
  "@/components/screen":{Screen:container,Facts:({items}:{items:[string,ReactNode][]})=>createElement("dl",null,items.map(([label,value])=>createElement("div",{key:label},createElement("dt",null,label),createElement("dd",null,value)))),Rows:container},"@/components/ui/button":{Button:container},
  "@/components/measurement-navigation":{MeasurementStatus:()=>null,MeasurementNavigation:()=>null,MeasurementEvidenceChecks:()=>null},
  "@/components/status-cell":{StatusCell:()=>null},
  "@/components/data-table":{DataTable:({columns,data}:{columns:{value:(row:unknown)=>ReactNode}[];data:unknown[]})=>createElement("div",null,data.map((row,index)=>createElement("div",{key:index},columns.map((column,index)=>createElement("div",{key:index},column.value(row))))))},
  "@/components/ui/dialog":Object.fromEntries(["Dialog","DialogContent","DialogDescription","DialogHeader","DialogTitle"].map(key=>[key,container])),
 }
 const details=load("../features/measurement-plans.tsx",modules)
 modules["@/features/measurement-plans"]={FirstMeasurementError:details.FirstMeasurementError,MeasurementPlanSheet:()=>null}
 const component=kind==="list"?load("../features/measurement-requests.tsx",modules).MeasurementRequestsPage:details.MeasurementPlanDetails
 return renderToStaticMarkup(createElement(component,{namespace:"ns",name:kind==="detail"?"request":undefined}))
}
for(const kind of ["list","detail"] as const)test(`R19 review: ${kind} renders the real first error line and recorded time`,()=>{
 const html=render(kind),visible=html.replace(/<[^>]+>/g,"")
 assert.match(html,/aria-label="첫 오류 원문"/)
 assert.match(visible,/start runner: EOF/)
 assert.equal(visible.split(firstErrorTime).length-1,1)
 assert.match(html,/title="2026\/11\/02 02:14:41 ERROR/)
})
test("R19 review: first error never hides different failure codes or catalog status",()=>{
 const visible=render("list").replace(/<[^>]+>/g,"")
 assert.equal(visible.split("GPU 메모리 부족").length-1,1)
 assert.match(visible,/전력 관측 시간 초과/)
 assert.match(visible,/카탈로그 조회 불가/)
 assert.doesNotMatch(visible,/실행 오류 설명|GPU 메모리 부족 설명|전력 관측 시간 초과 설명/)
})
