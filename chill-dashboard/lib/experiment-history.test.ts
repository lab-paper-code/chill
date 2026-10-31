import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as helpers from "./experiments.ts"
import * as measurements from "./measurements.ts"
import type { Experiment } from "./experiments.ts"
import type * as ClientModule from "./client.tsx"
import type * as ConclusionModule from "../features/experiments-conclusion.tsx"
import type * as ExperimentsModule from "../features/experiments.tsx"
import type * as OverviewModule from "../features/overview.tsx"

const require=createRequire(import.meta.url)
function load<T>(path:string,modules:Record<string,unknown>):T {
  const source=readFileSync(new URL(path,import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:Record<string,unknown>={}
  new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:name.startsWith("@/")||name==="next/navigation"?{}:name.endsWith(".css")?{}:require(name),exports)
  return exports as T
}
const {ApiError}=load<typeof ClientModule>("./client.tsx",{"lucide-react":{}})
const fixture=JSON.parse(readFileSync(new URL("./fixtures/experiment-213.json",import.meta.url),"utf8")).publication as Experiment
const run:Experiment={...fixture,id:"candidate",comparisons:[{left:{stageID:fixture.stages[0].id},right:{historical:{publicationID:"baseline",stageID:"baseline-stage"}},reason:"선언된 과거 비교"}]}

test("historical failures stay errors across list, detail and overview; retry refetches only failed references",()=>{
  let queries:Array<{error:Error|null;data?:Experiment;isPending:boolean;refetch?:()=>void}>=[],params=new URLSearchParams(),retried:number[]=[],buttons:Array<()=>void>=[]
  const container=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const Button=({children,onClick,...props}:{children?:ReactNode;onClick?:()=>void})=>{if(onClick)buttons.push(onClick);return createElement("button",props,children)}
  const client={ApiError,useChillQueries:()=>queries,useSession:()=>({config:{namespace:"test"}}),useChillQuery:(path:string)=>({data:path==="v1/experiments"?{items:[run]}:run,error:null,isFetching:false,refetch:()=>{}})}
  const modules:Record<string,unknown>={
    "@/lib/client":client,"@/lib/experiments":helpers,"@/lib/measurements":measurements,
    "next/link":{default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},"next/navigation":{useSearchParams:()=>params},
    "lucide-react":{TriangleAlert:container},"recharts":{},
    "@/components/screen":{Screen:container,Empty:container},"@/components/read-region":{QueryGate:container,SourceJSON:container,SourceExport:container},
    "@/components/ui/button":{Button},"@/components/ui/sheet":{Sheet:container,SheetContent:container,SheetHeader:container,SheetTitle:container},
    "@/features/scheduler-evidence":{},"@/features/measurement-coverage":{useMeasurementCoverage:()=>({data:undefined,refetch:()=>{}}),useCoverageInventory:()=>({})},
    "@/lib/overview":{overviewCoverageMatrix:()=>({rows:[],devices:[]})},"@/lib/measurement-coverage":{},
  }
  const conclusion=load<typeof ConclusionModule>("../features/experiments-conclusion.tsx",modules)
  modules["@/features/experiments-conclusion"]=conclusion
  const {ExperimentsPage}=load<typeof ExperimentsModule>("../features/experiments.tsx",modules),{OverviewPage}=load<typeof OverviewModule>("../features/overview.tsx",modules)
  for(const error of [new ApiError(500,"",null),new ApiError(403,"",null),new Error("request timed out")]) {
    retried=[]
    queries=[{error,data:undefined,isPending:false,refetch:()=>{retried.push(0)}},{error:new ApiError(404,"",null),isPending:false,refetch:()=>{retried.push(1)}},{error:null,isPending:false,refetch:()=>{retried.push(2)}}]
    const historical=conclusion.useHistoricalExperiments([run])
    assert.equal(historical.error,error,`${error.message} must be returned, not treated as a missing publication`)
    historical.refetch()
    assert.deepEqual(retried,[0])
    for(const [Page,search] of [[ExperimentsPage,""],[ExperimentsPage,"a=candidate"],[OverviewPage,""]] as const) {
      params=new URLSearchParams(search);buttons=[];retried=[]
      const html=renderToStaticMarkup(createElement(Page))
      assert.match(html,/role="alert"/)
      assert.match(html,/대상 게시 조회 실패/)
      assert.doesNotMatch(html,/대상 게시 없음/)
      assert.doesNotMatch(html,/<a[^>]*>(?:(?!<\/a>)[\s\S])*<button/)
      assert.ok(buttons.length,"error UI has a retry action")
      buttons[0]()
      assert.deepEqual(retried,[0])
    }
  }
  for(const query of [{error:new ApiError(404,"",null),isPending:false},{error:null,data:{...fixture,id:"baseline"},isPending:false}]) {
    queries=[query]
    const historical=conclusion.useHistoricalExperiments([run])
    assert.equal(historical.error,null)
    assert.equal(historical.pending,false)
    assert.match(renderToStaticMarkup(createElement(conclusion.ExperimentJudgment,{run,runs:historical.runs})),/대상 게시 없음/)
  }
})
