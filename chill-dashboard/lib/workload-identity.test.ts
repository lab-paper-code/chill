import assert from "node:assert/strict"
import { test } from "node:test"
import { resolveWorkloadIdentities } from "./workload-identity.ts"
import type { ObservationDefinition, SchedulerDefinition } from "./read-types.ts"

test("retained definitions name deleted UIDs and preserve each request's historical model", () => {
  const old:SchedulerDefinition = {uid:"deleted-uid",name:"삭제한 서비스",generation:1,model:{name:"old-model",uid:"old",generation:1,paths:[]},slo:{percentile:99,targetLatencyNanos:100}}
  const changed:SchedulerDefinition = {...old,generation:2,model:{...old.model,name:"new-model",uid:"new"}}
  const definitions:ObservationDefinition[] = [{identity:"old-definition",definition:old},{identity:"new-definition",definition:changed}]
  const resolved = resolveWorkloadIdentities([],definitions,[],[old.uid,"unknown-uid"])
  assert.deepEqual(resolved.get(old.uid),{name:old.name,model:"new-model",source:"definition"})
  assert.deepEqual(resolved.get("unknown-uid"),{name:"이름 미확인 (unknown-)",model:"모델 미확인",source:"unknown"})
  assert.equal(resolveWorkloadIdentities([],[],[old]).get(old.uid)?.source,"snapshot")
  const current = [{desired:{metadata:{uid:old.uid,name:old.name},spec:{modelRef:{name:"current-model"}}}}]
  assert.equal(resolveWorkloadIdentities(current,definitions,[old]).get(old.uid)?.model,"current-model")
  // Request details join definitionID, never use the current registration's model.
  for (const [definitionID,model] of [["old-definition","old-model"],["new-definition","new-model"]]) {
    const requestDefinitions = definitions.filter(item => item.identity === definitionID)
    assert.equal(resolveWorkloadIdentities([],requestDefinitions).get(old.uid)?.model,model)
  }
})
