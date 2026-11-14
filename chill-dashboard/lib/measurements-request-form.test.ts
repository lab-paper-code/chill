import assert from "node:assert/strict"
import test from "node:test"
import { advancedIssue, nameIssue, pathSupport } from "./measurements-request-form.ts"
import { defaultAdvanced, emptyDraft, manifest, type Advanced } from "./measurement-request.ts"
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
  const values: Advanced = { ...defaultAdvanced, warmup: 1, requestLifetime: 2, deliveryMargin: 1, framedDecodedRequestUpperBytes: 10 }
  for (const key of Object.keys(values) as (keyof Advanced)[]) assert.equal(advancedIssue(values, key), "", key)
  assert.equal(advancedIssue({ ...values, warmup: NaN }, "warmup"), "0 이상 정수")
  assert.equal(advancedIssue({ ...values, confidence: 1 }, "confidence"), "0.5 이상 1 미만")
  assert.equal(advancedIssue({ ...values, midLevelRatio: 1 }, "midLevelRatio"), "0과 1 사이 값")
  assert.equal(advancedIssue({ ...values, levelLimit: 18 }, "levelLimit"), "지연과 최소 관측의 합보다 길게")
  assert.equal(advancedIssue({ ...values, deliveryMargin: 2000 }, "deliveryMargin"), "요청 수명보다 짧게")
  assert.equal(advancedIssue({ ...values, observationTimeout: 100 }, "observationTimeout"), "기록 간격보다 짧게")
  assert.equal(advancedIssue({ ...values, meterLag: 0 }, "meterLag"), "")
})

test("a new request carries a valid session block without typing any value", () => {
  const context = { models: [], workloads: [], classes: [], plans: [], preprocessors: [], datasets: [], profiles: [] }
  const draft = { ...emptyDraft, workload: "mobilenet", model: "mobilenet-v2-050", preprocessor: "mobilenet-imagenet", dataset: "vqav2-val-subset", deviceClass: "jetson", node: "jetsono", path: "firstfloor-ort-cpu-arm64", powerMode: "fixed", name: "session-request" }
  for (const key of Object.keys(defaultAdvanced) as (keyof Advanced)[]) assert.equal(advancedIssue(defaultAdvanced, key), "", key)
  const { spec } = manifest(draft, context, "chill-system")
  const session = spec.session!
  assert.deepEqual({ ...session, observation: undefined }, {
    meterLagSeconds: 3, minimumPowerWindowSeconds: 15, midLevelRatio: 0.25, sparseMeanGapSeconds: 1, levelDurationLimitSeconds: 300, postLoadWindowSeconds: 60,
    confidenceLevel: "0.95", requestLifetimeSeconds: 30, deliveryMarginMilliseconds: 100, framedDecodedRequestUpperBytes: 2097152, observation: undefined,
  })
  // The schema's rules on the session block.
  assert.match(session.confidenceLevel, /^0\.[5-9][0-9]*$/)
  assert.ok(session.levelDurationLimitSeconds > session.meterLagSeconds + session.minimumPowerWindowSeconds)
  assert.ok(session.requestLifetimeSeconds * 1000 > session.deliveryMarginMilliseconds)
  assert.deepEqual(session.observation, { intervalMilliseconds: 100, requestTimeoutMilliseconds: 80, maximumOperationSeconds: 600, maximumGapMilliseconds: 500,
    timing: { changeIntervalP95Seconds: "1", maximumRequestSeconds: "0.08", maximumReceiptGapSeconds: "1", source: "sample values, not measured: replace with the Node meter's observed timing" } })
  assert.deepEqual(Object.keys(spec).sort(), ["activeProtocol", "session", "state"])
  assert.deepEqual(Object.keys(spec.activeProtocol).sort(), ["dataset", "warmupIterations", "workload"])
})

test("request names are DNS-1123 and unique", () => {
  assert.equal(nameIssue("ok-name.1", new Set()), "")
  assert.equal(nameIssue(""), "입력 필요")
  assert.equal(nameIssue("", undefined, false), "")
  assert.equal(nameIssue("Bad"), "소문자, 숫자, -, . 200자 이내")
  assert.equal(nameIssue("taken", new Set(["taken"])), "이미 있는 이름")
})
