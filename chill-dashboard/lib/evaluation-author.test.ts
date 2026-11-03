import assert from "node:assert/strict"
import test from "node:test"
import { adoptWorkloads, canCreateLoad, canDeleteLoad, copyAuthorDraft, evaluationReplacement, object, parseSpec, patchSpec, renameLoadWorkload, removeLoadWorkload, specJSON, specValue, workloadChanges } from "./evaluation-author.ts"

test("JSON → form → JSON preserves unknown fields and exact integer evidence",()=>{
  const spec=parseSpec('{"question":"before","future":{"count":9007199254740993},"stages":[{"id":"candidate","row":"before","loadScenarioRef":{"name":"load","uid":"pinned","future":true},"future":{"enabled":false}}]}')
  const changed=patchSpec(spec,["stages",0,"row"],"after")
  const roundtrip=parseSpec(specJSON(changed))
  assert.equal(specValue(roundtrip,["stages",0,"row"]),"after")
  assert.equal(specValue(spec,["stages",0,"row"]),"before")
  assert.equal(specValue(roundtrip,["future","count"]),9007199254740993n)
  assert.deepEqual(specValue(roundtrip,["stages",0,"loadScenarioRef"]),{name:"load",uid:"pinned",future:true})
  assert.deepEqual(specValue(roundtrip,["stages",0,"future"]),{enabled:false})
  assert.throws(()=>parseSpec("[]"))
  assert.throws(()=>parseSpec('{"question":'))
})

test("copy resets run identity, leaves pinned referenced UIDs and source untouched",()=>{
  const source={name:"old-run",uid:"run-uid",spec:{stages:[{loadScenarioRef:{name:"load",uid:"load-uid"}}],future:{v:1}}}
  const draft=copyAuthorDraft(source)
  assert.equal(draft.name,"")
  assert.equal("uid" in draft,false)
  assert.equal(specValue(draft.spec,["stages",0,"loadScenarioRef","uid"]),"load-uid")
  object(draft.spec.future).v=2
  assert.equal(source.spec.future.v,1)
})

test("live Workload differences remain unapplied until explicitly selected",()=>{
  const spec={workloads:[{name:"w",uid:"old",generation:1,future:"keep"},{name:"other",uid:"other-uid",generation:2}],future:true}
  const changes=workloadChanges(spec,{w:{uid:"current",generation:3},other:{uid:"other-uid",generation:2}})
  assert.equal(changes.length,1)
  assert.deepEqual(adoptWorkloads(spec,changes),spec)
  assert.deepEqual(adoptWorkloads(spec,changes,["w"]),{...spec,workloads:[{name:"w",uid:"current",generation:3,future:"keep"},spec.workloads[1]]})
  assert.equal(spec.workloads[0].uid,"old")
  const stale=patchSpec(spec,["workloads",0,"uid"],"edited")
  assert.equal(specValue(adoptWorkloads(stale,changes,["w"]),["workloads",0,"uid"]),"edited")
})

test("inUse blocks deletion and failed or stale preflight never enables create",()=>{
  const load={name:"load",uid:"uid",spec:{},inUse:[] as string[]}
  assert.equal(canDeleteLoad(load),true)
  assert.equal(canDeleteLoad({...load,inUse:["running"]}),false)
  assert.equal(canDeleteLoad({...load,uid:""}),false)
  const good={checks:[{id:"server",passed:true}]}
  assert.equal(canCreateLoad(good,"same","same",false),true)
  assert.equal(canCreateLoad({checks:[{id:"server",passed:false}]},"same","same",false),false)
  assert.equal(canCreateLoad(good,"before","after",false),false)
  assert.equal(canCreateLoad(good,"same","same",true),false)
  assert.equal(canCreateLoad(null,"same","same",false),false)
})

