import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit } from "typescript"
import * as measurements from "./measurements.ts"
import * as costs from "./operation-cost.ts"
import type { OperationStep } from "./measurements.ts"

type Element={type:unknown;props:Record<string,unknown>}
type Component=(props:Record<string,unknown>)=>Element
type CostRow={operation:string}
type Column={key:string;value:(row:CostRow)=>unknown}
const require=createRequire(import.meta.url)
function nodes(node:unknown):Element[] {
  if(Array.isArray(node))return node.flatMap(nodes)
  if(!node||typeof node!=="object"||!("props" in node))return []
  const element=node as Element
  return [element,...nodes(element.props.children)]
}
function text(node:unknown):string {
  if(Array.isArray(node))return node.map(text).join("")
  if(node==null||typeof node==="boolean")return ""
  if(typeof node!=="object")return String(node)
  const element=node as Element
  return text(typeof element.type==="function"?(element.type as Component)(element.props):element.props?.children)
}
function load(file:string,names:string[]) {
  const source=readFileSync(new URL(file,import.meta.url),"utf8")+`\nexport {${names.join(",")}}`
  const compiled=transpileModule(source,{compilerOptions:{module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:Record<string,Component>={}
  const modules:Record<string,unknown>={
    "@/lib/measurements":measurements,"@/lib/operation-cost":costs,
    "@/components/data-table":{DataTable:"data-table"},
    "@/features/measurement-plans":{ResultRows:"result-rows"},
    "lucide-react":{TriangleAlert:"icon"},
  }
  new Function("require","exports",compiled)((name:string)=>modules[name]??(name.startsWith("@/")?{}:require(name)),exports)
  return exports
}
const profile=load("../features/scheduler-profiles.tsx",["ProfileValues","ProfileBatches"])
const projection=load("../features/measurement-projections.tsx",["OperationCostTable","OperationCostValues","OperationRanges"])
const step=(kind:"prepare"|"remove"|"power",results:OperationStep["results"]):OperationStep=>({name:kind,condition:{name:kind,kind},results})
const result=(durationNanos:number,wallJoules:number|null|undefined,extra:Partial<OperationStep["results"][number]>={})=>({name:"attempt",key:"raw-key",durationNanos,wallJoules,verdict:"Accepted",model:"model",...extra})

test("representative throughput uses first actual batch, preserves zero/null, and folds every actual row",()=>{
  const rows=[{id:"b2",batch:2,p95Milliseconds:0,totalJoulesPerItem:null},{id:"b8",batch:8,p95Milliseconds:19,totalJoulesPerItem:0}]
  assert.equal(text(profile.ProfileValues({rows,metric:"p95Milliseconds"})),"0.00")
  assert.equal(text(profile.ProfileValues({rows,metric:"totalJoulesPerItem"})),"미제공")
  const tree=profile.ProfileBatches({rows,batches:[]})
  assert.match(text(tree),/대표 배치.*2/)
  const folded=nodes(tree).find(node=>node.type==="details")!
  assert.notEqual(folded.props.open,true)
  assert.deepEqual(nodes(folded).find(node=>node.type==="result-rows")?.props.rows,rows)
  assert.match(text(profile.ProfileBatches({rows:[],batches:[{batch:0},{batch:4}]})),/0/)
})

test("cost primary list opts into URL and model absence is structural, not a missing-model fallback",()=>{
  const measurement={node:"device",metadata:{name:"cost"},conditions:{steps:[{name:"power",kind:"power"},{name:"prepare",kind:"prepare"},{name:"remove",kind:"remove"}]},status:{steps:[{...result(0,0,{model:undefined}),name:"power"},{...result(0,undefined,{model:undefined}),name:"prepare"},{...result(0,undefined,{model:undefined}),name:"remove"}]}}
  const table=projection.OperationCostTable({items:[measurement],select:(_:unknown,label:unknown)=>label})
  assert.equal(table.props.urlState,true)
  const column=(table.props.columns as Column[]).find(column=>column.key==="model")!
  const labels=Object.fromEntries((table.props.data as CostRow[]).map(row=>[row.operation,text(column.value(row))]))
  assert.equal(labels.power,"해당 없음")
  assert.equal(labels.prepare,"미기록")
  assert.equal(labels.remove,"미기록")
  const source=(table.props.columns as Column[]).find(column=>column.key==="source")!
  const verdicts=(table.props.data as CostRow[]).map(row=>text(source.value(row))).join("")
  assert.equal(verdicts.split("판정 불가 · 비교 표본 부족").length-1,1)
})

test("actual transition rows lift shared insufficiency once while retaining individual numeric values",()=>{
  const steps=[step("power",[result(0,0,{model:undefined})]),step("remove",[result(2e9,null,{model:undefined,reason:"different raw reason"})])]
  const tree=projection.OperationCostValues({rows:costs.operationTransitionCosts(steps),device:"device"})
  assert.equal(text(tree).split("판정 불가 · 비교 표본 부족").length-1,1)
  const rows=nodes(tree).filter(node=>node.type==="tr").slice(1)
  assert.match(text(rows[0]),/해당 없음/)
  assert.match(text(rows[1]),/미기록/)
  assert.match(text(rows[0]),/0\.0/)
  assert.match(text(rows[1]),/2\.0/)
})

test("range chart uses measured median and maximum grouped by operation and device, retaining numeric table",()=>{
  const steps=[step("prepare",[result(0,0),result(10e9,10),result(100e9,100)]),step("remove",[result(0,null)])]
  const tree=projection.OperationRanges({steps,device:"actual-device"})
  const ranges=nodes(tree).filter(node=>node.props.role==="img")
  assert.equal(ranges.length,3)
  assert.ok(ranges.some(node=>String(node.props["aria-label"]).includes("10.0")&&String(node.props["aria-label"]).includes("100.0")))
  assert.match(text(tree),/actual-device/)
  assert.match(text(tree),/모델 준비/)
  assert.match(text(tree),/모델 제거/)
  assert.ok(nodes(tree).some(node=>node.type==="table"))
  assert.ok(nodes(tree).filter(node=>node.type==="td").some(node=>text(node)==="미제공"))
  for(const range of ranges)for(const bar of nodes(range).filter(node=>node.props.style))assert.ok(!JSON.stringify(bar.props.style).includes("NaN"))
})
