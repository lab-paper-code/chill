import assert from "node:assert/strict"
import { test } from "node:test"
import { setImmediate as nextTurn } from "node:timers/promises"
import { createRequire } from "node:module"
import { readFileSync } from "node:fs"
import * as React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import * as live from "./live.ts"
import * as history from "./live-history.ts"

const now=Date.parse("2026-11-03T12:00:00Z"),from=new Date(now-900000).toISOString(),to=new Date(now).toISOString(),at="2026-11-03T11:59:00.123456789Z"
const event={type:"event",at,kind:"cluster",cluster:{kind:"pod",at,event:"changed",pod:{uid:"pod-a",ready:"False"}}}
const coverage={from:null,to:null,sources:[{from,to,stream:"cluster",source:"archive",store:"s3",key:"exact-object"}],gaps:[{from,to,stream:"cluster",reason:"MissingArchive"}]}
const page=(nextCursor?:string)=>({from,to,events:[event],coverage,...(nextCursor?{nextCursor}:{})})
const updateState=(read:()=>live.LiveState,write:(state:live.LiveState)=>void)=>(change:(state:live.LiveState)=>live.LiveState)=>write(change(read()))

test("recent archive stops at five pages and passes the opaque cursor with identical bounds",async()=>{
  let state=live.initialLiveState();const paths:string[]=[],abort=new AbortController()
  await history.seedRecentLiveEvents(async(path,signal)=>{assert.equal(signal,abort.signal);paths.push(path);return page(`opaque+/=${paths.length}`)},updateState(()=>state,value=>state=value),abort.signal,now)
  assert.equal(paths.length,5)
  for(const [index,path] of paths.entries()){
    const url=new URL(path,"http://test/")
    assert.equal(url.pathname,"/v1/observations/events")
    assert.equal(url.searchParams.get("from"),from);assert.equal(url.searchParams.get("to"),to);assert.equal(url.searchParams.get("limit"),"100")
    assert.equal(url.searchParams.get("cursor"),index?`opaque+/=${index}`:null)
  }
  assert.equal(state.events.length,1)
  assert.equal(Object.hasOwn(state.events[0],"seq"),false)
  assert.deepEqual(state.eventArchive?.coverage,coverage)
  assert.equal(state.eventArchive?.partial,true)
  assert.equal(state.eventArchive?.nextCursor,"opaque+/=5")
})

test("archive arriving after live and live arriving after archive dedupe payload without losing WS envelope",async()=>{
  const snapshot=live.parseLiveMessage({type:"snapshot",seq:0,at:from,workloads:[],nodes:[],instances:[],routing:{},decision:null,actions:[],reservations:[]})!
  const ws=live.parseLiveMessage({...event,seq:1,at:to})!
  let state=live.applyLiveMessage(live.initialLiveState(),snapshot)
  const {promise,resolve:finish}=Promise.withResolvers<unknown>()
  const seeding=history.seedRecentLiveEvents(()=>promise,updateState(()=>state,value=>state=value),new AbortController().signal,now)
  state=live.applyLiveMessage(state,ws)
  const observed=state.lastObservedAt,checkpoint=state.checkpoint,current=state.snapshot
  finish(page());await seeding
  assert.equal(state.events.length,1);assert.equal(state.events[0].seq,1)
  assert.equal(state.checkpoint,checkpoint);assert.equal(state.lastObservedAt,observed);assert.equal(state.snapshot,current)
  assert.deepEqual(state.eventArchive?.coverage,coverage)
  let reverse=live.applyLiveMessage(live.initialLiveState(),snapshot)
  await history.seedRecentLiveEvents(async()=>page(),updateState(()=>reverse,value=>reverse=value),new AbortController().signal,now)
  reverse=live.applyLiveMessage(reverse,ws)
  assert.equal(reverse.events.length,1);assert.equal(reverse.events[0].seq,1)
  const distinct=live.parseLiveMessage({...event,seq:2,cluster:{...event.cluster,pod:{uid:"pod-a",ready:"True"}}})!
  reverse=live.applyLiveMessage(reverse,distinct)
  assert.equal(reverse.events.length,2,"different factual payloads at one timestamp are not duplicates")
})

