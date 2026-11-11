import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as measurementSummary from "./measurement-summary.ts"
export type Tree={type:unknown;props:Record<string,unknown>}
const require=createRequire(import.meta.url)
export function elements(node:unknown):Tree[] {
  if(Array.isArray(node))return node.flatMap(elements)
  if(!node||typeof node!=="object"||!("props" in node))return []
  const element=node as Tree
  return [element,...elements(element.props.children)]
}
export function hookRunner() {
  const values:unknown[]=[]
  let cursor=0
  return {
    react:{useState(initial:unknown) {
      const index=cursor++
      if(!Object.hasOwn(values,index))values[index]=typeof initial==="function"?initial():initial
      return [values[index],(next:unknown)=>{values[index]=typeof next==="function"?next(values[index]):next}]
    },
    useRef(initial:unknown) {
      const index=cursor++
      if(!Object.hasOwn(values,index))values[index]={current:initial}
      return values[index]
    },
    useEffect(effect:()=>void) {effect()}},
    render(Component:(props:Record<string,unknown>)=>unknown,props:Record<string,unknown>={}) {cursor=0;return Component(props)},
  }
}
export function load(path:string,modules:Record<string,unknown>):Record<string,(props:Record<string,unknown>)=>unknown> {
  const source=readFileSync(new URL(path,import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports={}
  const extracted:Record<string,string>={"@/components/evaluation-author-fields":"../components/evaluation-author-fields.tsx","@/features/settings-patch-editor":"../features/settings-patch-editor.tsx","@/components/unreadable-experiments":"../components/unreadable-experiments.tsx"}
  new Function("require","exports",compiled)((name:string)=>Object.hasOwn(modules,name)?modules[name]:name==="@/lib/client-transport"?modules[name]=load("./client-transport.ts",{"@/lib/measurement-summary":measurementSummary}):Object.hasOwn(extracted,name)?load(extracted[name],modules):name.startsWith("@/")?{}:require(name),exports)
  return exports
}
