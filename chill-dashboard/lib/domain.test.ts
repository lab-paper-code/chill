import assert from "node:assert/strict"
import test from "node:test"
import { inferenceBody, positiveIntegers, tensorData, workloadSpec, type Datatype, type TensorContract } from "./domain.ts"

test("forms preserve arbitrary SLO and lossless single-sample typed tensor input", () => {
  assert.deepEqual(workloadSpec("shared-model", "imagenet", "", "99.5", "+1h2m3.000000001s"), { modelRef: { name: "shared-model" }, preprocessorRef: { name: "imagenet" }, slo: { percentile: 99.5, targetLatency: "+1h2m3.000000001s" } })
  assert.deepEqual(workloadSpec("shared-model", "imagenet", "vqa", "99", "1s").datasetRef, { name: "vqa" })
  assert.throws(() => workloadSpec("shared-model", "", "vqa", "99", "1s"))
  for (const value of ["", "0", "100.1", "NaN", "Infinity"]) assert.throws(() => workloadSpec("shared-model", "imagenet", "", value, "100ms"))
  assert.deepEqual(positiveIntegers("1, 224 224"), [1, 224, 224])
  for (const value of ["", "0", "-1", "1.2", "2147483648"]) assert.throws(() => positiveIntegers(value))
  const tensor = (datatype: Datatype, count = 1): TensorContract => ({ name: "input", datatype, sampleShape: [count] })
  const limits: [Datatype, string, string][] = [
    ["INT8", "-128", "128"], ["UINT8", "255", "256"], ["INT16", "-32768", "32768"], ["UINT16", "65535", "65536"],
    ["INT32", "-2147483648", "2147483648"], ["UINT32", "4294967295", "4294967296"],
    ["INT64", "-9223372036854775808", "9223372036854775808"], ["UINT64", "18446744073709551615", "18446744073709551616"],
    ["FP16", "65504", "65505"], ["FP32", "3.4e38", "3.5e38"], ["FP64", "1.7e308", "1e309"],
  ]
  for (const [datatype, accepted, rejected] of limits) {
    assert.equal(tensorData(tensor(datatype), `[${accepted}]`), `[${accepted}]`)
    assert.throws(() => tensorData(tensor(datatype), `[${rejected}]`))
  }
  assert.equal(tensorData(tensor("BOOL", 2), "[true,false]"), "[true,false]")
  assert.equal(tensorData(tensor("BYTES", 2), '["a,b", "한글"]'), '["a,b", "한글"]')
  for (const [datatype, data] of [["BOOL", "[1]"], ["BYTES", "[null]"], ["INT64", "[1e0]"], ["UINT64", "[-0]"], ["FP32", '[[1]]'], ["FP32", '["1"]'], ["FP32", "[]"], ["FP32", "[1,2]"], ["BF16", "[0]"]] as const) assert.throws(() => tensorData(tensor(datatype), data))
  const body = inferenceBody([tensor("UINT64"), { ...tensor("BOOL"), name: "mask" }], ["[18446744073709551615]", "[true]"])
  assert.equal(body, '{"inputs":[{"name":"input","datatype":"UINT64","shape":[1],"data":[18446744073709551615]},{"name":"mask","datatype":"BOOL","shape":[1],"data":[true]}]}')
  assert.deepEqual(Object.keys(JSON.parse(body)), ["inputs"])
  assert.throws(() => inferenceBody([], []))
  assert.throws(() => inferenceBody([tensor("FP32")], []))
})
