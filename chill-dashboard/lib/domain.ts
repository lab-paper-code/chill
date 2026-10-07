// JSON contracts from api/v1alpha1 and internal/serving/kube/workload_view.go.
export const datatypes = ["BOOL", "UINT8", "UINT16", "UINT32", "UINT64", "INT8", "INT16", "INT32", "INT64", "FP16", "FP32", "FP64", "BYTES", "BF16"] as const
export type Datatype = typeof datatypes[number]
export type TensorContract = { name: string; datatype: Datatype; sampleShape: number[] }
export type TritonSettings = { maxBatchSize: number; priorityLevels: number; maxQueueSize: number }
export type ModelArtifact = {
  name: string; format: string
  source: { image: string; architecture: string; path: string }
  input: { tensors: TensorContract[] } | null
  derivation?: { sourceArtifact: string; targetPlatform: string; declaredBatchSizes: number[]; conversion: { tool: string; version: string } }
}
export type ExecutionPath = { name: string; artifact: string; runtime: { family: string; backend: string; settings?: { triton?: TritonSettings } } }
export type ModelSpec = { artifacts: ModelArtifact[]; executionPaths: ExecutionPath[] }
export type Metadata = { name: string; uid: string; resourceVersion: string; generation: number; creationTimestamp?: string; deletionTimestamp?: string; [key: string]: unknown }
export type ModelResource = { apiVersion?: string; kind?: string; metadata: Metadata; spec: ModelSpec; status?: unknown }
export type ModelList = { items: ModelResource[]; metadata?: unknown }
export type WorkloadSpec = { modelRef: { name: string }; slo: { percentile: number; targetLatency: string } }
export type Operation = { kind: string; phase: string; targetGeneration: number; targetModelUID?: string; targetModelGeneration?: number; acceptedAt: string; appliedAt?: string; completedAt?: string; reason?: string; error?: string; actorUsername?: string; actorUID?: string }
export type WorkloadStatus = { appliedDefinition?: { workloadGeneration: number; modelRef: { name: string; uid: string; generation: number }; slo: WorkloadSpec["slo"] }; admission: { allowed: boolean; reason?: string; observedAt: string }; operation?: Operation }
export type WorkloadResource = { metadata: Metadata; spec: WorkloadSpec; status?: WorkloadStatus; apiVersion?: string; kind?: string }
export type AppliedDefinition = { name: string; uid: string; generation: number; model: { name: string; uid: string; generation: number; paths: { name: string; artifact: string; format: string; image: string; architecture: string; path: string; runtimeFamily: string; backend: string; tensors: TensorContract[]; triton?: TritonSettings }[] }; slo: { percentile: number; targetLatencyNanos: number } }
export type ObjectIdentity = { store: string; key: string; size: number }
export type SupportedInput = { executionPath: string; artifact: string; tensors: TensorContract[]; profiles: { id: string; reference: ObjectIdentity; evidence: ObjectIdentity[]; condition: unknown }[] }
export type WorkloadView = { desired: WorkloadResource; appliedDefinition?: AppliedDefinition; supportedInputs: SupportedInput[]; admission: boolean; readiness: string; reason: string; pending: number; assigned: number; operation?: Operation; observedAt: string }
export type TensorInput = { name: string; datatype: Datatype; shape: number[]; data: unknown[] }

export function workloadSpec(model: string, percentile: string, latency: string): WorkloadSpec {
  const p = Number(percentile)
  if (!percentile.trim() || !Number.isFinite(p) || p <= 0 || p > 100) throw new Error("SLO 백분위는 0 < p ≤ 100 이어야 합니다.")
  if (!model) throw new Error("모델 참조가 필요합니다.")
  if (!latency.trim()) throw new Error("SLO 목표 지연과 단위가 필요합니다. Go API가 양수와 범위를 검증합니다.")
  return { modelRef: { name: model }, slo: { percentile: p, targetLatency: latency } }
}
export function positiveIntegers(text: string): number[] {
  const parts = text.split(/[\s,]+/).filter(Boolean)
  if (!parts.length || parts.some(p => !/^\d+$/.test(p) || Number(p) < 1 || Number(p) > 2147483647)) throw new Error("1부터 2147483647까지 정수 목록이 필요합니다.")
  return parts.map(Number)
}
export function tensorCount(tensor: TensorContract): number {
  const count = tensor.sampleShape.reduce((total, size) => total * size, 1)
  if (!Number.isSafeInteger(count) || count < 1) throw new Error(`${tensor.name}: 입력 크기가 브라우저의 안전한 정수 범위를 초과합니다.`)
  return count
}
// Return the original flat JSON to preserve 64-bit integers exactly on the wire.
export function tensorData(tensor: TensorContract, text: string): string {
  const parsed: unknown = JSON.parse(text)
  if (!Array.isArray(parsed) || parsed.length !== tensorCount(tensor)) throw new Error(`${tensor.name}: 평면 배열의 값 수가 sampleShape와 일치해야 합니다.`)
  if (tensor.datatype === "BF16") throw new Error("BF16은 CHILL HTTP JSON에서 지원하지 않습니다. 바이너리 codec이 필요합니다.")
  if (tensor.datatype === "BOOL" || tensor.datatype === "BYTES") {
    const type = tensor.datatype === "BOOL" ? "boolean" : "string"
    if (parsed.some(value => typeof value !== type)) throw new Error(`${tensor.name}: ${type} 값만 허용합니다.`)
  } else {
    const values = text.trim().slice(1, -1).split(",").map(value => value.trim())
    const integer = /^(U?INT)(8|16|32|64)$/.exec(tensor.datatype)
    if (parsed.some(value => typeof value !== "number")) throw new Error(`${tensor.name}: 숫자 JSON 값만 허용합니다.`)
    for (const value of values) {
      if (integer) {
        if (!/^-?(0|[1-9]\d*)$/.test(value)) throw new Error(`${tensor.name}: 정수 JSON 값이 필요합니다.`)
        const bits = BigInt(integer[2]), unsigned = integer[1] === "UINT", n = BigInt(value)
        const min = unsigned ? BigInt(0) : -(BigInt(1) << (bits - BigInt(1)))
        const max = (BigInt(1) << (unsigned ? bits : bits - BigInt(1))) - BigInt(1)
        if (n < min || n > max || (unsigned && value.startsWith("-"))) throw new Error(`${tensor.name}: ${tensor.datatype} 범위를 초과합니다.`)
      } else {
        const n = Number(value)
        if (!Number.isFinite(n) || (tensor.datatype === "FP16" && Math.abs(n) > 65504) || (tensor.datatype === "FP32" && !Number.isFinite(Math.fround(n)))) throw new Error(`${tensor.name}: 유한한 ${tensor.datatype} 숫자가 필요합니다.`)
      }
    }
  }
  return text.trim()
}
export function inferenceBody(tensors: TensorContract[], data: string[]): string {
  if (tensors.length !== data.length || !tensors.length) throw new Error("지원 입력 계약을 선택하세요.")
  return `{"inputs":[${tensors.map((tensor, index) => `{"name":${JSON.stringify(tensor.name)},"datatype":${JSON.stringify(tensor.datatype)},"shape":${JSON.stringify(tensor.sampleShape)},"data":${tensorData(tensor, data[index])}}`).join(",")}]}`
}
