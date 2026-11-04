import assert from "node:assert/strict"
import { test } from "node:test"
import { createElement, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { load } from "./evaluation-author-test-helpers.ts"
import { initialLiveState, type LiveState } from "./live.ts"
import * as display from "./live-display.ts"
import * as data from "../features/resources-data.ts"
import type { PoolsView } from "./read-types.ts"
import * as icons from "lucide-react"

const at="2026-11-05T00:00:00Z"
function render(state:LiveState,selected=false,reads:Record<string,unknown>={}) {
  const wrap=({children}:{children?:ReactNode})=>createElement("div",{},children)
  const modules:Record<string,unknown>={
    "lucide-react":icons,
    "next/link":{default:"a"},"next/navigation":{useSearchParams:()=>new URLSearchParams(selected?{node:"node-private-uid"}:{}),useRouter:()=>({replace:()=>{}})},
    "@/lib/client":{useChillQuery:(path:string)=>({data:reads[path],isPending:false,error:null})},"@/lib/use-live":{useLive:()=>({state})},
    "@/hooks/use-measurement-data":{useCoverageInventory:()=>({isPending:false,error:null})},"@/lib/scheduler":{parseSchedulerJSON:JSON.parse,stringifySchedulerJSON:JSON.stringify},
    "@/lib/live-display":display,"@/lib/status":{STATUS_AXES:{}},"@/components/status-cell":{StatusCell:({label}:{label:string})=>createElement("span",{},label)},
    "@/components/ui/button":{Button:({children,onClick,className}:{children?:ReactNode;onClick?:()=>void;className?:string})=>createElement("button",{onClick,className},children)},"@/lib/source-export":{safeSource:(value:unknown)=>value},
    "@/components/ui/sheet":{Sheet:({open,children}:{open:boolean;children:ReactNode})=>open?createElement("div",{},children):null,SheetContent:wrap,SheetHeader:wrap,SheetTitle:wrap},
    "@/components/ui/tooltip":{Tooltip:wrap,TooltipTrigger:wrap,TooltipContent:wrap},
    "./resources-data":data,"./power-series":{usePowerSeries:()=>({points:[],query:{isPending:false,error:null}}),PowerTrend:()=>null},"./resources.css":{},
  }
  modules["@/components/read-region"]=load("../components/read-region.tsx",modules)
  modules["./serving-instance.css"]={}
  modules["./observation-age.css"]={}
  modules["@/components/serving-instance"]=load("../components/serving-instance.tsx",modules)
  modules["@/components/observation-age"]=load("../components/observation-age.tsx",modules)
  const exports=load("../features/resources.tsx",modules)
  return renderToStaticMarkup(createElement(exports.DevicesPage as ()=>ReactNode))
}
function state():LiveState {
  return {...initialLiveState(),status:"fixture",lastObservedAt:at,lastPower:{watts:20,bucketAt:at},snapshot:{nodes:[{uid:"node-private-uid",name:"장치 A",ready:true,measurement:false,powerMode:"fixed",observedAt:at}],workloads:[{uid:"service-private-uid",name:"영상 분석"} as NonNullable<LiveState["snapshot"]>["workloads"][number]],instances:[{id:"pod-private-id",node:{uid:"node-private-uid",name:"장치 A"},ownerWorkloadUID:"service-private-uid",profileID:"profile-private-hash",ready:true,stopped:false,routable:{"service-private-uid":true},running:{"service-private-uid":2},waiting:{"service-private-uid":1},observedAt:at}],routing:{"service-private-uid":{"pod-private-id":1,other:3}},decision:null,actions:[],reservations:[]},metrics:[]}
}
test("device fleet shows service instance state and actual normalized routing without exposing identity text",()=>{
  const html=render(state())
  assert.ok(/25(?:\.0)?%/.test(html),"actual routing share is visible on the device's service instance")
  assert.ok(html.includes("영상 분석"))
  const text=html.replace(/<[^>]*>/g,"")
  assert.ok(!text.includes("node-private-uid"))
  assert.ok(!text.includes("pod-private-id"))
  assert.ok(!text.includes("profile-private-hash"))
})
test("device detail presents serving observations before power history and keeps raw identity in a disclosure",()=>{
  const html=render(state(),true)
  assert.ok(/25(?:\.0)?%/.test(html))
  assert.ok(html.includes('aria-label="처리 요청"'))
  assert.ok(html.includes('aria-label="대기 요청"'))
  const detail=html.slice(html.indexOf('class="devices-detail"'))
  const raw=detail.indexOf('<details class="devices-original"')
  assert.ok(raw>0)
  for(const identity of ["node-private-uid","pod-private-id","profile-private-hash"])assert.ok(!detail.slice(0,raw).replace(/<[^>]*>/g,"").includes(identity))
  const disclosure=detail.slice(raw,detail.lastIndexOf("</details>")+10)
  for(const [,attributes] of disclosure.matchAll(/<details\b([^>]*)>/g))assert.ok(!/\bopen(?:\s|=|$)/.test(attributes),"raw disclosures start closed")
  const source=disclosure.match(/<pre class="source-json">([\s\S]*?)<\/pre>/)?.[1]
  assert.ok(source,"the real source renderer retains the raw payload inside the disclosure")
  for(const identity of ["node-private-uid","pod-private-id","profile-private-hash"])assert.ok(source.includes(identity))
  assert.ok(disclosure.includes("lucide-download"),"the real source export control remains inside the disclosure")
})
test("device power observations keep identical element slots across freshness and missing-power changes",()=>{
  const first=state(),second=state()
  second.lastPower={watts:20,bucketAt:"2020-01-01T00:00:00Z"}
  second.metrics=[{type:"metrics",at,seq:1,bucketAt:at,nodes:{"node-private-uid":{watts:20,observedAt:at}},workloads:{},pool:{wholeWatts:20,activeWatts:20}}]
  const structure=(html:string)=>(html.match(/<\/?[a-z][^>]*>/g)??[]).map(tag=>tag.match(/^<\/?[a-z]+/)?.[0])
  assert.deepEqual(structure(render(first)),structure(render(second)))
})

test("REST pool sparse counters remain observed zero without fabricating routing",()=>{
  const rest=state()
  rest.snapshot!.instances=[]
  const ref={uid:"node-private-uid",name:"장치 A"}
  // Representative operator-pools.json shape: bound owner, sparse request maps and a physical runtime ID.
  const instance={id:"pod-private-id/containerd://runtime-private",node:ref,ownerWorkloadUID:"service-private-uid",profile:"hash",ready:true,stopped:false,workloads:[{uid:"service-private-uid",name:"영상 분석"}],running:{},waiting:{},assignedUnknown:{},observedAt:at}
  const pools:PoolsView={observedAt:at,workloads:[{workload:instance.workloads[0],nodes:[ref],instances:[instance]}],measurement:[],available:[{node:{uid:"available-device",name:"Available device"},ready:true,powerMode:"fixed",reasons:null}],other:[],issues:[]}
  const html=render(rest,false,{"v1/pools":pools})
  assert.ok(html.includes("영상 분석"))
  assert.ok(!/25(?:\.0)?%/.test(html))
  const counts=html.match(/aria-label="(?:처리|대기) 요청"[^>]*>(.*?)<\/span>/g)??[]
  assert.equal(counts.length,2)
  assert.ok(counts.every(value=>/>[^<]*\b0\b/.test(value)),"a missing UID in an observed pool counter map means zero")
})

test("unready REST instance with retained routing weight leaves actual distribution unobserved",()=>{
  const live=state()
  live.snapshot!.instances=[]
  const uid="service-private-uid",id="unready-rest"
  const snapshot={nodes:[],workloads:[{uid,name:"영상 분석"}],instances:[{id,node:{uid:"node-private-uid",name:"장치 A"},workloadUID:uid,ready:false,stopped:false}],routing:{[uid]:{[id]:1000}}}
  const html=render(live,false,{"v1/scheduler/state":{snapshot}})
  const share=html.match(/aria-label="실제 분배율"[^>]*>([^<]*)</)?.[1]
  assert.notEqual(share,"100%","a retained declaration is not actual dispatch")
  assert.equal(share,"분배 미관측")
})

test("device header and aggregate power retain shared connection and age slots",()=>{
  const live=state()
  live.metrics=[{type:"metrics",at,seq:1,bucketAt:at,nodes:{},workloads:{},pool:{wholeWatts:20,activeWatts:20}}]
  const html=render(live)
  const heading=html.slice(html.indexOf('class="devices-heading-status"'),html.indexOf('<section'))
  assert.ok(heading.includes('class="live-observation"'))
  assert.ok(/class="observation-age-value">[\d,]+</.test(heading),"metric age remains numeric rather than a freshness verdict")
  const power=html.slice(html.indexOf('devices-power-facts'),html.indexOf('devices-gap-table'))
  assert.ok(power.includes('class="observation-age"'))
  assert.ok(power.includes(`title="${at.replace("Z",".000Z")}"`))
  live.lastPower=null
  assert.ok(render(live).includes('class="observation-age-unit"></span>'))
})

test("shared physical instances enclose services under one accessible group label",()=>{
  const shared=state()
  shared.snapshot!.workloads.push({...shared.snapshot!.workloads[0],uid:"second-service",name:"다른 서비스"})
  shared.snapshot!.instances[0].routable["second-service"]=true
  shared.snapshot!.instances[0].running["second-service"]=1
  const html=render(shared,true)
  const groups=html.match(/class="devices-instance" role="group" aria-label="[^"]+"/g)??[]
  assert.equal(groups.length,2,"one physical group in the fleet and one in the Sheet")
  assert.equal((html.match(/class="devices-instance-label"/g)??[]).length,2)
  assert.equal((html.match(/class="devices-service-instance"/g)??[]).length,4)
  const detail=html.slice(html.indexOf('class="devices-detail"'),html.indexOf('<details class="devices-original"'))
  assert.ok(!detail.includes('class="serving-instance-mode"'))
})

test("owner-only stopped REST instances retain their own service links and request slots in fleet and detail",()=>{
  const node={uid:"node-private-uid",name:"Device A"}
  const owner={uid:"owner-service",name:"Owner service"},other={uid:"other-service",name:"Other service"}
  const instance={id:"owner-pod/containerd://private",node,ownerWorkloadUID:owner.uid,profile:"owner-profile",ready:true,stopped:true,workloads:[],running:{},waiting:{},assignedUnknown:{},observedAt:at}
  const second={...instance,id:"second-pod",ownerWorkloadUID:other.uid,stopped:false,workloads:[other],running:{[other.uid]:7},waiting:{[other.uid]:3}}
  const reads={
    "v1/pools":{observedAt:at,workloads:[{workload:owner,nodes:[node],instances:[instance]},{workload:other,nodes:[node],instances:[second]}],measurement:[],available:[],other:[],issues:null},
    "v1/scheduler/state":{snapshot:{at,workloads:[],nodes:[],instances:[{id:instance.id,node,workloadUID:"",ready:true,stopped:true}],routing:{}},decision:{target:null}},
  }
  const html=render(initialLiveState(),true,reads)
  const visible=html.slice(0,html.indexOf('<details class="devices-original"'))
  const groups=visible.split(/<div class="devices-instance" role="group"[^>]*>/).slice(1)
  assert.equal(groups.length,4,"both physical instances appear in fleet and detail")
  for(const [index,group] of groups.entries()) {
    const service=index%2===0?owner:other,unrelated=index%2===0?other:owner
    assert.ok(group.includes(`<strong>${service.name}</strong>`),"known instance service identity is preserved")
    assert.ok(group.includes(`href="/workloads/${encodeURIComponent(service.name)}?uid=${service.uid}"`))
    assert.ok(!group.includes(`<strong>${unrelated.name}</strong>`),"other services on the device do not leak into this instance")
    assert.ok(group.includes(`data-tone="${index%2===0?"idle":"ok"}"`),"stopped takes precedence over ready")
    const counts=group.match(/aria-label="(?:처리|대기) 요청"[^>]*>(.*?)<\/span>/g)??[]
    assert.equal(counts.length,2)
    assert.deepEqual(counts.map(value=>value.replace(/<[^>]*>/g,"").match(/\d+/)?.[0]),index%2===0?["0","0"]:["7","3"])
    assert.ok(!/aria-label="실제 분배율"[^>]*>[^<]*\d/.test(group),"unroute does not invent an observed routing share")
  }
})

test("scheduler-only placements keep request counts unobserved without a pool",()=>{
  const node={uid:"node-private-uid",name:"Device A"},service={uid:"scheduler-service",name:"Scheduler service"}
  const html=render(initialLiveState(),false,{"v1/scheduler/state":{snapshot:{workloads:[service],nodes:[],instances:[{id:"scheduler-instance",node,workloadUID:service.uid,ready:true,stopped:false}],routing:{}}}})
  assert.ok(html.includes(`<strong>${service.name}</strong>`))
  const counts=html.match(/aria-label="(?:처리|대기) 요청"[^>]*>(.*?)<\/span>/g)??[]
  assert.equal(counts.length,2)
  assert.ok(counts.every(value=>!/\d/.test(value)),"without a pool observation neither request slot invents zero")
})

test("REST instance membership includes active request services but excludes unrelated zero counters",()=>{
  const node={uid:"node-private-uid",name:"Device A"}
  const services=["Owner","Bound","Running","Waiting","Unrelated"].map(name=>({uid:name.toLowerCase(),name}))
  const instance={id:"rest-instance",node,ownerWorkloadUID:"owner",profile:"hash",ready:true,stopped:false,workloads:[services[1]],running:{running:4,unrelated:0},waiting:{waiting:5,unrelated:0},assignedUnknown:{},observedAt:at}
  const pools:PoolsView={observedAt:at,workloads:services.map((workload,index)=>({workload,nodes:[node],instances:index===0?[instance]:[]})),measurement:[],available:[],other:[],issues:null}
  const html=render(initialLiveState(),false,{"v1/pools":pools})
  const cells=html.split(/<div class="devices-service-instance"[^>]*>/).slice(1)
  assert.equal(cells.length,4)
  for(const [name,running,waiting] of [["Owner",0,0],["Bound",0,0],["Running",4,0],["Waiting",0,5]] as const) {
    const cell=cells.find(value=>value.includes(`<strong>${name}</strong>`))
    assert.ok(cell)
    const counts=cell.match(/aria-label="(?:처리|대기) 요청"[^>]*>(.*?)<\/span>/g)??[]
    assert.deepEqual(counts.map(value=>Number(value.replace(/<[^>]*>/g,"").match(/\d+/)?.[0])),[running,waiting])
  }
  assert.ok(!html.includes("<strong>Unrelated</strong>"))
})
