import assert from "node:assert/strict"
import { test } from "node:test"
import { safeSource } from "./source-export.ts"
import { parseSchedulerJSON, stringifySchedulerJSON } from "./scheduler.ts"

test("source export removes nested credentials without mutating its source", () => {
  const source = {uid:"registration-1",from:"2026-10-07T00:00:00.123456789Z",definitionFilter:["registration-1/2/model-1/3"],outcomeFilter:["timeout"],cursor:"opaque-page",samplePopulation:"server cohort",quantileNanos:null,secretKey:"secret-value",authorization:"Bearer private",nested:[{password:"private",sourceRef:{store:"evidence",key:"raw/key"},url:"https://store.invalid/raw?X-Amz-Credential=private"}]}
  const exported = safeSource(source) as typeof source
  assert.doesNotMatch(JSON.stringify(exported),/secret-value|Bearer private|X-Amz-Credential=private/)
  assert.equal(source.secretKey,"secret-value")
})

test("redacted scheduler export preserves exact integers and null versus zero", () => {
  const exported = stringifySchedulerJSON(safeSource({seed:-9223372036854775808n,memoryBytes:18446744073709551615n,powerJoules:null,watts:0,token:"private"}))
  assert.deepEqual(parseSchedulerJSON(exported),{seed:-9223372036854775808n,memoryBytes:18446744073709551615n,powerJoules:null,watts:0,token:"[비밀정보 제거]"})
})
