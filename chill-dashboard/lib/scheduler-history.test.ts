import assert from "node:assert/strict"
import test from "node:test"
import { schedulerRecordRef, schedulerRecordParams, readSchedulerRecord, schedulerRecordPath, schedulerRecordMatches, schedulerPageParams, schedulerComparisonParams, schedulerHistoryNodeMatches, powerNumber, powerPolicyFacts, schedulerHistoryKindMatches, groupHistoryKeeps } from "./scheduler-history.ts"
import type { SchedulerObservation, SchedulerObservationSummary } from "./read-types.ts"
import { relayPlan } from "./relay.ts"
import { serverConfig } from "./server-config.ts"

const from="2026-10-13T02:00:00.123456789Z",to="2026-10-13T03:00:00.987654321Z"
const event:SchedulerObservation={at:"2026-10-13T02:30:00.000000001Z",action:{id:"same-action",kind:"route",node:{name:"node",uid:"uid-a"},phase:"starting",startedAt:"2026-10-13T02:29:59.123456789Z"}}

test("incident node filtering matches only loaded records with that explicit node and survives evidence close",()=>{
  assert.equal(schedulerHistoryNodeMatches("",event),true)
  assert.equal(schedulerHistoryNodeMatches("uid-a",event),true)
  assert.equal(schedulerHistoryNodeMatches("node",event),true)
  assert.equal(schedulerHistoryNodeMatches("uid-b",event),false)
  assert.equal(schedulerHistoryNodeMatches("uid-a",{at:event.at,node:{uid:"uid-a",name:"node"}}),true)
  assert.equal(schedulerHistoryNodeMatches("uid-a",{at:event.at,decision:{at:event.at,status:"Keep"}}),false)
  const params=new URLSearchParams({filterNode:"uid-a",eventAt:event.at,cursor:"100",...Object.fromEntries(schedulerRecordParams(schedulerRecordRef(event,from,to,"21")))})
  const closed=schedulerPageParams(params,"100")
  assert.equal(closed.get("filterNode"),"uid-a")
  assert.equal(closed.get("eventAt"),event.at)
  assert.equal(closed.has("node"),false)
})
test("comparison entry and close retain the investigation window, filter, cursor and both selections",()=>{
  const a=schedulerRecordRef(event,from,to,"21"),b={...a,offset:"75"}
  const params=new URLSearchParams({from,to,filter:"decision",cursor:"100",limit:"50",uid:"service"})
  for(const [key,value] of schedulerRecordParams(a,"a"))params.set(key,value)
  for(const [key,value] of schedulerRecordParams(b,"b"))params.set(key,value)
  const next=schedulerComparisonParams(params)
  assert.equal(next.get("compare"),"1")
  assert.deepEqual(readSchedulerRecord(next,"a"),a)
  assert.deepEqual(readSchedulerRecord(next,"b"),b)
  next.delete("compare")
  assert.equal(next.toString(),params.toString())
})

test("power display rounds only presented values and translates duration policies",()=>{
  assert.equal(powerNumber(52.58421453765),"52.584")
  assert.equal(powerNumber(0),"0")
  assert.equal(powerNumber(undefined),"미제공")
  assert.deepEqual(powerPolicyFacts({
    powerSampling:{Interval:500000000n,Duration:5000898121n,RequestTimeout:1000000000n},
    powerCoverage:{MaximumBoundaryDistance:1000000000n,MinimumCoverage:0.9,MaximumGap:758787895n},
  }),[
    ["표본 간격 (초)","0.5"],["수집 기간 (초)","5.001"],["요청 제한 시간 (초)","1"],
    ["경계 허용 거리 (초)","1"],["최소 관측 비율 (%)","90"],["최대 표본 간격 기준 (초)","0.759"],
  ])
})

test("consecutive Keep judgments collapse without crossing a transition or losing the first record reference",()=>{
  const keep:SchedulerObservationSummary={at:event.at,decision:{at:event.at,status:"Keep"}}
  const rows=[keep,keep,event,keep].map((event,index)=>({event,ref:schedulerRecordRef(event,from,to,String(index))}))
  const grouped=groupHistoryKeeps(rows)
  assert.deepEqual(grouped.map(row=>row.keepCount),[2,0,1])
  assert.equal(grouped[0].ref.offset,"0")
  assert.equal(grouped[1].event,event)
})

