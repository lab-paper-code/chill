import assert from "node:assert/strict"
import test from "node:test"
import { decodeExperiment } from "./experiments.ts"

test("cleanup residuals survive publication decoding without accepting malformed rows",()=>{
  const residual={kind:"Node",name:"worker-1",reason:"CordonNotRestored"}
  const run=decodeExperiment(JSON.stringify({id:"cleanup-failed",outcome:{cleanup:{status:"Failed",reason:"CleanupFailed",residuals:[residual,{kind:"Pod",name:9,reason:"bad"}]}}}))
  assert.deepEqual(run.outcome?.cleanup?.residuals,[residual])
  assert.equal(run.outcome?.cleanup?.status,"Failed")
})
