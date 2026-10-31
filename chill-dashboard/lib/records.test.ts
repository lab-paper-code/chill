import assert from "node:assert/strict"
import test from "node:test"
import { recordKind,recordKindParams } from "./records.ts"
import { schedulerPageParams,readSchedulerRecord,schedulerRecordParams } from "./scheduler-history.ts"
import { dashboardDestination } from "./dashboard-routes.ts"

test("kind changes retain exact range but never reuse another API cursor or selection",()=>{
  const source=new URLSearchParams("from=2026-10-31T01%3A00%3A00.123456789Z&to=2026-10-31T02%3A00%3A00.987654321Z&uid=u&limit=81&cursor=opaque&offset=3&kind=decision&at=exact&id=d&definition=a&definition=b&compare=1")
  for(const kind of ["judgments","requests","power"]){const next=recordKindParams(source,kind);assert.equal(recordKind(next),kind);for(const key of ["from","to","uid","limit"])assert.equal(next.get(key),source.get(key));for(const key of ["cursor","offset","at","id","definition","compare"])assert.equal(next.has(key),false)}
  assert.equal(source.get("cursor"),"opaque")
})
test("power paging keeps kind, submitted range and node filter and clears only selected identity",()=>{
  const source=new URLSearchParams("kind=power&from=nanosecond-from&to=nanosecond-to&offset=31&at=event&id=node&filterNode=node&limit=17&cursor=30")
  const next=schedulerPageParams(source,"60")
  assert.deepEqual(Object.fromEntries(next),{kind:"power",from:"nanosecond-from",to:"nanosecond-to",filterNode:"node",limit:"17",cursor:"60"})
})
test("legacy selected records preserve exact identity through redirect and paging preserves API scope",()=>{
  const ref={from:"2026-10-31T01:00:00.123456789Z",to:"2026-10-31T02:00:00.987654321Z",offset:"9007199254740993",kind:"action",at:"exact-event",id:"action/id",node:"node",startedAt:"exact-start"}
  const params=schedulerRecordParams(ref);params.set("cursor","9007199254740990");params.set("limit","81")
  for(const path of ["/scheduler/history","/transitions/action%2Fid"]){const target=new URL(dashboardDestination(path,params)!,"https://dashboard.invalid");assert.equal(target.pathname,"/records");assert.deepEqual(readSchedulerRecord(target.searchParams),ref);assert.equal(target.searchParams.get("cursor"),params.get("cursor"));assert.equal(target.searchParams.get("limit"),"81")}
  for(const [path,kind] of [["/observations","requests"],["/scheduler/power","power"]]){const target=new URL(dashboardDestination(path,params)!,"https://dashboard.invalid");assert.equal(target.searchParams.get("kind"),kind);assert.equal(target.searchParams.get("from"),ref.from);assert.equal(target.searchParams.get("to"),ref.to);assert.equal(target.searchParams.get("cursor"),params.get("cursor"))}
})
test("service section redirects preserve query and use canonical detail anchors",()=>{
  for(const [section,anchor] of [["definition","definition"],["evidence","definition"],["placement","placement"],["operation","placement"],["changes","application"]]){const target=new URL(dashboardDestination(`/workloads/a%2Fb/${section}`,new URLSearchParams("uid=old&filter=a&filter=b"))!,"https://dashboard.invalid");assert.equal(target.pathname,"/workloads/a%2Fb");assert.equal(target.hash,`#${anchor}`);assert.deepEqual(target.searchParams.getAll("filter"),["a","b"]);assert.equal(target.searchParams.get("uid"),"old")}
})
