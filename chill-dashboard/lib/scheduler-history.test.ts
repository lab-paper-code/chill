import assert from "node:assert/strict"
import test from "node:test"
import { schedulerRecordRef, schedulerRecordParams, readSchedulerRecord, schedulerRecordPath, schedulerRecordMatches, schedulerPageParams } from "./scheduler-history.ts"
import type { SchedulerObservation } from "./read-types.ts"

const from="2026-10-13T02:00:00.123456789Z",to="2026-10-13T03:00:00.987654321Z"
const event:SchedulerObservation={at:"2026-10-13T02:30:00.000000001Z",action:{id:"same-action",kind:"route",node:{name:"node",uid:"uid-a"},phase:"starting",startedAt:"2026-10-13T02:29:59.123456789Z"}}

test("selected journal restoration retains exact window and refuses same-ID replacement",()=>{
  const ref=schedulerRecordRef(event,from,to,"9007199254740993")
  assert.deepEqual(readSchedulerRecord(schedulerRecordParams(ref,"a"),"a"),ref)
  const query=new URLSearchParams(schedulerRecordPath(ref).split("?")[1])
  assert.deepEqual(Object.fromEntries(query),{from,to,limit:"1",cursor:"9007199254740993"})
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
