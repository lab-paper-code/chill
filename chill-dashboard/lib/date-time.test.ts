import assert from "node:assert/strict"
import test from "node:test"
import { localDateTime, utcDateTime } from "./date-time.ts"

test("date edits reject empty, partial and unrepresentable dates and convert valid local time",()=>{
  for(const value of ["","2026-10-14T","invalid","999999-10-14T15:00"]){
    assert.equal(utcDateTime(value),"")
    assert.equal(localDateTime(value),"")
  }
  const previousTZ=process.env.TZ
  process.env.TZ="Asia/Seoul"
  try {
    assert.equal(utcDateTime("2026-10-14T15:00"),"2026-10-14T06:00:00.000Z")
    assert.equal(localDateTime("2026-10-14T06:09:18.350814Z"),"2026-10-14T15:09")
  } finally {
    if(previousTZ===undefined)delete process.env.TZ
    else process.env.TZ=previousTZ
  }
})
