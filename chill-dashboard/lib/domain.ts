// JSON contracts from api/v1alpha1 and internal/serving/kube/workload_view.go.
export const datatypes = ["BOOL", "UINT8", "UINT16", "UINT32", "UINT64", "INT8", "INT16", "INT32", "INT64", "FP16", "FP32", "FP64", "BYTES", "BF16"] as const
export type Datatype = typeof datatypes[number]
export type TensorContract = { name: string; datatype: Datatype; sampleShape: number[] }
export type FirstfloorSettings = { maxBatchSize: number; maxQueueSize: number; deadlinePolicy?: "drop" | "keep" | "defer" }
export type ModelArtifact = {
  name: string; format: string
  source: { image: string; architecture: string; files: string[] }
  // Declared for RKNN artifacts only; preprocessor artifacts define every other format's model input.
  input?: { tensors: TensorContract[] }
  derivation?: { sourceArtifact: string; targetPlatform: string; declaredBatchSizes: number[]; conversion: { tool: string; version: string } }
}
export type ExecutionPath = { name: string; artifact: string; runtime: { family: string; backend: string; settings?: { firstfloor?: FirstfloorSettings } } }
export type ModelSpec = { artifacts: ModelArtifact[]; executionPaths: ExecutionPath[] }
export type Metadata = { name: string; uid: string; resourceVersion: string; generation: number; creationTimestamp?: string; deletionTimestamp?: string; [key: string]: unknown }
export type ModelResource = { apiVersion?: string; kind?: string; metadata: Metadata; spec: ModelSpec; status?: unknown }
export type ModelList = { items: ModelResource[]; metadata?: unknown }
export const fieldFormats = ["JPEG", "UTF8"] as const
export type RawField = { name: string; format: typeof fieldFormats[number]; maxBytes: number }
export type PreprocessorArtifact = { target: { model: string; artifact: string }; source: { image: string; architecture: string; path: string } }
export type PreprocessorSpec = { input: { fields: RawField[] }; output: { fields: Omit<RawField, "maxBytes">[] }; maxNewTokens?: number; artifacts: PreprocessorArtifact[] }
export type DatasetSpec = { source: { image: string; path: string }; fields: RawField[] }
export type PreprocessorResource = { apiVersion?: string; kind?: string; metadata: Metadata; spec: PreprocessorSpec }
export type DatasetResource = { apiVersion?: string; kind?: string; metadata: Metadata; spec: DatasetSpec }
type CatalogRef = { name: string }
type AppliedRef = { name: string; uid: string; generation: number }
export type WorkloadSpec = { modelRef: CatalogRef; preprocessorRef: CatalogRef; datasetRef?: CatalogRef; slo: { percentile: number; targetLatency: string } }
export type Operation = { kind: string; phase: string; targetGeneration: number; targetCatalogUID?: string; targetCatalogGeneration?: number; acceptedAt: string; appliedAt?: string; completedAt?: string; reason?: string; error?: string; actorUsername?: string; actorUID?: string }
// The only artifact format → firstfloor backend pairs ChillServer accepts (internal/modelcatalog/catalog.go:107-123).
export const backendsByFormat: Record<string, readonly string[]> = { onnx: ["ort-cpu", "ort-cuda"], rknn: ["rknnlite"] }
// The plain word for a contract field format; the field name itself is always shown as written.
export const formatText = (format: RawField["format"]) => format === "JPEG" ? "사진" : "글"
export type WorkloadStatus = { appliedDefinition?: { workloadGeneration: number; modelRef: AppliedRef; preprocessorRef: AppliedRef; datasetRef?: Omit<AppliedRef, "generation">; slo: WorkloadSpec["slo"] }; admission: { allowed: boolean; reason?: string; observedAt: string }; operation?: Operation }
export type WorkloadResource = { metadata: Metadata; spec: WorkloadSpec; status?: WorkloadStatus; apiVersion?: string; kind?: string }
export type AppliedDefinition = {
  name: string; uid: string; generation: number
  model: AppliedRef & { paths: { name: string; artifact: string; format: string; image: string; architecture: string; files: string[]; runtimeFamily: string; backend: string; firstfloor?: FirstfloorSettings; preprocessor: PreprocessorArtifact["source"] }[] }
  preprocessor: AppliedRef & { input: RawField[]; output: Omit<RawField, "maxBytes">[]; maxNewTokens?: number }
  dataset?: Omit<AppliedRef, "generation">
  slo: { percentile: number; targetLatencyNanos: number }
}
export type ObjectIdentity = { store: string; key: string; size: number }
export type SupportedInput = { executionPath: string; artifact: string; profiles: { id: string; reference: ObjectIdentity; evidence: ObjectIdentity[]; condition: unknown }[] }
export type WorkloadView = { desired: WorkloadResource; appliedDefinition?: AppliedDefinition; supportedInputs: SupportedInput[]; admission: boolean; readiness: string; reason: string; pending: number; assigned: number; operation?: Operation; observedAt: string }
// Native Response JSON verbatim from POST /api/v1/workloads/{name}/infer.
type InferenceOutput = { outputs: { name: string; data: string[] }[] }

