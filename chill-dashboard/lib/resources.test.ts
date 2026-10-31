import assert from "node:assert/strict"
import { test } from "node:test"
import { initialLiveState, type LiveMetrics, type LiveNode } from "./live.ts"
import type { InventoryView, PoolsView } from "./read-types.ts"
import { deviceRows, devicePowerPercent, devicePowerScale } from "../features/resources-data.ts"

const at="2026-11-01T00:00:00Z"
const node=(uid:string,name:string):LiveNode=>({uid,name,ready:true,measurement:false,powerMode:"fixed",observedAt:at})
const metric:LiveMetrics={type:"metrics",seq:1,at,bucketAt:at,workloads:{},nodes:{a:{watts:20,observedAt:at},b:{watts:80,observedAt:at},outside:{watts:null,observedAt:null}},pool:{wholeWatts:null,activeWatts:null}}
const pools:PoolsView={observedAt:at,workloads:[],measurement:[],available:[],other:[],issues:[]}

test("device rows preserve observations outside board configuration and keep UID-only observations",()=>{
  const state={...initialLiveState(),snapshot:{workloads:[],nodes:[node("b","나")],instances:[],routing:{},decision:null,actions:[],reservations:[]},metrics:[metric]}
  const inventory:InventoryView={readAt:at,deviceClassesResourceVersion:"1",nodesResourceVersion:"1",modelSpecsResourceVersion:"1",models:[],classes:[{uid:"class",name:"장비",resourceVersion:"1",architecture:"arm64",memory:"8Gi",nodeSelector:{},powerModes:[],nodes:[{uid:"a",name:"가",resourceVersion:"1",ready:"True",schedulingDisabled:false}]}]}
  const rows=deviceRows(state,inventory,pools)
  assert.deepEqual(rows.map(row=>row.uid),["a","b","outside"])
  assert.equal(rows[0].configured,false)
  assert.equal(rows[1].configured,true)
  assert.equal(rows[2].name,"")
  assert.equal(rows[2].watts,null)
  assert.equal(rows[2].ready,null)
  const changed=deviceRows({...state,metrics:[{...metric,nodes:{a:{watts:100,observedAt:at},b:{watts:0,observedAt:at}}}]},inventory,pools)
  assert.deepEqual(changed.slice(0,2).map(row=>row.name),["가","나"])
})
test("all device power bars use a shared retained scale and missing power never becomes zero",()=>{
  const rows=deviceRows({...initialLiveState(),metrics:[metric]})
  const scale=devicePowerScale(rows,100)
  assert.equal(scale,100)
  assert.equal(devicePowerPercent(rows.find(row=>row.uid==="a")!,scale),20)
  assert.equal(devicePowerPercent(rows.find(row=>row.uid==="b")!,scale),80)
  assert.equal(devicePowerPercent(rows.find(row=>row.uid==="outside")!,scale),null)
  const zero=deviceRows({...initialLiveState(),metrics:[{...metric,nodes:{a:{watts:0,observedAt:at}}}]})[0]
  assert.equal(devicePowerPercent(zero,scale),0)
})
test("measurement, available, excluded and shared instance roles survive the combined device projection",()=>{
  const ref={uid:"shared",name:"공유 장치"},instance={id:"shared-instance",node:ref,profile:"p",ready:true,stopped:false,workloads:[{uid:"w1",name:"서비스 1"},{uid:"w2",name:"서비스 2"}],waiting:{},running:{},assignedUnknown:{},observedAt:at}
  const data:PoolsView={...pools,workloads:[{workload:{uid:"w1",name:"서비스 1"},nodes:[ref],instances:[instance]},{workload:{uid:"w2",name:"서비스 2"},nodes:[ref],instances:[instance]}],measurement:[{node:{uid:"measure",name:"측정"},instances:[]}],available:[{node:{uid:"available",name:"가용"},ready:true,powerMode:"fixed",reasons:[]}],other:[{node:{uid:"excluded",name:"제외"},ready:false,powerMode:"fixed",reasons:["ExcludedNode"]}]}
  const rows=deviceRows(initialLiveState(),undefined,data)
  assert.deepEqual(rows.find(row=>row.uid==="shared")?.instances,["shared-instance"])
  assert.deepEqual(rows.find(row=>row.uid==="shared")?.services.map(row=>row.uid),["w1","w2"])
  assert.deepEqual(rows.find(row=>row.uid==="measure")?.roles,["측정 중"])
  assert.deepEqual(rows.find(row=>row.uid==="available")?.roles,["가용"])
  assert.deepEqual(rows.find(row=>row.uid==="excluded")?.roles,["제외·기타"])
})
test("same-name replacement devices never merge their power or readiness",()=>{
  const state={...initialLiveState(),snapshot:{workloads:[],nodes:[node("old","동일 이름"),node("new","동일 이름")],instances:[],routing:{},decision:null,actions:[],reservations:[]},metrics:[{...metric,nodes:{new:{watts:5,observedAt:at}}}]}
  const rows=deviceRows(state)
  assert.equal(rows.length,2)
  assert.equal(rows.find(row=>row.uid==="old")?.watts,null)
  assert.equal(rows.find(row=>row.uid==="new")?.watts,5)
})
