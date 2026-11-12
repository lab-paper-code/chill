import assert from "node:assert/strict"
import test from "node:test"
import { parseMeasurementDocument } from "./measurement-document.ts"
import { parseCatalogManifest } from "./catalog-manifest.ts"
import { readFileSync } from "node:fs"
import { measurementRequestExample } from "./measurement-guidance.ts"

const envelope = "apiVersion: edge.dacs.io/v1alpha1\nkind: BatchEnergyOptimization\nmetadata: {name: producer-request, namespace: lab}\n"

test("native measurement import preserves provided spec and user metadata while dropping server lifecycle", () => {
  const body = parseMeasurementDocument(JSON.stringify({
    apiVersion: "edge.dacs.io/v1alpha1", kind: "BatchEnergyOptimization",
    metadata: { name: "producer-request", namespace: "lab", labels: { campaign: "operator" }, annotations: { source: "producer" }, uid: "old", resourceVersion: "9", creationTimestamp: "old", deletionTimestamp: "old", managedFields: [{}], finalizers: ["old"] },
    spec: { producerExtension: { enabled: false, zero: 0, entries: ["provided", null] }, protocol: { duration: "operator-value" } },
    status: { phase: "Complete" },
  }), "lab")
  const sent = JSON.parse(JSON.stringify(body))
  assert.equal(sent.metadata.name, "producer-request")
  assert.equal(sent.metadata.namespace, "lab")
  assert.equal(sent.metadata.labels.campaign, "operator")
  assert.equal(sent.metadata.annotations.source, "producer")
  assert.ok(!Object.hasOwn(sent.metadata, "uid"))
  assert.ok(!Object.hasOwn(sent.metadata, "resourceVersion"))
  assert.ok(!Object.hasOwn(sent.metadata, "finalizers"))
  assert.ok(!Object.hasOwn(sent, "status"))
  assert.equal(sent.spec.producerExtension.enabled, false)
  assert.equal(sent.spec.producerExtension.zero, 0)
  assert.equal(sent.spec.producerExtension.entries[1], null)
  assert.equal(sent.spec.protocol.duration, "operator-value")
  assert.equal(parseMeasurementDocument(envelope.replace(", namespace: lab", "") + "spec: {}", "selected").metadata.namespace, "selected")
})

test("document envelope rejects wrong kinds, versions and changed namespaces without validating spec domain", () => {
  const source = envelope + "spec: {futureServerField: arbitrary}"
  assert.equal(parseMeasurementDocument(source, "lab").spec.futureServerField, "arbitrary")
  assert.throws(() => parseMeasurementDocument(source, "other"))
  for (const invalid of [source.replace("BatchEnergyOptimization", "Dataset"), source.replace("v1alpha1", "v2"), source.replace("name: producer-request", "name: ''"), envelope + "spec: []", "name: short-form\nspec: {}", source + "\n---\nspec: {}"])
    assert.throws(() => parseMeasurementDocument(invalid, "lab"))
})

test("provided sample and authoring reference include all required operating point inputs",()=>{
  const sample=parseMeasurementDocument(readFileSync(new URL("../../config/samples/profiling/edge_v1alpha1_batchenergyoptimization.yaml",import.meta.url),"utf8"),"chill-system")
  const active=sample.spec.activeProtocol as Record<string,unknown>
  const points=active.operatingPoints as Record<string,unknown>
  assert.ok((points.utilizations as number[]).length>0)
  assert.ok((points.utilizations as number[]).every(value=>value>0&&value<1))
  assert.ok(Number(points.sparseMeanGapMilliseconds)>0)
  for(const field of ["phaseDurationSeconds","postLoadDurationSeconds","requestLifetimeSeconds","deliveryMarginMilliseconds","framedDecodedRequestUpperBytes"]) {
    assert.ok(Number.isInteger(points[field])&&Number(points[field])>0,field)
    assert.match(measurementRequestExample,new RegExp(`${field}:`))
  }
  assert.ok(Number(points.phaseDurationSeconds)>=30)
  assert.equal(active.durationSeconds,Number(points.phaseDurationSeconds)*((points.utilizations as number[]).length+2)+Number(points.postLoadDurationSeconds))
  assert.ok(Number((active.metricObservation as Record<string,unknown>).durationSeconds)>Number(active.durationSeconds))
  for(const field of ["utilizations","sparseMeanGapMilliseconds"])assert.match(measurementRequestExample,new RegExp(`${field}:`))
})

test("measurement and catalog imports retain shared JSON transport and default alias protections", () => {
  const parsers = [
    (spec: string) => parseMeasurementDocument(envelope + `spec: ${spec}`, "lab").spec,
    (spec: string) => parseCatalogManifest("datasets", `name: catalog-entry\nspec: ${spec}`).spec,
  ]
  const bomb = "\n  a0: &a0 [1]\n" + Array.from({ length: 8 }, (_, index) => `  a${index + 1}: &a${index + 1} [${Array(10).fill(`*a${index}`).join(",")}]`).join("\n")
  for (const parse of parsers) {
    for (const invalid of ["{integer: 9007199254740993}", "{number: .inf}", "{number: .nan}", "&self {nested: *self}", "{field: one, field: two}", "{value: !!timestamp 2001-12-15T02:59:43Z}", "{value: !!set {one: null}}", bomb]) assert.throws(() => parse(invalid))
    const sent = JSON.parse(JSON.stringify(parse("{source: &source {path: /provided}, copy: *source}")))
    assert.equal(sent.source.path, "/provided")
    assert.equal(sent.copy.path, "/provided")
  }
  assert.throws(() => parseMeasurementDocument(envelope.replace("namespace: lab", "namespace: lab, labels: {limit: .inf}") + "spec: {}", "lab"))
})
