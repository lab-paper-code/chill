// The visible form of device classes, power modes, execution paths and architectures, shared by every screen.
// Device names (cluster node names such as "orin-nano") are identifiers and are shown as they are.
import type { ModelSpec } from "./domain.ts"

const words: Record<string, string> = { rtx: "RTX", agx: "AGX", nx: "NX", ti: "Ti", lattepanda: "LattePanda", orangepi: "Orange Pi" }
/** "jetson-agx-orin-64g" → "Jetson AGX Orin 64GB", "edge-server-rtx-2080-ti-64g" → "RTX 2080 Ti 64GB". */
export function deviceClassLabel(name: string): string {
  return name.replace(/^edge-server-/, "").split("-").filter(Boolean)
    .map(word => /^\d+g$/.test(word) ? `${word.slice(0, -1)}GB` : words[word] ?? word[0].toUpperCase() + word.slice(1)).join(" ")
}

/** Power mode names are the device's own ("MAXN", "15W"); only the actuator code "fixed" is translated. */
export const powerModeLabel = (mode: string): string => (mode.toLowerCase() === "fixed" ? "고정" : mode)

const architectures: Record<string, string> = { arm64: "ARM 64비트", amd64: "x86 64비트" }
export const architectureLabel = (architecture: string): string => architectures[architecture] ?? architecture.toUpperCase()

const backends: Record<string, string> = { "ort-cpu": "CPU", "ort-cuda": "CUDA", rknnlite: "NPU" }
/**
 * Execution path in the coverage matrix words: backend, "정수 8비트" when quantised, artifact architecture.
 * With the model spec the declared backend and artifact architecture decide; without it the path name does.
 * A path whose backend cannot be told keeps its name.
 */
export function executionPathLabel(name: string, model?: { spec: ModelSpec }): string {
  const path = model?.spec.executionPaths.find(item => item.name === name)
  if (path) {
    const architecture = model?.spec.artifacts.find(item => item.name === path.artifact)?.source.architecture
    return [backends[path.runtime.backend] ?? path.runtime.backend.toUpperCase(), /int8/i.test(path.artifact) ? "정수 8비트" : "", architecture ? architectureLabel(architecture) : ""].filter(Boolean).join(" ")
  }
  const backend = /cuda/.test(name) ? "CUDA" : /rknn/.test(name) ? "NPU" : /cpu/.test(name) ? "CPU" : ""
  if (!backend) return name
  const architecture = /arm64/.test(name) ? "arm64" : /amd64/.test(name) ? "amd64" : ""
  return [backend, /int8/.test(name) ? "정수 8비트" : "", architecture && architectureLabel(architecture)].filter(Boolean).join(" ")
}
