import assert from "node:assert/strict"
import { test } from "node:test"
import { millisecondsToNanos, nanosToMilliseconds, parseSchedulerJSON, stringifySchedulerJSON, wireInteger } from "./scheduler.ts"

test("editing another setting preserves signed seed and maximum uint64 memory", () => {
  const settings = parseSchedulerJSON('{"seed":-9223372036854775808,"servingMemoryBytes":18446744073709551615,"controlIntervalNanos":9223372036854775807,"replays":1}') as Record<string,unknown>
  settings.replays = 2
  const saved = stringifySchedulerJSON(settings)
  assert.equal(saved,'{"seed":-9223372036854775808,"servingMemoryBytes":18446744073709551615,"controlIntervalNanos":9223372036854775807,"replays":2}')
  assert.deepEqual(parseSchedulerJSON(saved),settings)
  assert.deepEqual(parseSchedulerJSON('{"expectedJoules":null,"watts":0,"expectedNanos":1.25}'),{expectedJoules:null,watts:0,expectedNanos:1.25})
})
test("duration conversion preserves nanoseconds and rejects nonpositive or overflowing inputs", () => {
  assert.equal(millisecondsToNanos("9223372036854.775807"),9223372036854775807n)
  assert.equal(nanosToMilliseconds(9223372036854775807n),"9223372036854.775807")
  assert.equal(millisecondsToNanos("0.000001"),1n)
  for (const value of ["0","-1","0.0000001","1e3","9223372036854.775808"]) assert.throws(()=>millisecondsToNanos(value))
  assert.equal(wireInteger("18446744073709551615",0n,18446744073709551615n),18446744073709551615n)
  for (const value of ["-1","1.5","18446744073709551616"]) assert.throws(()=>wireInteger(value,0n,18446744073709551615n))
})
