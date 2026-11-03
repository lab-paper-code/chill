import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as coverage from "../lib/measurement-coverage.ts"
import * as measurements from "../lib/measurements.ts"

type Element={type:unknown;props:Record<string,unknown>}
type Component=(props:Record<string,unknown>)=>unknown
const require=createRequire(import.meta.url)
function elements(node:unknown):Element[] {
  if(Array.isArray(node))return node.flatMap(elements)
  if(!node||typeof node!=="object"||!("props" in node))return []
  const element=node as Element
  return [element,...elements(element.props.children)]
}
function text(node:unknown):string {
  if(Array.isArray(node))return node.map(text).join("")
  if(node==null||typeof node==="boolean")return ""
  if(typeof node!=="object")return String(node)
  const element=node as Element
  if(typeof element.type==="function")return text((element.type as Component)(element.props))
  return text(element.props?.children)
}
function load(file:string,modules:Record<string,unknown>):Record<string,Component> {
  const source=readFileSync(new URL(file,import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  new Function("require","exports",compiled)((name:string)=>Object.hasOwn(modules,name)?modules[name]:name==="@/hooks/use-measurement-data"?load("../hooks/use-measurement-data.ts",modules):name.startsWith("@/")?{}:require(name),exports)
  return exports
}
function harness(search:string) {
  const params=new URLSearchParams(search),replacements:string[]=[]
  const modules={
    "next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:(value:string)=>replacements.push(value)})},
    "@/components/ui/input":{Input:"input"},
    "@/lib/measurement-coverage":coverage,
    "@/lib/measurements":measurements,
  }
  return {modules,replacements}
}

test("editable execution path filters accept off-page identifiers and deduplicate suggestions",()=>{
  const h=harness("namespace=ns&path=off-page-path&node=device&page=3&offset=50&profile=selected")
  const filters=load("../components/measurement-filters.tsx",h.modules).MeasurementFilters
  const paths=["firstfloor-ort-cuda-arm64","firstfloor-rnn-ort-cuda-arm64","firstfloor-vlm-ort-cuda-arm64"]
  const axes=[{key:"path",label:"실행 경로",values:[...paths,paths[0],""]}]
  const tree=filters({editable:true,states:false,axes})
  const input=elements(tree).find(element=>element.type==="input"&&element.props.list==="measurement-path")!
  assert.ok(input)
  assert.equal(input.props.value,"off-page-path")
  assert.equal(elements(tree).some(element=>element.type==="select"),false)
  const suggestions=elements(elements(tree).find(element=>element.type==="datalist"))
    .filter(element=>element.type==="option")
  assert.deepEqual(suggestions.map(element=>element.props.value),paths)
  assert.equal(new Set(suggestions.map(text)).size,paths.length)
  ;(input.props.onChange as (event:{target:{value:string}})=>void)({target:{value:"another-page-path"}})
  const next=new URL(h.replacements[0],"https://dashboard.invalid").searchParams
  assert.equal(next.get("path"),"another-page-path")
  assert.equal(next.get("namespace"),"ns")
  assert.equal(next.get("node"),"device")
  for(const key of ["page","offset","profile"])assert.equal(next.has(key),false)
  const select=elements(filters({states:false,axes})).find(element=>element.type==="select")!
  const options=elements(select).filter(element=>element.type==="option")
  assert.equal(options.filter(element=>element.props.value===paths[0]).length,1)
  assert.equal(options.filter(element=>element.props.value==="off-page-path").length,1)
})
