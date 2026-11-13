import type { ExecutionPath, ModelSpec } from "./domain.ts"
import type { InventoryClass, ModelCompatibility } from "./read-types.ts"
import { advancedFields, type Advanced } from "./measurement-request.ts"
import { staticCompatibility, type Compatibility } from "./measurement-coverage.ts"
import { architectureLabel, reasonLabel } from "./measurements-coverage-matrix.ts"

const backends: Record<string, string> = { "ort-cpu": "CPU", "ort-cuda": "CUDA", rknnlite: "NPU" }

/** Short execution path label in the coverage matrix words: backend, 정수 8비트 when quantised, artifact architecture. Unknown paths keep their name. */
export function pathLabel(model: { spec: ModelSpec } | undefined, name: string): string {
  const path = model?.spec.executionPaths.find(item => item.name === name)
  if (!path) return name
  const architecture = model?.spec.artifacts.find(item => item.name === path.artifact)?.source.architecture
  return [backends[path.runtime.backend] ?? path.runtime.backend.toUpperCase(), /int8/i.test(path.artifact) ? "정수 8비트" : "", architecture ? architectureLabel(architecture) : ""].filter(Boolean).join(" ")
}

export type PathSupport = { state: Compatibility["state"]; reason: string }
type Read = { data?: ModelCompatibility; error?: unknown; isPending?: boolean }

/** Static declaration first (a proven mismatch blocks), then the server verdict for the selected node. */
export function pathSupport(model: { spec: ModelSpec }, path: ExecutionPath, cls: InventoryClass, node: string, read?: Read): PathSupport {
  if (staticCompatibility(model, path, cls).state === "unsupported") {
    const architecture = model.spec.artifacts.find(item => item.name === path.artifact)?.source.architecture
    return { state: "unsupported", reason: architecture && architecture !== cls.architecture ? `${architectureLabel(architecture)} 전용` : "RK3588 NPU 전용" }
  }
  const data = read?.data
  if (!data) return { state: "unknown", reason: read?.isPending ? "확인 중" : "지원 미확인" }
  if (data.selection.executionPath !== path.name || data.selection.deviceClass !== cls.name || data.selection.node !== node) return { state: "unknown", reason: "지원 미확인" }
  const reasons = [...new Set(data.reasons.map(reasonLabel))].join(", ")
  if (data.verdict === "Compatible") return { state: "candidate", reason: "" }
  if (data.verdict === "Incompatible") return { state: "unsupported", reason: reasons || "지원 안 함" }
  return { state: "unknown", reason: reasons || "지원 미확인" }
}

/** Short inline error of one protocol value; mirrors the validator in measurement-request.ts. */
export function advancedIssue(values: Advanced, key: keyof Advanced): string {
  if (key === "utilizations") return values.utilizations.split(",").some(item => !item.trim() || !(Number(item) > 0 && Number(item) < 1)) ? "0과 1 사이 값" : ""
  const field = advancedFields.find(([name]) => name === key)
  if (!field) return ""
  const [, , min, max = 2147483647, step = 1] = field
  const value = values[key]
  const upper = key === "retryLimit" ? 10 : max
  if (!Number.isFinite(value) || (step !== "any" && !Number.isInteger(value)) || value < min || (key === "sparseMeanGap" && value === 0) || value > upper)
    return key === "sparseMeanGap" ? "0보다 큰 값" : key === "retryLimit" ? "0~10 정수" : `${min} 이상 정수`
  if ((key === "activeTimeout" && value >= values.activeInterval) || (key === "idleTimeout" && value >= values.idleInterval)) return "기록 간격보다 짧게"
  if ((key === "activeWindow" && value < values.activeDuration) || (key === "idleWindow" && value < values.idleDuration)) return "측정 시간 이상"
  return ""
}

const dns1123 = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/
/** Short inline error of a request name (DNS-1123 subdomain, at most 200 characters). */
export function nameIssue(name: string, taken?: ReadonlySet<string>, required = true): string {
  if (!name) return required ? "입력 필요" : ""
  if (name.length > 200 || !dns1123.test(name)) return "소문자, 숫자, -, . 200자 이내"
  return taken?.has(name) ? "이미 있는 이름" : ""
}
