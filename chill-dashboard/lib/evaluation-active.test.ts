import assert from "node:assert/strict"
import { test } from "node:test"

import { evaluationRemaining, type ActiveEvaluation } from "./evaluation-active.ts"

const at="2026-11-01T00:00:00Z",now=Date.parse(at)
const run:ActiveEvaluation={name:"evaluation",uid:"uid-1",stageIndex:1,stagesTotal:3,stage:{id:"base",plannedSeconds:300,injections:[{id:"kill",action:"delete-serving-pod",offsetSeconds:120,nodeName:"node-1",podName:"serving-1"}]}}

test("remaining duration uses load goAt, not stage start, and waits before goAt exists",()=>{
  assert.equal(evaluationRemaining(undefined,now),null)
  assert.equal(evaluationRemaining({...run.stage!,startedAt:at},now),null)
  const stage={...run.stage!,startedAt:"2026-10-31T23:59:00Z",goAt:at}
  assert.equal(evaluationRemaining(stage,now),300)
  assert.equal(evaluationRemaining(stage,now+1001),299)
  assert.equal(evaluationRemaining(stage,now+301000),0)
})