test("archive cancellation discards late results and never starts another page",async()=>{
  const abort=new AbortController();let calls=0,state=live.initialLiveState()
  const original=state,{promise,resolve:finish}=Promise.withResolvers<unknown>()
  const pending=history.seedRecentLiveEvents(()=>{calls++;return promise},updateState(()=>state,value=>state=value),abort.signal,now)
  abort.abort();finish(page("next"))
  await assert.rejects(pending,{name:"AbortError"})
  assert.equal(state,original);assert.equal(calls,1)
  await assert.rejects(history.seedRecentLiveEvents(async()=>{calls++;return page()},()=>assert.fail("cancelled update"),abort.signal,now),{name:"AbortError"})
  assert.equal(calls,1)
})

test("archive rejects invalid pages instead of silently counting or dropping malformed facts",async()=>{
  for(const invalid of [
    {...page(),events:[{...event,seq:10}]},
    {...page(),coverage:{...coverage,gaps:[{from:to,to:from,stream:"cluster",reason:"bad"}]}},
    {...page(),from:to},
    {...page(),events:Array.from({length:101},()=>event)},
  ]){
    let state=live.initialLiveState()
    await assert.rejects(history.seedRecentLiveEvents(async()=>invalid,updateState(()=>state,value=>state=value),new AbortController().signal,now))
    assert.deepEqual(state.events,[])
  }
})

