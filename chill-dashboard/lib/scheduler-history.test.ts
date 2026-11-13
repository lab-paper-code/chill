import assert from "node:assert/strict"
import test from "node:test"
import { schedulerRecordRef, schedulerRecordPath, schedulerRecordMatches } from "./scheduler-history.ts"
import type { SchedulerObservation, SchedulerObservationSummary } from "./read-types.ts"
import { relayPlan } from "./relay.ts"
import { serverConfig } from "./server-config.ts"

const from="2026-10-13T02:00:00.123456789Z",to="2026-10-13T03:00:00.987654321Z"
const event:SchedulerObservation={at:"2026-10-13T02:30:00.000000001Z",action:{id:"same-action",kind:"route",node:{name:"node",uid:"uid-a"},phase:"starting",startedAt:"2026-10-13T02:29:59.123456789Z"}}

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

import { readFileSync } from "node:fs"
import { decisionRange } from "./scheduler-history.ts"
import { experimentPath, type Experiment } from "./experiments.ts"
const recorded=JSON.parse(readFileSync(new URL("./fixtures/experiment-213.json",import.meta.url),"utf8")) as {publication:Experiment;decision:SchedulerObservation}

test("S1-4: archive judgment lookup pins nanosecond decision.at plus identity, never latest; absence differs from deadline",()=>{
  const at=recorded.decision.decision!.at,range=decisionRange(at)!
  assert.deepEqual(range,{from:"2026-10-28T01:34:28.365665918Z",to:"2026-10-28T01:34:28.365665919Z"})
  assert.deepEqual(decisionRange("2026-10-28T10:34:28.365665918+09:00"),range)
  assert.equal(decisionRange("2026-10-28T01:34:28.999999999Z")!.to,"2026-10-28T01:34:29.000000000Z")
  assert.equal(decisionRange("2026-02-30T01:00:00Z"),null)
  assert.equal(decisionRange("latest"),null)
  const config=serverConfig({CHILL_BASE_URL:"https://fixture.invalid",CHILL_NAMESPACE:"research",CHILL_SYSTEM_NAME:"fixture"})
  const plan=relayPlan("GET",["v1","observations","scheduler"],`?${new URLSearchParams({...range,limit:"20"})}`,config)
  assert.match(plan.path,/365665918Z/);assert.match(plan.path,/365665919Z/)
})

test("R9: selected published results use the experiment endpoint without stage filters; evidence uses only supported range filters",()=>{
  const config=serverConfig({CHILL_BASE_URL:"https://fixture.invalid",CHILL_NAMESPACE:"ops",CHILL_SYSTEM_NAME:"fixture"})
  assert.equal(experimentPath("control-222-223-first-20261030"),"v1/experiments/control-222-223-first-20261030")
  const result=relayPlan("GET",experimentPath(recorded.publication.id).split("/"),"",config)
  assert.equal(result.kind,"query")
  assert.throws(()=>relayPlan("GET",experimentPath(recorded.publication.id).split("/"),"?stage=ramp-up",config),/DashboardQueryInvalid/)
})

test("selected journal restoration retains exact window and refuses same-ID replacement",()=>{
  const ref=schedulerRecordRef(event,from,to,"9007199254740993")
  const query=new URLSearchParams(schedulerRecordPath(ref).split("?")[1])
  assert.deepEqual(Object.fromEntries(query),{from,to,limit:"1",cursor:"9007199254740993",view:"compact"})
  assert.equal(schedulerRecordMatches(ref,event),true)
  assert.equal(schedulerRecordMatches(ref,{...event,at:"2026-10-13T02:30:00.000000002Z"}),false)
  assert.equal(schedulerRecordMatches(ref,{...event,action:{...event.action!,node:{name:"node",uid:"uid-b"}}}),false)
  assert.equal(schedulerRecordMatches(ref,{...event,action:{...event.action!,startedAt:"2026-10-13T02:29:59.123456788Z"}}),false)
  assert.equal(schedulerRecordMatches(ref,undefined),false)
})
