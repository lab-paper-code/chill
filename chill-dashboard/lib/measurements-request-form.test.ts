import assert from "node:assert/strict"
import test from "node:test"
import { advancedIssue, nameIssue, pathSupport } from "./measurements-request-form.ts"
import type { Advanced } from "./measurement-request.ts"
import type { InventoryClass, ModelCompatibility } from "./read-types.ts"

const model = { spec: {
  artifacts: [
    { name: "onnx-arm64", format: "onnx", source: { image: "i", architecture: "arm64", files: [] } },
    { name: "onnx-int8-amd64", format: "onnx", source: { image: "i", architecture: "amd64", files: [] } },
  ],
  executionPaths: [
    { name: "ort-cuda-arm64", artifact: "onnx-arm64", runtime: { family: "firstfloor", backend: "ort-cuda" } },
    { name: "ort-cpu-int8-amd64", artifact: "onnx-int8-amd64", runtime: { family: "firstfloor", backend: "ort-cpu" } },
  ],
} }
const orin = { name: "orin", uid: "u", resourceVersion: "1", nodeSelector: {}, architecture: "arm64", memory: "", powerModes: [], nodes: [] } as InventoryClass

test("path support: static mismatch blocks, server verdict decides the rest", () => {
  assert.deepEqual(pathSupport(model, model.spec.executionPaths[1], orin, "n"), { state: "unsupported", reason: "x86 64비트 전용" })
  assert.equal(pathSupport(model, model.spec.executionPaths[0], orin, "n").state, "unknown")
  const read = (verdict: ModelCompatibility["verdict"], node = "n", code = "RuntimeDeclarationMissing") => ({ data: { selection: { modelSpec: "m", executionPath: "ort-cuda-arm64", deviceClass: "orin", node }, verdict, reasons: [{ kind: "k", code, message: "no cuda" }] } as unknown as ModelCompatibility })
  assert.equal(pathSupport(model, model.spec.executionPaths[0], orin, "n", read("Compatible")).state, "candidate")
  assert.deepEqual(pathSupport(model, model.spec.executionPaths[0], orin, "n", read("Incompatible")), { state: "unsupported", reason: "실행 환경 선언 없음" })
  assert.deepEqual(pathSupport(model, model.spec.executionPaths[0], orin, "n", read("Unknown", "n", "SomethingNew")), { state: "unknown", reason: "기타 사유" })
  assert.equal(pathSupport(model, model.spec.executionPaths[0], orin, "n", read("Compatible", "other")).state, "unknown")
})

test("advanced issues follow the request validator", () => {
  const values: Advanced = { warmup: 1, activeDuration: 60, activeWindow: 64, activeInterval: 1000, activeTimeout: 500, settling: 5, idleDuration: 10, idleWindow: 12, idleInterval: 1000, idleTimeout: 500, retryLimit: 2, utilizations: "0.2, 0.5", sparseMeanGap: 10, phaseDuration: 5, postLoadDuration: 1, requestLifetime: 2, deliveryMargin: 1, framedDecodedRequestUpperBytes: 10 }
  for (const key of Object.keys(values) as (keyof Advanced)[]) assert.equal(advancedIssue(values, key), "", key)
  assert.equal(advancedIssue({ ...values, warmup: NaN }, "warmup"), "0 이상 정수")
  assert.equal(advancedIssue({ ...values, retryLimit: 11 }, "retryLimit"), "0~10 정수")
  assert.equal(advancedIssue({ ...values, sparseMeanGap: 0 }, "sparseMeanGap"), "0보다 큰 값")
  assert.equal(advancedIssue({ ...values, activeTimeout: 1000 }, "activeTimeout"), "기록 간격보다 짧게")
  assert.equal(advancedIssue({ ...values, idleWindow: 9 }, "idleWindow"), "측정 시간 이상")
  assert.equal(advancedIssue({ ...values, utilizations: "0.2, 1" }, "utilizations"), "0과 1 사이 값")
})

test("request names are DNS-1123 and unique", () => {
  assert.equal(nameIssue("ok-name.1", new Set()), "")
  assert.equal(nameIssue(""), "입력 필요")
  assert.equal(nameIssue("", undefined, false), "")
  assert.equal(nameIssue("Bad"), "소문자, 숫자, -, . 200자 이내")
  assert.equal(nameIssue("taken", new Set(["taken"])), "이미 있는 이름")
})