export const resourceHref = (kind: string, name: string, uid: string, action = "") => `/${kind}/${encodeURIComponent(name)}${action ? `/${action}` : ""}?uid=${encodeURIComponent(uid)}`
export const sloTarget = (spec: WorkloadSpec) => `${spec.slo.percentile}%의 요청을 ${spec.slo.targetLatency} 안에 처리`
// Matches serving.Definition.Identity(): uid/generation/modelUID/modelGeneration/preprocessorUID/preprocessorGeneration.
export const definitionIdentity = (applied: AppliedDefinition) => `${applied.uid}/${applied.generation}/${applied.model.uid}/${applied.model.generation}/${applied.preprocessor.uid}/${applied.preprocessor.generation}`

// An empty dataset leaves datasetRef out; the service registers and waits with MissingDataset.
export function workloadSpec(model: string, preprocessor: string, dataset: string, percentile: string, latency: string): WorkloadSpec {
  const p = Number(percentile)
  if (!percentile.trim() || !Number.isFinite(p) || p <= 0 || p > 100) throw new Error("SLO 백분위는 0 < p ≤ 100 이어야 합니다.")
  if (!model) throw new Error("모델 참조가 필요합니다.")
  if (!preprocessor) throw new Error("전처리기 참조가 필요합니다.")
  if (!latency.trim()) throw new Error("SLO 목표 지연과 단위가 필요합니다. Go API가 양수와 범위를 검증합니다.")
  return { modelRef: { name: model }, preprocessorRef: { name: preprocessor }, ...(dataset ? { datasetRef: { name: dataset } } : {}), slo: { percentile: p, targetLatency: latency } }
}
export function positiveIntegers(text: string): number[] {
  const parts = text.split(/[\s,]+/).filter(Boolean)
  if (!parts.length || parts.some(p => !/^\d+$/.test(p) || Number(p) < 1 || Number(p) > 2147483647)) throw new Error("1부터 2147483647까지 정수 목록이 필요합니다.")
  return parts.map(Number)
}
function base64(bytes: Uint8Array): string {
  let text = ""
  for (let i = 0; i < bytes.length; i += 0x8000) text += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(text)
}
// One raw sample for the applied preprocessor input contract; the gateway checks JPEG and UTF-8 content.
export function rawInferenceBody(fields: RawField[], values: Record<string, Uint8Array | undefined>): string {
  if (!fields.length) throw new Error("적용된 전처리기의 입력 계약이 없어요.")
  return JSON.stringify({ inputs: fields.map(field => {
    const data = values[field.name]
    if (!data?.length) throw new Error(`${field.name}: 값을 넣어 주세요.`)
    if (data.length > field.maxBytes) throw new Error(`${field.name}: 최대 ${field.maxBytes}바이트까지 보낼 수 있어요.`)
    return { name: field.name, datatype: "BYTES", data: base64(data) }
  }) })
}
// Each declared output field's first element: UTF8 decoded to text, JPEG kept as base64 for a data URL.
export function rawOutputs(fields: Omit<RawField, "maxBytes">[], response: unknown): { name: string; format: RawField["format"]; value?: string }[] {
  const outputs = (response as Partial<InferenceOutput> | null)?.outputs ?? []
  return fields.map(field => {
    const data = outputs.find(output => output.name === field.name)?.data?.[0]
    if (data === undefined || field.format === "JPEG") return { name: field.name, format: field.format, value: data }
    return { name: field.name, format: field.format, value: new TextDecoder().decode(Uint8Array.from(atob(data), c => c.charCodeAt(0))) }
  })
}
