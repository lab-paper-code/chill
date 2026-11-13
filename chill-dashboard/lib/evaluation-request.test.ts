import assert from "node:assert/strict"
import test from "node:test"
import { evaluationActions, evaluationCanSubmit, evaluationPreflight, evaluationReleaseBody, evaluationSameSpec } from "./evaluation-request.ts"

test("evaluation submission requires successful checks for the unchanged body",()=>{
  const body='{"name":"new","spec":{}}'
  for(const value of [null,{}, {checks:[]},{checks:[{id:"target",passed:"true"}]}])assert.equal(evaluationPreflight(value),null)
  const failed=evaluationPreflight({checks:[{id:"target",passed:false,reason:"TargetUnavailable"}]})
  assert.equal(evaluationCanSubmit(failed,body,body,false),false)
  const passed=evaluationPreflight({checks:[{id:"target",passed:true}]})
  assert.equal(evaluationCanSubmit(passed,body,body,false),true)
  assert.equal(evaluationCanSubmit(passed,body,body+" ",false),false)
  assert.equal(evaluationCanSubmit(passed,body,body,true),false)
})

test("preflight validates blackbox target observations without a spec image match",()=>{
  const target={component:"chill",resource:{apiVersion:"apps/v1",kind:"Deployment",namespace:"research",name:"chill"},liveImageIDs:["containerd://sha256:observed"],ready:false}
  const checks=[{id:"target/chill",passed:false}]
  assert.deepEqual(evaluationPreflight({checks,liveTarget:[target]})?.liveTarget,[target])
  for(const liveTarget of [{},[{}],[{...target,ready:undefined}],[{...target,liveImageIDs:[null]}],[{...target,resource:{kind:"Deployment"}}]])assert.equal(evaluationPreflight({checks,liveTarget}),null)
})

test("re-read spec confirmation ignores object key order but retains stage order and exact integers",()=>{
  assert.equal(evaluationSameSpec({stages:[{id:"a",value:9007199254740993n}]},{stages:[{value:9007199254740993n,id:"a"}]}),true)
  assert.equal(evaluationSameSpec({stages:["a","b"]},{stages:["b","a"]}),false)
  assert.equal(evaluationSameSpec({value:9007199254740993n},{value:9007199254740992n}),false)
})

test("evaluation phases restrict modification and expose blocked release",()=>{
  assert.deepEqual(evaluationActions("Pending"),{edit:true,remove:"삭제",release:false})
  for(const phase of ["Running","Finishing"])assert.deepEqual(evaluationActions(phase),{edit:false,remove:"중지",release:false})
  assert.deepEqual(evaluationActions("Done"),{edit:false,remove:"삭제",release:false})
  assert.deepEqual(evaluationActions("Blocked"),{edit:false,remove:"중지",release:true})
})

test("release confirmation requires a UID and nonblank operator reason",()=>{
  for(const [uid,reason] of [["","verified"],["uid",""],["uid"," \n "]])assert.throws(()=>evaluationReleaseBody(uid,reason),/필수/)
  assert.deepEqual(evaluationReleaseBody("uid","  복구 관측  "),{uid:"uid",reason:"복구 관측"})
})
