import assert from "node:assert/strict"
import test from "node:test"
import { evaluationActions, evaluationAdoptDigests, evaluationCanSubmit, evaluationDigestChanges, evaluationOperation, evaluationPreflight, evaluationReleaseBody, evaluationSameSpec } from "./evaluation-request.ts"

test("evaluation submission requires successful checks for the unchanged body",()=>{
  const body='{"name":"new","spec":{}}'
  for(const value of [null,{}, {checks:[]},{checks:[{id:"target",passed:"true"}]}])assert.equal(evaluationPreflight(value),null)
  const failed=evaluationPreflight({checks:[{id:"target",passed:false,reason:"DigestMismatch"}]})
  assert.equal(evaluationCanSubmit(failed,body,body,false),false)
  const passed=evaluationPreflight({checks:[{id:"target",passed:true}]})
  assert.equal(evaluationCanSubmit(passed,body,body,false),true)
  assert.equal(evaluationCanSubmit(passed,body,body+" ",false),false)
  assert.equal(evaluationCanSubmit(passed,body,body,true),false)
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

test("live target digest differences are not adopted by default",()=>{
  const resource={apiVersion:"apps/v1",kind:"Deployment",namespace:"research",name:"scheduler"}
  const spec={target:{components:[{name:"scheduler",resource,image:"chill@sha256:old",sourceSHA:"old-source"},{name:"worker",resource:{...resource,name:"worker"},image:"worker@sha256:old"}]}}
  const live=[{component:"scheduler",resource:{name:"scheduler",namespace:"research",kind:"Deployment",apiVersion:"apps/v1"},specImage:"chill@sha256:old",liveImageIDs:["chill@sha256:new"],match:false},{component:"worker",resource:{...resource,name:"worker"},specImage:"worker@sha256:old",liveImageIDs:["worker@sha256:old"],match:true}]
  const changes=evaluationDigestChanges(spec,live)
  assert.deepEqual(changes,[{name:"scheduler",before:"chill@sha256:old",after:"chill@sha256:new",images:["chill@sha256:new"]}])
  assert.deepEqual(evaluationAdoptDigests(spec,changes),spec)
  const adopted=evaluationAdoptDigests(spec,changes,["scheduler"])
  assert.equal((adopted.target as typeof spec.target).components[0].image,"chill@sha256:new")
  assert.equal(spec.target.components[0].image,"chill@sha256:old")
  assert.equal((adopted.target as typeof spec.target).components[0].sourceSHA,"old-source")
  assert.deepEqual(evaluationDigestChanges(spec,{scheduler:"chill@sha256:new"}),[])
  const multiple=evaluationDigestChanges(spec,[{...live[0],liveImageIDs:["first","second"]}])
  assert.deepEqual(evaluationAdoptDigests(spec,multiple,["scheduler"]),spec)
  assert.deepEqual(evaluationDigestChanges(spec,[{...live[0],resource:{...resource,name:"different"}}]),[])
  const digest=`sha256:${"a".repeat(64)}`
  for(const imageID of [`containerd://${digest}`,`docker-pullable://registry/chill@${digest}`,digest]) {
    const current=evaluationDigestChanges(spec,[{...live[0],liveImageIDs:[imageID]}])
    assert.equal((evaluationAdoptDigests(spec,current,["scheduler"]).target as typeof spec.target).components[0].image,`chill@${digest}`)
  }
})

test("unknown evaluation result re-reads once without retrying the mutation",async()=>{
  let sends=0,reads=0
  const error=Object.assign(new Error("lost"),{unknownOutcome:true,status:504})
  await assert.rejects(evaluationOperation(async()=>{sends++;throw error},async()=>{reads++}),error)
  assert.equal(sends,1);assert.equal(reads,1)
  await assert.rejects(evaluationOperation(async()=>{throw Object.assign(new Error("denied"),{status:403})},async()=>{reads++}),/denied/)
  assert.equal(reads,1)
})

test("release confirmation requires a UID and nonblank operator reason",()=>{
  for(const [uid,reason] of [["","verified"],["uid",""],["uid"," \n "]])assert.throws(()=>evaluationReleaseBody(uid,reason),/필수/)
  assert.deepEqual(evaluationReleaseBody("uid","  복구 관측  "),{uid:"uid",reason:"복구 관측"})
})
