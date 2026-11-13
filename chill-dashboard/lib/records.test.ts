import assert from "node:assert/strict"
import test from "node:test"
import { recordKind,recordKindParams } from "./records.ts"
import { schedulerPageParams } from "./scheduler-history.ts"

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
