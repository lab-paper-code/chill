import { checkJSON, documentObject, parseYAMLDocument } from "./yaml-document.ts"

export type MeasurementRequestDocument = {
  apiVersion: string
  kind: string
  metadata: { name: string; namespace: string; labels?: unknown; annotations?: unknown }
  spec: Record<string, unknown>
}

export function parseMeasurementDocument(text: string, namespace: string): MeasurementRequestDocument {
  const document = parseYAMLDocument(text)
  if (!documentObject(document)) throw new Error("BatchEnergyOptimization YAML/JSON 문서 하나 필요")
  if (document.apiVersion !== "edge.dacs.io/v1alpha1") throw new Error("apiVersion edge.dacs.io/v1alpha1 필요")
  if (document.kind !== "BatchEnergyOptimization") throw new Error("BatchEnergyOptimization 문서만 가능")
  if (!documentObject(document.metadata) || typeof document.metadata.name !== "string" || !document.metadata.name.trim()) throw new Error("metadata.name에 측정 요청 이름 필요")
  if (!namespace.trim()) throw new Error("네임스페이스 선택 필요")
  if (document.metadata.namespace !== undefined && document.metadata.namespace !== namespace) throw new Error(`metadata.namespace는 선택한 네임스페이스 ${namespace}와 같아야 함`)
  if (!documentObject(document.spec)) throw new Error("spec 객체 필요")
  // Keep only user-owned metadata; lifecycle and identity belong to the server.
  const metadata: MeasurementRequestDocument["metadata"] = { name: document.metadata.name, namespace }
  if (Object.hasOwn(document.metadata, "labels")) metadata.labels = document.metadata.labels
  if (Object.hasOwn(document.metadata, "annotations")) metadata.annotations = document.metadata.annotations
  const body = { apiVersion: document.apiVersion, kind: document.kind, metadata, spec: document.spec }
  checkJSON(body)
  return body
}
