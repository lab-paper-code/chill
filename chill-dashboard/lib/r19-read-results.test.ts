import assert from "node:assert/strict"
import test from "node:test"
import {readFileSync} from "node:fs"
import {createRequire} from "node:module"
import {transpileModule,ModuleKind,JsxEmit} from "typescript"
import * as measurements from "./measurements.ts"
import * as coverage from "./measurement-coverage.ts"
import * as assessment from "./measurement-assessment.ts"
import * as liveDisplay from "./live-display.ts"
const require=createRequire(import.meta.url)
type E={type:unknown;props:Record<string,unknown>}
type Component=(props:Record<string,unknown>)=>unknown
function elements(n:unknown):E[]{if(Array.isArray(n))return n.flatMap(elements);if(!n||typeof n!=="object"||!("props" in n))return [];const e=n as E;return [e,...elements(e.props.children)]}
function text(n:unknown):string{if(Array.isArray(n))return n.map(text).join("");if(n==null||typeof n==="boolean")return "";if(typeof n!=="object")return String(n);const e=n as E;return typeof e.type==="function"?text((e.type as Component)(e.props)):text(e.props?.children)}
function load(file:string,modules:Record<string,unknown>,extra="") {const exports={} as Record<string,Component>;new Function("require","exports",transpileModule(readFileSync(new URL(file,import.meta.url),"utf8")+extra,{compilerOptions:{module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText)((name:string)=>modules[name]??(name.startsWith("@/")||name.endsWith(".css")?{}:require(name)),exports);return exports}
const base:Record<string,unknown>={react:{useState:(v:unknown)=>[v,()=>{}],useRef:()=>({current:null}),useEffect:()=>{}},"next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams(),useRouter:()=>({})},"@/lib/measurements":measurements,"@/lib/measurement-coverage":coverage,"@/lib/measurement-assessment":assessment,"@/lib/live-display":liveDisplay,"@/components/screen":{Screen:"main",Facts:"facts"},"@/components/ui/button":{Button:"button"},"@/components/measurement-navigation":{MeasurementStatus:"status",MeasurementNavigation:"nav"}}
base["./scheduler-workload-states"]=load("../features/scheduler-workload-states.tsx",base)
test("R19 retained idle material is not automatically requested as an active passport, including refresh",()=>{
 for(const [mode,evidence,enabled] of [["idle-model-loaded",{key:"retained"},false],["active",{key:"retained"},true],["active",undefined,false]] as const){
  const run={namespace:"ns",name:"run",uid:"uid",generation:1,spec:{measurement:{workloadMode:mode}},status:{evidence,conditions:[{type:"ExecutionComplete",status:"True",observedGeneration:1}]}}
  const calls:{path:string;options:{enabled?:boolean}}[]=[],refresh:string[]=[]
  const feature=load("../features/read-pages.tsx",{...base,"@/lib/client":{useSession:()=>({config:{namespace:"ns"}}),useChillQuery:(path:string,options:{enabled?:boolean})=>{calls.push({path,options});return {data:path.includes("evidence-passport")?undefined:{runs:[run]},refetch:()=>refresh.push(path)}}}})
  const tree=feature.MeasurementDetail({name:"run"})
  assert.equal(calls.find(c=>c.path.includes("evidence-passport"))?.options.enabled,enabled)
  const button=elements((tree as E).props.action).find(e=>e.props.children==="갱신")!;(button.props.onClick as ()=>void)()
  assert.equal(refresh.some(p=>p.includes("evidence-passport")),enabled)
  if(mode!=="active")assert.match(text(tree),/유휴 측정/)
 }
})
test("R19 limitation labels use server codes and retain raw reasons in tooltips",()=>{
 const issues=[{code:"ReplacementSelection",reason:"superseded by replacement; original evidence retained"},{code:"ReplacementSelection",reason:"operator replacement changes specification; selection retained as declared"},{code:"ProfileReuseRejected",reason:"reuse device class mismatch; independent target measurement required"}].map(issue=>({...issue,source:{Namespace:"ns",Name:"request"}}))
 const feature=load("../features/scheduler-profiles.tsx",{...base,"@/components/data-table":{DataTable:"table",useServerTable:()=>({query:new URLSearchParams()})},"@/components/read-region":{QueryGate:"gate"},"@/lib/client":{useChillQuery:()=>({data:{profiles:[],insufficiencies:issues}})}})
 const tree=feature.SchedulerProfilesPage({});assert.doesNotMatch(text(tree),/superseded|operator replacement|independent target/)
 for(const issue of issues)assert.ok(elements(tree).some(e=>e.props.title===issue.reason))
 assert.match(text(tree),/대체 측정 선택/);assert.match(text(tree),/재사용 불가/)
})
test("R19 uniform policy-unset coverage is one status rather than redundant fractions",()=>{
 const counts={cells:48,measuredCells:48,usedCells:48,noEvidenceCells:0,policyUnsetCells:48,unknownQualityCells:48,targetBatches:237,measuredBatches:237,failedCells:0,unsupportedCells:77}
 const feature=load("../features/measurement-coverage.tsx",{...base,"@tanstack/react-query":{},"@/lib/measurement-coverage":{...coverage,coverageScopeCounts:()=>counts,coverageQualityReasons:()=>[]}},"\nexport {CoverageCounts};")
 const tree=feature.CoverageCounts({cells:[]});assert.ok(!elements(tree).some(e=>e.type==="td"&&String(e.props["data-label"]).startsWith("정책 미설정")));assert.ok(!elements(tree).some(e=>e.type==="td"&&String(e.props["data-label"]).startsWith("품질 미확인")));assert.match(text(tree),/전체 정책 미설정/)
})
test("R19 power boundary reasons are Korean labels with original tooltip",()=>{
 const feature=load("../features/scheduler.tsx",{...base,"./power-series":{}},"\nexport {Lines};")
 const reason="sample boundary distance exceeds policy",tree=feature.Lines({values:[reason]})
 assert.equal(text(tree),"구간 경계 표본 부족");assert.ok(elements(tree).some(e=>e.props.title===reason))
})
test("R19 node-prefixed power boundary reason retains device attribution",()=>{
 const feature=load("../features/scheduler.tsx",{...base,"./power-series":{}},"\nexport {Lines};")
 const reason="Node rasp5 (node-uid): sample boundary distance exceeds policy",tree=feature.Lines({values:[reason]})
 assert.equal(text(tree),"구간 경계 표본 부족 · rasp5");assert.ok(elements(tree).some(e=>e.props.title===reason))
})
test("R19 null power values remain unavailable instead of being reconstructed",()=>{
 const row={node:{name:"rasp5"},joules:null,meanWatts:null,samples:12,reasons:["sample boundary distance exceeds policy"]}
 const feature=load("../features/scheduler.tsx",{...base,"./power-series":{PowerTimeline:"timeline"},"@/components/read-region":{QueryGate:"gate"},"@/components/data-table":{DataTable:"data-table"},"@/lib/client":{useChillQuery:()=>({data:{from:"2026-11-03T02:00:00Z",to:"2026-11-03T03:00:00Z",complete:false,nodes:[row],wholePoolJoules:null,wholePoolMeanWatts:null,activePoolJoules:null,activePoolMeanWatts:null}})}})
 const tree=feature.PowerInterval({from:"2026-11-03T02:00:00Z",to:"2026-11-03T03:00:00Z"})
 const columns=elements(tree).find(e=>e.type==="data-table")!.props.columns as {key:string;value:(row:unknown)=>unknown}[]
 assert.equal(columns.find(c=>c.key==="energy")!.value(row),"미제공");assert.equal(columns.find(c=>c.key==="power")!.value(row),"미제공")
 const facts=elements(tree).find(e=>e.type==="facts")!.props.items as [string,unknown][]
 for(const label of ["전체 실행 그룹 J","전체 평균 W","활성 실행 그룹 J","활성 평균 W"])assert.equal(facts.find(([key])=>key===label)?.[1],"미제공")
})