test("filtered latest-first rows retain original append offset for evidence lookup",()=>{
  const ref=schedulerRecordRef({...event,offset:9007199254740993n},from,to,"0")
  assert.equal(ref.offset,"9007199254740993")
  const query=new URLSearchParams(schedulerRecordPath(ref).split("?")[1])
  assert.equal(query.get("cursor"),ref.offset)
  assert.equal(query.has("order"),false)
  assert.equal(query.has("filter"),false)
  assert.equal(query.has("uid"),false)
})

test("summary rows resolve to explicit compact detail while list requests default to summary",()=>{
  const summary:SchedulerObservationSummary={at:event.at,offset:21,action:{id:event.action!.id,kind:event.action!.kind,node:event.action!.node,phase:event.action!.phase,startedAt:event.action!.startedAt}}
  const ref=schedulerRecordRef(summary,from,to,"0")
  const query=new URLSearchParams(schedulerRecordPath(ref).split("?")[1])
  assert.equal(query.get("limit"),"1")
  assert.equal(query.get("cursor"),"21")
  assert.equal(query.get("view"),"compact")
  assert.equal(schedulerRecordMatches(ref,event),true)
})
test("selected journal restoration retains exact window and refuses same-ID replacement",()=>{
  const ref=schedulerRecordRef(event,from,to,"9007199254740993")
  assert.deepEqual(readSchedulerRecord(schedulerRecordParams(ref,"a"),"a"),ref)
  const query=new URLSearchParams(schedulerRecordPath(ref).split("?")[1])
  assert.deepEqual(Object.fromEntries(query),{from,to,limit:"1",cursor:"9007199254740993",view:"compact"})
  assert.equal(schedulerRecordMatches(ref,event),true)
  assert.equal(schedulerRecordMatches(ref,{...event,at:"2026-10-13T02:30:00.000000002Z"}),false)
  assert.equal(schedulerRecordMatches(ref,{...event,action:{...event.action!,node:{name:"node",uid:"uid-b"}}}),false)
  assert.equal(schedulerRecordMatches(ref,{...event,action:{...event.action!,startedAt:"2026-10-13T02:29:59.123456788Z"}}),false)
  assert.equal(schedulerRecordMatches(ref,undefined),false)
  const incomplete=schedulerRecordParams(ref);incomplete.delete("offset")
  assert.equal(readSchedulerRecord(incomplete),null)
})

test("cursor movement keeps submitted nano window and local filter while clearing selected record",()=>{
  const ref=schedulerRecordRef(event,from,to,"21"),params=schedulerRecordParams(ref)
  params.set("scope","scheduler");params.set("filter","power");params.set("cursor","20");params.set("aOffset","10")
  const next=schedulerPageParams(params,"40")
  assert.deepEqual(Object.fromEntries(next),{from,to,scope:"scheduler",filter:"power",cursor:"40",aOffset:"10"})
  assert.equal(schedulerPageParams(next).has("cursor"),false)
  assert.equal(params.get("offset"),"21")
})

test("experiment evidence filter selects only decision and action kinds and survives paging",()=>{
  const kinds=["decision","action","snapshot","settings","power","other"]
  assert.deepEqual(kinds.filter(kind=>schedulerHistoryKindMatches("decision-action",kind)),["decision","action"])
  assert.deepEqual(kinds.filter(kind=>schedulerHistoryKindMatches("all",kind)),kinds)
  assert.deepEqual(kinds.filter(kind=>schedulerHistoryKindMatches("decision",kind)),["decision"])
  assert.deepEqual(kinds.filter(kind=>schedulerHistoryKindMatches("action",kind)),["action"])
  const params=new URLSearchParams({from,to,filter:"decision-action",limit:"100",offset:"21",kind:"action",at:event.at,id:"same-action"})
  const next=schedulerPageParams(params,"100")
  assert.equal(next.get("filter"),"decision-action")
  assert.equal(next.get("from"),from)
  assert.equal(next.get("to"),to)
  assert.equal(next.get("cursor"),"100")
  assert.equal(next.has("kind"),false)
})


test("detail consumers with omitted view retain compact decision and settings evidence",()=>{
  const config=serverConfig({CHILL_BASE_URL:"https://chill.example",CHILL_NAMESPACE:"chill-system",CHILL_SYSTEM_NAME:"chill"})
  const queries={
    liveBoardDecision:new URLSearchParams({from,to,limit:"100"}),
    transitionRoutingReason:new URLSearchParams({from,to:new Date(Date.parse(from)+1).toISOString()}),
  }
  for(const [consumer,params] of Object.entries(queries)){
    const path=relayPlan("GET",["v1","observations","scheduler"],`?${params}`,config).path
    assert.equal(new URL(path,"https://chill.example").searchParams.get("view"),"compact",consumer)
  }
})
