import assert from "node:assert/strict"
import { test } from "node:test"
import { resolveWorkloadIdentities } from "./workload-identity.ts"
import type { ObservationDefinition, SchedulerDefinition } from "./read-types.ts"

test("retained definitions name deleted UIDs and preserve each request's historical model", () => {
  const old:SchedulerDefinition = {uid:"deleted-uid",name:"삭제한 서비스",generation:1,model:{name:"old-model",uid:"old",generation:1,paths:[]},slo:{percentile:99,targetLatencyNanos:100}}
  const changed:SchedulerDefinition = {...old,generation:2,model:{...old.model,name:"new-model",uid:"new"}}
  const definitions:ObservationDefinition[] = [{identity:"old-definition",definition:old},{identity:"new-definition",definition:changed}]
  const resolved = resolveWorkloadIdentities([],definitions,[],[old.uid,"unknown-uid"])
  assert.equal(resolved.get(old.uid),old.name)
  assert.equal(resolved.get("unknown-uid"),"이름 미확인 (unknown-)")
  const snapshot = {...old,name:"판단 시점 이름"}
  assert.equal(resolveWorkloadIdentities([],[],[snapshot]).get(old.uid),snapshot.name)
  assert.equal(resolveWorkloadIdentities([],definitions,[snapshot]).get(old.uid),old.name)
  const current = [{desired:{metadata:{uid:old.uid,name:"현재 이름"}}}]
  assert.equal(resolveWorkloadIdentities(current,definitions,[snapshot]).get(old.uid),"현재 이름")
  // Request details join definitionID, never use the current registration's model.
  for (const [definitionID,model] of [["old-definition","old-model"],["new-definition","new-model"]]) {
    assert.equal(definitions.find(item => item.identity === definitionID)?.definition.model.name,model)
  }
})