test("Workload rename migrates form maps without losing extension fields",()=>{
  const original={workloads:[{name:"old",uid:"uid",generation:1}],arrivalDistribution:{old:"poisson"},seeds:{old:"18446744073709551615"},datasetRefs:{old:{name:"dataset",uid:"dataset-uid"}},traceRefs:{old:{store:"s",key:"trace",size:1}},parts:[{id:"p",durationSeconds:2,rates:{old:3},future:"retained"}]}
  const renamed=renameLoadWorkload(original,0,"new")
  assert.equal(specValue(renamed,["parts",0,"rates","new"]),3)
  assert.equal(specValue(renamed,["parts",0,"rates","old"]),undefined)
  assert.equal(specValue(renamed,["arrivalDistribution","new"]),"poisson")
  assert.deepEqual(specValue(renamed,["traceRefs","new"]),original.traceRefs.old)
  assert.deepEqual(renamed.seeds,{new:"18446744073709551615"})
  assert.deepEqual(renamed.datasetRefs,{new:original.datasetRefs.old})
  assert.equal(specValue(renamed,["parts",0,"future"]),"retained")
  assert.equal(original.workloads[0].name,"old")
})

test("PUT reconciliation distinguishes new UID, old UID, deleted object and unknown",()=>{
  const draft={future:{exact:9007199254740993n},question:"kept"}
  assert.deepEqual(evaluationReplacement("old",draft,200,{uid:"new",spec:{question:"kept",future:{exact:9007199254740993n}}}),{state:"updated",uid:"new"})
  assert.deepEqual(evaluationReplacement("old",draft,200,{uid:"old",spec:draft}),{state:"unchanged"})
  assert.deepEqual(evaluationReplacement("old",draft,404,null),{state:"deleted"})
  assert.deepEqual(evaluationReplacement("old",draft,503,null),{state:"unknown"})
  assert.deepEqual(evaluationReplacement("old",draft,200,{uid:"new",spec:{question:"different"}}),{state:"unknown"})
  assert.deepEqual(draft,{future:{exact:9007199254740993n},question:"kept"})
})

test("form map keys cannot mutate object prototypes",()=>{
  const spec=patchSpec({},["arrivalDistribution","__proto__","injected"],true)
  assert.equal(specValue(spec,["arrivalDistribution","__proto__","injected"]),true)
  assert.equal(Object.hasOwn(Object.prototype,"injected"),false)
})

test("removing a Workload clears only its declared load maps",()=>{
  const spec={workloads:[{name:"a"},{name:"b"}],arrivalDistribution:{a:"poisson",b:"poisson"},seeds:{a:"1",b:"2"},datasetRefs:{a:{name:"a",uid:"a"},b:{name:"b",uid:"b"}},traceRefs:{a:{key:"a"},b:{key:"b"}},parts:[{rates:{a:1,b:2},future:true}],future:{a:"keep"}}
  const next=removeLoadWorkload(spec,0)
  assert.deepEqual(next,{workloads:[{name:"b"}],arrivalDistribution:{b:"poisson"},seeds:{b:"2"},datasetRefs:{b:{name:"b",uid:"b"}},traceRefs:{b:{key:"b"}},parts:[{rates:{b:2},future:true}],future:{a:"keep"}})
  assert.equal(spec.workloads.length,2)
})

test("colliding Workload rename is rejected atomically before a later valid rename",()=>{
  const spec={workloads:[{name:"a",uid:"a"},{name:"b",uid:"b"}],arrivalDistribution:{a:"poisson-a",b:"poisson-b"},traceRefs:{a:{key:"a"},b:{key:"b"}},parts:[{rates:{a:1,b:2}},{rates:{a:3,b:4}}]}
  const rejected=renameLoadWorkload(spec,0,"b")
  assert.deepEqual(rejected,spec)
  const renamed=renameLoadWorkload(rejected,0,"c")
  assert.deepEqual(renamed,{workloads:[{name:"c",uid:"a"},{name:"b",uid:"b"}],arrivalDistribution:{c:"poisson-a",b:"poisson-b"},traceRefs:{c:{key:"a"},b:{key:"b"}},parts:[{rates:{c:1,b:2}},{rates:{c:3,b:4}}]})
})

test("rename rejects occupied seed and dataset map keys without partially updating the draft",()=>{
  for(const map of ["seeds","datasetRefs"]) {
    const spec={workloads:[{name:"old",uid:"uid"}],parts:[{rates:{old:5}}],[map]:{old:"pinned",taken:"occupied"}}
    assert.equal(renameLoadWorkload(spec,0,"taken"),spec)
    assert.equal(specValue(spec,["parts",0,"rates","old"]),5)
  }
})
