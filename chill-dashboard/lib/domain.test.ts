import assert from "node:assert/strict"
import test from "node:test"
import { positiveIntegers, rawInferenceBody, rawOutputs, workloadSpec } from "./domain.ts"

test("forms preserve arbitrary SLO and one raw sample per preprocessor input field", () => {
  const admission = { policy: "Reject", maxQueuedRequests: 1024 } as const
  assert.deepEqual(workloadSpec("shared-model", "imagenet", "", "99.5", "+1h2m3.000000001s", admission), { modelRef: { name: "shared-model" }, preprocessorRef: { name: "imagenet" }, slo: { percentile: 99.5, targetLatency: "+1h2m3.000000001s" }, admission })
  assert.deepEqual(workloadSpec("shared-model", "imagenet", "vqa", "99", "1s", admission).datasetRef, { name: "vqa" })
  assert.throws(() => workloadSpec("shared-model", "", "vqa", "99", "1s", admission))
  for (const value of ["", "0", "100.1", "NaN", "Infinity"]) assert.throws(() => workloadSpec("shared-model", "imagenet", "", value, "100ms", admission))
  assert.deepEqual(positiveIntegers("1, 224 224"), [1, 224, 224])
  for (const value of ["", "0", "-1", "1.2", "2147483648"]) assert.throws(() => positiveIntegers(value))

  const fields = [{ name: "image", format: "JPEG", maxBytes: 4 }, { name: "question", format: "UTF8", maxBytes: 16 }] as const
  const values = { image: new Uint8Array([0xff, 0xd8, 0xff, 0xe0]), question: new TextEncoder().encode("무엇?") }
  assert.equal(rawInferenceBody([...fields], values), '{"inputs":[{"name":"image","datatype":"BYTES","data":"/9j/4A=="},{"name":"question","datatype":"BYTES","data":"66y07JeHPw=="}]}')
  assert.throws(() => rawInferenceBody([], values))
  assert.throws(() => rawInferenceBody([...fields], { image: values.image }))
  assert.throws(() => rawInferenceBody([...fields], { ...values, question: new Uint8Array() }))
  assert.throws(() => rawInferenceBody([...fields], { ...values, image: new Uint8Array(5) }))

  assert.deepEqual(rawOutputs([{ name: "answer", format: "UTF8" }, { name: "mask", format: "JPEG" }, { name: "missing", format: "UTF8" }], { outputs: [{ name: "answer", data: ["66y07JeHPw=="] }, { name: "mask", data: ["/9j/4A=="] }] }), [
    { name: "answer", format: "UTF8", value: "무엇?" }, { name: "mask", format: "JPEG", value: "/9j/4A==" }, { name: "missing", format: "UTF8", value: undefined },
  ])
})

test("workload admission validates locally while final deadline ranges belong to the server", () => {
  const admission = { policy: "Delay", maxQueuedRequests: 17 }
  const build = (lifetime: string, options = admission) => workloadSpec("model", "input", "", "99", "1s", options, lifetime)
  for (const lifetime of ["+1m2.000000001s", ".5s", "1.s", "1µs", "1μs", "0", "-1s", "130s"]) assert.equal(build(lifetime).requestLifetime, lifetime)
  assert.equal(Object.hasOwn(build(""), "requestLifetime"), false)
  for (const lifetime of [" ", "1", "1day", "1 s", "1s trailing", "1s-2s", "NaNs", "Infinitys", ".s"]) assert.throws(() => build(lifetime), /요청 최종 기한/)
  for (const policy of ["", "reject", "Keep", "toString"]) assert.throws(() => build("", { ...admission, policy }), /과부하 정책/)
  for (const maxQueuedRequests of [0, -1, 1.5, NaN, Infinity]) assert.throws(() => build("", { ...admission, maxQueuedRequests }), /대기 상한/)
  assert.equal(build("", { ...admission, maxQueuedRequests: 1 }).admission.maxQueuedRequests, 1)
})
