import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as domain from "./domain.ts"
import * as measurementSummary from "./measurement-summary.ts"

type Tree={type:unknown;props:Record<string,unknown>}
const require=createRequire(import.meta.url)
function hooks() {
  let values:unknown[]=[];let cursor=0
  const states=new Map<unknown,unknown[]>()
  return {react:{useState(initial:unknown){const index=cursor++;if(!Object.hasOwn(values,index))values[index]=typeof initial==="function"?initial():initial;const state=values;return [state[index],(next:unknown)=>{state[index]=typeof next==="function"?next(state[index]):next}]}},render(Component:(props:Record<string,unknown>)=>unknown,props:Record<string,unknown>={}){cursor=0;if(!states.has(Component))states.set(Component,[]);values=states.get(Component)!;return Component(props)}}
}
function load(path:string,modules:Record<string,unknown>):Record<string,(props:Record<string,unknown>)=>unknown> {
  const compiled=transpileModule(readFileSync(new URL(path,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  new Function("require","exports",compiled)((name:string)=>Object.hasOwn(modules,name)?modules[name]:name==="@/lib/client-transport"?modules[name]=load("./client-transport.ts",{"@/lib/measurement-summary":measurementSummary}):name==="@/hooks/use-catalog-resource"?load("../hooks/use-catalog-resource.ts",modules):name.startsWith("@/")?{}:require(name),exports)
  return exports
}
const base={"next/navigation":{useSearchParams:()=>new URLSearchParams()},"@tanstack/react-query":{useQueryClient:()=>({invalidateQueries:async()=>{}})},"@/lib/domain":domain}

test("catalog update retains If-Match and original definition and never auto-retries",async()=>{
  for(const kind of ["models","preprocessors","datasets"]){
    const runner=hooks(),sent:unknown[]=[],current={metadata:{name:"registered",uid:"original",resourceVersion:"7"},spec:{future:"original"}}
    const {ResourceEditor}=load("../features/catalog-editor.tsx",{...base,react:runner.react,"@/lib/client":{useChillMutation:()=>({isPending:false,mutateAsync:async(input:unknown)=>{sent.push(input);return {status:200,body:"{}",data:current}}}),useChillQuery:(path:string)=>({data:path===`v1/${kind}/registered`?current:path==="v1/workloads?view=summary"?[]:{items:[]},refetch:async()=>({data:current})})}})
    runner.render(ResourceEditor,{kind,name:"registered"})
    const editor=runner.render(ResourceEditor,{kind,name:"registered"}) as Tree
    const state=editor.props.r as {source:unknown;submit:(method:string,body:unknown)=>Promise<void>}
    assert.equal(state.source,current)
    await state.submit("PUT",{spec:{future:"edited"}})
    assert.deepEqual(sent,[{path:`v1/${kind}/registered`,method:"PUT",body:{spec:{future:"edited"}},version:"7"}])
  }
})

