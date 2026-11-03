import assert from "node:assert/strict"
import test from "node:test"
import { setImmediate as nextTurn } from "node:timers/promises"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import * as React from "react"
import * as icons from "lucide-react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as live from "./live.ts"
import * as history from "./live-history.ts"
import type * as Client from "./client.tsx"

const require=createRequire(import.meta.url)
function load<T extends object>(file:string,modules:Record<string,unknown>):T {
  const exports={} as T
  const code=transpileModule(readFileSync(new URL(file,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",code)((name:string)=>modules[name]??require(name),exports)
  return exports
}
const client=load<typeof Client>("./client.tsx",{"lucide-react":icons,"next/navigation":{},"@/components/ui/button":{},"@/components/ui/input":{},"@/components/ui/dialog":{},"@/components/brand":{},"@/lib/scheduler":{},"@/lib/session-token":{},"@/lib/measurement-summary":{}})

for(const outcome of ["recover","unmount","session"] as const)test(`provider event seed handles CoverageWarming and ${outcome}`,async t=>{
  t.mock.timers.enable({apis:["setTimeout"]})
  let state=live.initialLiveState()
  const effects:(()=>void|(()=>void))[]=[],signals=new Set<AbortController>(),requests:{path:string;signal:AbortSignal}[]=[]
  const queryClient={getQueryData:()=>state,setQueryData:(_key:unknown,change:(value:live.LiveState)=>live.LiveState)=>{state=change(state)}}
  const modules={
    react:{...React,useEffect:(effect:()=>void|(()=>void))=>{effects.push(effect)}},
    "@tanstack/react-query":{skipToken:Symbol(),useQuery:()=>({data:state}),useQueryClient:()=>queryClient},
    "next/navigation":{useSearchParams:()=>new URLSearchParams(),usePathname:()=>"/"},
    "@/lib/client":{...client,useSession:()=>({token:"token",epoch:1,config:{},signals})},
    "@/lib/live":{...live,connectLive:()=>({close:()=>{},reconnect:()=>{}})},
    "@/lib/live-fixture":{},"@/lib/live-history":history,
  }
  const provider=load<{LiveProvider:React.ComponentType<{children:React.ReactNode}>}>("./use-live.tsx",modules)
  const previous=globalThis.fetch
  globalThis.fetch=async(path,options)=>{
    requests.push({path:String(path),signal:options!.signal!})
    if(requests.length===1)return Response.json({code:"CoverageWarming"},{status:503,headers:{"Retry-After":"2"}})
    const bounds=new URL(String(path),"https://local").searchParams,to=bounds.get("to")!,at=new Date(Date.parse(to)-1000).toISOString()
    return Response.json({from:bounds.get("from"),to,events:[{type:"event",at,kind:"cluster",cluster:{kind:"pod",at,event:"changed",pod:{uid:"seeded-pod",ready:"True"}}}],coverage:{from:null,to:null,sources:[],gaps:[]}})
  }
  const render=()=>renderToStaticMarkup(React.createElement(provider.LiveProvider,null,"operating"))
  let cleanups:(void|(()=>void))[]=[]
  try {
    render();cleanups=effects.splice(0).map(effect=>effect())
    await nextTurn()
    assert.equal(requests.length,1)
    assert.equal(state.eventArchiveError,"초기화 중")
    assert.match(render(),/role="status"[^>]*>초기화 중/)
    t.mock.timers.tick(1999);await nextTurn();assert.equal(requests.length,1)
    if(outcome==="unmount")cleanups.forEach(dispose=>dispose?.())
    if(outcome==="session")signals.forEach(controller=>controller.abort())
    t.mock.timers.tick(1);await nextTurn()
    if(outcome==="recover") {
      assert.equal(requests.length,2)
      assert.equal(requests[1].path,requests[0].path)
      assert.equal(requests[1].signal,requests[0].signal)
      assert.equal(state.events.length,1)
      assert.equal(state.eventArchiveError,null)
      assert.doesNotMatch(render(),/초기화 중|최근 사건 기록 조회 실패/)
    } else {
      assert.equal(requests.length,1)
      assert.equal(requests[0].signal.aborted,true)
      assert.equal(state.events.length,0)
    }
  } finally {cleanups.forEach(dispose=>dispose?.());globalThis.fetch=previous}
})

test("exhausted event seed offers an explicit retry that restores the archive",async t=>{
  t.mock.timers.enable({apis:["setTimeout"]})
  let state=live.initialLiveState(),attempt=0,ready=false
  const effects:{run:()=>void|(()=>void);deps:readonly unknown[]}[]=[],signals=new Set<AbortController>(),requests:{path:string;signal:AbortSignal}[]=[]
  const queryClient={getQueryData:()=>state,setQueryData:(_key:unknown,change:(value:live.LiveState)=>live.LiveState)=>{state=change(state)}}
  const modules={
    react:{...React,useMemo:(create:()=>unknown)=>create(),useCallback:(callback:unknown)=>callback,useRef:(current:unknown)=>({current}),useState:()=>[attempt,(next:number|((value:number)=>number))=>{attempt=typeof next==="function"?next(attempt):next}],useEffect:(run:()=>void|(()=>void),deps:readonly unknown[])=>{effects.push({run,deps})}},
    "@tanstack/react-query":{skipToken:Symbol(),useQuery:()=>({data:state}),useQueryClient:()=>queryClient},
    "next/navigation":{useSearchParams:()=>new URLSearchParams(),usePathname:()=>"/"},
    "@/lib/client":{...client,useSession:()=>({token:"token",epoch:1,config:{},signals})},
    "@/lib/live":{...live,connectLive:()=>({close:()=>{},reconnect:()=>{}})},
    "@/lib/live-fixture":{},"@/lib/live-history":history,
  }
  const provider=load<{LiveProvider:(props:{children:React.ReactNode})=>React.ReactElement}>("./use-live.tsx",modules)
  const previous=globalThis.fetch
  globalThis.fetch=async(path,options)=>{
    requests.push({path:String(path),signal:options!.signal!})
    if(!ready)return Response.json({code:"CoverageWarming"},{status:503,headers:{"Retry-After":"2"}})
    const bounds=new URL(String(path),"https://local").searchParams,to=bounds.get("to")!,at=new Date(Date.parse(to)-1000).toISOString()
    return Response.json({from:bounds.get("from"),to,events:[{type:"event",at,kind:"cluster",cluster:{kind:"pod",at,event:"changed",pod:{uid:"restored-pod",ready:"True"}}}],coverage:{from:null,to:null,sources:[],gaps:[]}})
  }
  const render=()=>{effects.length=0;return provider.LiveProvider({children:"operating"})}
  const elements=(node:unknown):React.ReactElement<{children?:React.ReactNode;onClick?:()=>void}>[]=>{
    if(Array.isArray(node))return node.flatMap(elements)
    if(!React.isValidElement<{children?:React.ReactNode;onClick?:()=>void}>(node))return []
    return [node,...elements(node.props.children)]
  }
  let cleanups:(void|(()=>void))[]=[]
  try {
    render();const initialDeps=effects[1].deps;cleanups=effects.map(effect=>effect.run())
    await nextTurn()
    for(let retry=0;retry<180;retry++){t.mock.timers.tick(2000);await nextTurn()}
    assert.equal(requests.length,181)
    t.mock.timers.tick(100000);await nextTurn();assert.equal(requests.length,181)
    assert.ok(requests.every(request=>request.path===requests[0].path))
    const button=elements(render()).find(node=>node.type==="button"&&node.props.children==="다시 조회")
    assert.ok(button?.props.onClick,"exhaustion must leave an explicit archive retry action")
    ready=true
    button.props.onClick()
    render()
    assert.notDeepEqual(effects[1].deps,initialDeps,"the action must restart the seed effect")
    cleanups[1]?.()
    cleanups[1]=effects[1].run()
    await nextTurn()
    assert.equal(requests.length,182)
    assert.equal(requests[0].signal.aborted,true)
    assert.notEqual(requests.at(-1)!.signal,requests[0].signal)
    assert.equal(state.events.length,1)
    assert.equal(state.eventArchiveError,null)
    assert.ok(!elements(render()).some(node=>node.type==="button"&&node.props.children==="다시 조회"))
  } finally {cleanups.forEach(dispose=>dispose?.());globalThis.fetch=previous}
})
