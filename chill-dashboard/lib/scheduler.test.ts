import assert from "node:assert/strict"
import { test } from "node:test"
import { millisecondsToNanos, nanosToMilliseconds, parseSchedulerJSON, stringifySchedulerJSON, wireInteger, settingsEqual } from "./scheduler.ts"
import type { SchedulerSettings } from "./read-types.ts"

test("editing another setting preserves maximum int64 and uint64 integers", () => {
  const settings = parseSchedulerJSON('{"servingMemoryBytes":18446744073709551615,"controlIntervalNanos":9223372036854775807,"maxReplicas":1}') as Record<string,unknown>
  settings.maxReplicas = 2
  const saved = stringifySchedulerJSON(settings)
  assert.equal(saved,'{"servingMemoryBytes":18446744073709551615,"controlIntervalNanos":9223372036854775807,"maxReplicas":2}')
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

test("review detects control or exact integer changes across numeric representations", () => {
  const occupancy={node:"rasp5",runtimeImage:"image",perRequestNanos:9409000,source:"m4"}
  const current:SchedulerSettings={observationWindowNanos:1,controlIntervalNanos:1,horizonNanos:1,maxObservationAgeNanos:1,servingMemoryBytes:18446744073709551615n,maxReplicas:1,maxInstances:1,maxCPUs:1,completionMarginMS:0,capacityWindowNanos:1,capacityWindows:1,capacityPassingWindows:1,capacityMinimumRequests:1,dispatchOccupancy:[occupancy],autonomousWorkloads:[],controls:{placement:false,resources:false,replicas:false,routing:false,batching:false,power:false,membership:false}}
  assert.equal(settingsEqual(current,{...current,controlIntervalNanos:1n,controls:{...current.controls}}),true)
  assert.equal(settingsEqual(current,{...current,servingMemoryBytes:18446744073709551614n}),false)
  assert.equal(settingsEqual(current,{...current,controls:{...current.controls,power:true}}),false)
  assert.equal(settingsEqual(current,{...current,dispatchOccupancy:[{...occupancy,perRequestNanos:9409001n}]}),false)
})

test("compact scheduler reads restore shared evidence into the full response", () => {
  const compact = '{"configVersion":"1","state":{"snapshot":{"instances":[{"id":"a","profile":{"$ref":"p"}},{"id":"b","profile":{"$ref":"p"}}]},"decision":{"keep":{"instances":[{"profile":{"$ref":"p"}}]},"target":null}},"profiles":{"p":{"id":"p","energy":12345678901234567890}}}'
  type Instance = {profile:{id:string;energy:bigint}}
  const full = parseSchedulerJSON(compact) as {state:{snapshot:{instances:Instance[]};decision:{keep:{instances:Instance[]}}}}
  assert.deepEqual(Object.keys(full), ["configVersion","state"])
  assert.equal(full.state.snapshot.instances[0].profile.energy, 12345678901234567890n)
  assert.equal(full.state.snapshot.instances[1].profile.id, "p")
  assert.equal(full.state.decision.keep.instances[0].profile.id, "p")
  assert.throws(() => parseSchedulerJSON(compact.replace('"$ref":"p"}},{', '"$ref":"missing"}},{')), /missing/)
  assert.deepEqual(parseSchedulerJSON('{"profile":{"$ref":"p"}}'), {profile:{$ref:"p"}})
  assert.deepEqual(parseSchedulerJSON('{"profiles":[{"id":"p"}]}'), {profiles:[{id:"p"}]})
})