test("one factual retention status renders without estimating a lost-event count",()=>{
  const require=createRequire(import.meta.url),exports:Record<string,React.ComponentType<{children:React.ReactNode}>>={}
  let state=live.initialLiveState()
  const modules:Record<string,unknown>={
    react:React,"@tanstack/react-query":{skipToken:Symbol(),useQuery:()=>({data:state}),useQueryClient:()=>({})},
    "next/navigation":{useSearchParams:()=>new URLSearchParams(),usePathname:()=>"/"},
    "@/lib/client":{useSession:()=>({token:"secret",epoch:1,config:{},signals:new Set()}),relay:()=>{}},
    "@/lib/live":live,"@/lib/live-fixture":{},"@/lib/live-history":{},
  }
  const source=readFileSync(new URL("./use-live.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
  const render=()=>renderToStaticMarkup(React.createElement(exports.LiveProvider,{children:React.createElement("span",{},"operating")}))
  assert.doesNotMatch(render(),/보존 공백/)
  state={...state,resumeHistory:{reason:"fresh",replayed:false,requested:{afterSeq:0,throughSeq:999},retained:{events:null,power:null},gaps:[{fromSeq:1,toSeq:999,scope:"events",reason:"NoRetainedFrames"}]},eventArchive:{from,to,coverage,partial:true,nextCursor:"rest"}}
  const html=render()
  assert.equal(html.match(/보존 공백/g)?.length,1)
  assert.match(html,/role="status"/)
  assert.match(html,/최근 사건 일부만 표시/)
  assert.doesNotMatch(html,/999|손실|누락.*개/)
})

test("each page preserves coverage sources and gaps while an exhausted cursor clears the partial indication",async()=>{
  let state=live.initialLiveState(),calls=0
  const firstCoverage={from,to,sources:coverage.sources,gaps:coverage.gaps}
  const secondCoverage={from,to,sources:[{from,to,stream:"resources",source:"local"}],gaps:[{from,to,stream:"resources",reason:"BeforeRetention"}]}
  await history.seedRecentLiveEvents(async()=>{
    calls++
    if(calls===1)return {...page("opaque"),coverage:firstCoverage}
    assert.equal(state.eventArchive?.partial,true)
    return {...page(),coverage:secondCoverage}
  },updateState(()=>state,value=>state=value),new AbortController().signal,now)
  assert.equal(calls,2)
  assert.deepEqual(state.eventArchive,{from,to,coverage:{from,to,sources:[...firstCoverage.sources,...secondCoverage.sources],gaps:[...firstCoverage.gaps,...secondCoverage.gaps]},partial:false})
  assert.equal(state.events.filter(row=>row.kind==="decision").length,0)
  assert.equal(state.checkpoint,null)
  assert.equal(state.lastObservedAt,null)
})

test("session revocation cannot refill live state from an archive response already in flight",async()=>{
  let state=live.initialLiveState()
  const {promise,resolve}=Promise.withResolvers<unknown>()
  const pending=history.seedRecentLiveEvents(()=>promise,updateState(()=>state,value=>state=value),new AbortController().signal,now)
  state={...live.initialLiveState(),status:"unauthorized"}
  resolve(page())
  await pending
  assert.deepEqual(state,{...live.initialLiveState(),status:"unauthorized"})
})

test("provider seed reentry uses the current identity and cancels the previous identity's late response",async()=>{
  const require=createRequire(import.meta.url),exports:Record<string,React.ComponentType<{children:React.ReactNode}>>={}
  const effects:(()=>void|(()=>void))[]=[],requests:{path:string;token:string;signal:AbortSignal;resolve:(value:{data:unknown})=>void}[]=[]
  const cache=new Map<string,live.LiveState>()
  let token="first-token",epoch=1,path="/"
  const signals=new Set<AbortController>()
  const queryClient={
    getQueryData:(key:unknown)=>cache.get(JSON.stringify(key)),
    setQueryData:(key:unknown,change:live.LiveState|((state:live.LiveState|undefined)=>live.LiveState|undefined))=>{
      const id=JSON.stringify(key),value=typeof change==="function"?change(cache.get(id)):change
      if(value)cache.set(id,value)
    },
  }
  const modules:Record<string,unknown>={
    react:{...React,useEffect:(effect:()=>void|(()=>void))=>{effects.push(effect)}},
    "@tanstack/react-query":{skipToken:Symbol(),useQuery:({queryKey}:{queryKey:unknown})=>({data:queryClient.getQueryData(queryKey)??live.initialLiveState()}),useQueryClient:()=>queryClient},
    "next/navigation":{useSearchParams:()=>new URLSearchParams(),usePathname:()=>path},
    "@/lib/client":{ApiError:class extends Error{},useSession:()=>({token,epoch,config:{},signals}),relay:(path:string,requestToken:string,signal:AbortSignal)=>{
      const {promise,resolve}=Promise.withResolvers<{data:unknown}>()
      requests.push({path,token:requestToken,signal,resolve});return promise
    }},
    "@/lib/live":{...live,connectLive:()=>({close:()=>{},reconnect:()=>{}})},
    "@/lib/live-fixture":{},"@/lib/live-history":history,
  }
  const source=readFileSync(new URL("./use-live.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
  const mount=()=>{
    effects.length=0
    renderToStaticMarkup(React.createElement(exports.LiveProvider,{children:"operating"}))
    return effects.map(effect=>effect())
  }
  const cleanup=mount()
  assert.equal(requests.length,1);assert.equal(requests[0].token,"first-token")
  signals.forEach(abort=>abort.abort())
  cleanup.forEach(dispose=>dispose?.())
  token="second-token";epoch=2
  const nextCleanup=mount()
  assert.equal(requests[0].signal.aborted,true)
  assert.equal(requests[1].token,"second-token")
  const response=(request:typeof requests[number])=>{
    const bounds=new URL(request.path,"http://test/").searchParams
    return {data:{from:bounds.get("from"),to:bounds.get("to"),events:[],coverage:{from:null,to:null,sources:null,gaps:null}}}
  }
  requests[0].resolve(response(requests[0]))
  requests[1].resolve(response(requests[1]))
  await nextTurn()
  assert.equal([...cache.keys()].some(key=>key.includes('1,')),false,"a detached identity must not recreate its cache")
  assert.ok(queryClient.getQueryData(["chill-live",2,null])?.eventArchive)
  nextCleanup.forEach(dispose=>dispose?.())
  path="/records"
  const awayCleanup=mount()
  assert.equal(requests.length,2,"non-operating routes do not read the archive")
  awayCleanup.forEach(dispose=>dispose?.())
  path="/"
  const reentryCleanup=mount()
  assert.equal(requests.length,3)
  assert.equal(requests[2].token,"second-token")
  reentryCleanup.forEach(dispose=>dispose?.())
  requests[2].resolve(response(requests[2]))
  await nextTurn()
})
