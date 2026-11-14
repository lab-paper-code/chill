import type { ExecutionPath, ModelSpec } from "./domain.ts"
import type { InventoryClass, ModelCompatibility } from "./read-types.ts"
import { staticCompatibility, type Compatibility } from "./measurement-coverage.ts"
import { architectureLabel } from "./display-labels.ts"
import { coverageReason } from "./status-labels.ts"

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
  const reasons = [...new Set(data.reasons.map(coverageReason))].join(", ")
  if (data.verdict === "Compatible") return { state: "candidate", reason: "" }
  if (data.verdict === "Incompatible") return { state: "unsupported", reason: reasons || "지원 안 함" }
  return { state: "unknown", reason: reasons || "지원 미확인" }
}

export { advancedIssue } from "./measurement-request.ts"

const dns1123 = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/
/** Short inline error of a request name (DNS-1123 subdomain, at most 200 characters). */
export function nameIssue(name: string, taken?: ReadonlySet<string>, required = true): string {
  if (!name) return required ? "입력 필요" : ""
  if (name.length > 200 || !dns1123.test(name)) return "소문자, 숫자, -, . 200자 이내"
  return taken?.has(name) ? "이미 있는 이름" : ""
}
