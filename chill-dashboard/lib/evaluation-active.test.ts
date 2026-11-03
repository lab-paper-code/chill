import assert from "node:assert/strict"
import { test } from "node:test"
import type { LiveEvent } from "./live.ts"
import { evaluationPoolBlocked, evaluationInjectionMatch, evaluationRemaining, evaluationStopPath, type ActiveEvaluation } from "./evaluation-active.ts"

const at="2026-11-01T00:00:00Z",now=Date.parse(at)
const run:ActiveEvaluation={name:"evaluation",uid:"uid-1",stageIndex:1,stagesTotal:3,stage:{id:"base",plannedSeconds:300,injections:[{id:"kill",action:"delete-serving-pod",offsetSeconds:120,nodeName:"node-1",podName:"serving-1"}]}}
const cluster=(pod:{uid:string;name?:string;node?:string}):LiveEvent=>({seq:1,at,type:"event",kind:"cluster",cluster:{kind:"pod",at,event:"deleted",pod}})

test("remaining duration uses load goAt, not stage start, and waits before goAt exists",()=>{
  assert.equal(evaluationRemaining(undefined,now),null)
  assert.equal(evaluationRemaining({...run.stage!,startedAt:at},now),null)
  const stage={...run.stage!,startedAt:"2026-10-31T23:59:00Z",goAt:at}
  assert.equal(evaluationRemaining(stage,now),300)
  assert.equal(evaluationRemaining(stage,now+1001),299)
  assert.equal(evaluationRemaining(stage,now+301000),0)
})

test("injection tags require exact node or pod names from actual event fields",()=>{
  assert.equal(evaluationInjectionMatch(cluster({uid:"other",name:"serving-1"}),[run]),true)
  assert.equal(evaluationInjectionMatch(cluster({uid:"other",node:"node-1"}),[run]),true)
  for(const pod of [{uid:"serving-1"},{uid:"node-1"},{uid:"other",name:"serving-10",node:"node-10"},{uid:"other",name:"Serving-1",node:"Node-1"},{uid:"other",name:"",node:""}])assert.equal(evaluationInjectionMatch(cluster(pod),[run]),false)
  assert.equal(evaluationInjectionMatch(cluster({uid:"other",name:"serving-1"}),[]),false)
  const action:LiveEvent={seq:2,at,type:"event",kind:"action",action:{id:"a",kind:"start",node:{uid:"different",name:"node-1"},workloadUID:"w",status:"running",phase:"started",startedAt:at,expectedSeconds:null,expectedSource:null}}
  assert.equal(evaluationInjectionMatch(action,[run]),true)
  assert.equal(evaluationInjectionMatch({...action,action:{...action.action,node:{uid:"node-1",name:"different"}}},[run]),false)
  assert.equal(evaluationInjectionMatch({seq:3,at,type:"event",kind:"decision",decision:{at,state:"Changed",reason:"node-1",explanation:"serving-1"}},[run]),false)
  assert.equal(evaluationInjectionMatch(cluster({uid:"other",name:"serving-1"}),[{...run,stage:undefined}]),false)
})

test("stop always supplies the selected UID condition and encodes the name",()=>{
  assert.equal(evaluationStopPath(run),"v1/evaluations/evaluation?uid=uid-1")
  assert.equal(evaluationStopPath({name:"name/other",uid:"uid&other"}),"v1/evaluations/name%2Fother?uid=uid%26other")
})

test("pool warning requires the active UID and Blocked phase, not matching name",()=>{
  assert.equal(evaluationPoolBlocked(run.uid,[{name:run.name,uid:run.uid,phase:"Blocked"}]),true)
  assert.equal(evaluationPoolBlocked(run.uid,[{name:run.name,uid:"replacement",phase:"Blocked"}]),false)
  for(const phase of ["Pending","Running","Finishing","Done"])assert.equal(evaluationPoolBlocked(run.uid,[{name:run.name,uid:run.uid,phase}]),false)
  assert.equal(evaluationPoolBlocked(run.uid,[]),false)
})
