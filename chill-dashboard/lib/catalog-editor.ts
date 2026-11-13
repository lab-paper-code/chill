import { backendsByFormat, type DatasetSpec, type ExecutionPath, type ModelArtifact, type ModelSpec, type PreprocessorArtifact, type PreprocessorSpec, type RawField, type TensorContract } from "./domain.ts"

export type Kind = "models" | "preprocessors" | "datasets"
export type Draft =
  | { kind: "models"; spec: ModelSpec }
  | { kind: "preprocessors"; spec: PreprocessorSpec }
  | { kind: "datasets"; spec: DatasetSpec }
export type Errors = Record<string, string>

export const blankField = (): RawField => ({ name: "", format: "JPEG", maxBytes: 4194304 })
export const blankTensor = (): TensorContract => ({ name: "", datatype: "FP32", sampleShape: [1] })
export const blankArtifact = (): ModelArtifact => ({ name: "", format: "onnx", source: { image: "", architecture: "amd64", files: [""] } })
export const blankPath = (artifact = ""): ExecutionPath => ({ name: "", artifact, runtime: { family: "firstfloor", backend: "ort-cpu", settings: { firstfloor: { maxBatchSize: 1, maxQueueSize: 1, deadlinePolicy: "keep" } } } })
export const blankTarget = (): PreprocessorArtifact => ({ target: { model: "", artifact: "" }, source: { image: "", architecture: "amd64", path: "" } })

export function blankDraft(kind: Kind): Draft {
  if (kind === "models") return { kind, spec: { artifacts: [blankArtifact()], executionPaths: [blankPath()] } }
  if (kind === "preprocessors") return { kind, spec: { input: { fields: [blankField()] }, output: { fields: [{ name: "", format: "UTF8" }] }, artifacts: [blankTarget()] } }
  return { kind, spec: { source: { image: "", path: "" }, fields: [blankField()] } }
}

/** A deep copy of a stored spec as an editable draft; null when the spec is not of this kind. */
export function draftFrom(kind: Kind, spec: unknown): Draft | null {
  const copy = structuredClone(spec) as Record<string, unknown>
  if (kind === "models" && "executionPaths" in copy) return { kind, spec: copy as unknown as ModelSpec }
  if (kind === "preprocessors" && "input" in copy) return { kind, spec: copy as unknown as PreprocessorSpec }
  if (kind === "datasets" && "fields" in copy) return { kind, spec: copy as unknown as DatasetSpec }
  return null
}

const blank = (value: string | undefined) => !value?.trim()
const positive = (value: number | undefined) => typeof value === "number" && Number.isInteger(value) && value >= 1 && value <= 2147483647

function names(errors: Errors, rows: { name: string }[], prefix: string) {
  const seen = new Set<string>()
  rows.forEach((row, i) => {
    if (blank(row.name)) errors[`${prefix}.${i}.name`] = "이름 필요"
    else if (seen.has(row.name)) errors[`${prefix}.${i}.name`] = "중복된 이름"
    seen.add(row.name)
  })
}
function rawFields(errors: Errors, fields: { name: string; maxBytes?: number }[], prefix: string, sized: boolean) {
  if (!fields.length) errors[prefix] = "하나 이상 필요"
  names(errors, fields, prefix)
  if (sized) fields.forEach((f, i) => { if (!positive(f.maxBytes)) errors[`${prefix}.${i}.maxBytes`] = "1 이상 정수" })
}

/** Inline validation errors keyed by field path ("artifacts.0.name"); empty when the draft can be sent. */
export function validate(draft: Draft, name: string, isNew: boolean): Errors {
  const errors: Errors = {}
  if (isNew && blank(name)) errors.name = "이름 필요"
  if (draft.kind === "models") {
    const { artifacts, executionPaths } = draft.spec
    if (!artifacts.length) errors.artifacts = "하나 이상 필요"
    names(errors, artifacts, "artifacts")
    artifacts.forEach((a, i) => {
      const p = `artifacts.${i}`
      if (blank(a.format)) errors[`${p}.format`] = "형식 필요"
      if (blank(a.source.image)) errors[`${p}.image`] = "주소 필요"
      if (blank(a.source.architecture)) errors[`${p}.architecture`] = "아키텍처 필요"
      if (!a.source.files.length) errors[`${p}.files`] = "하나 이상 필요"
      a.source.files.forEach((f, j) => { if (blank(f)) errors[`${p}.files.${j}`] = "경로 필요" })
      if (a.format === "rknn") {
        const tensors = a.input?.tensors ?? []
        if (!tensors.length) errors[`${p}.tensors`] = "입력 텐서 필요"
        names(errors, tensors, `${p}.tensors`)
        tensors.forEach((t, j) => { if (!t.sampleShape.length || !t.sampleShape.every(positive)) errors[`${p}.tensors.${j}.shape`] = "1 이상 정수 목록" })
      }
      const d = a.derivation
      if (d) {
        if (blank(d.sourceArtifact)) errors[`${p}.derivation.source`] = "원본 파일 필요"
        if (blank(d.targetPlatform)) errors[`${p}.derivation.platform`] = "대상 플랫폼 필요"
        if (!d.declaredBatchSizes.length || !d.declaredBatchSizes.every(positive)) errors[`${p}.derivation.batches`] = "1 이상 정수 목록"
        if (blank(d.conversion.tool)) errors[`${p}.derivation.tool`] = "도구 필요"
        if (blank(d.conversion.version)) errors[`${p}.derivation.version`] = "버전 필요"
      }
    })
    if (!executionPaths.length) errors.executionPaths = "하나 이상 필요"
    names(errors, executionPaths, "executionPaths")
    executionPaths.forEach((x, i) => {
      const p = `executionPaths.${i}`
      const artifact = artifacts.find(a => a.name === x.artifact)
      if (!artifact) errors[`${p}.artifact`] = "모델 파일 선택"
      else if (x.runtime.family === "firstfloor" && !(backendsByFormat[artifact.format] ?? []).includes(x.runtime.backend)) errors[`${p}.backend`] = "이 형식에서 쓸 수 없음"
      const f = x.runtime.settings?.firstfloor
      if (x.runtime.family === "firstfloor") {
        if (!positive(f?.maxBatchSize)) errors[`${p}.batch`] = "1 이상 정수"
        if (!positive(f?.maxQueueSize)) errors[`${p}.queue`] = "1 이상 정수"
      }
    })
  } else if (draft.kind === "preprocessors") {
    const { input, output, artifacts, maxNewTokens } = draft.spec
    rawFields(errors, input.fields, "input", true)
    rawFields(errors, output.fields, "output", false)
    if (maxNewTokens !== undefined && !positive(maxNewTokens)) errors.maxNewTokens = "1 이상 정수"
    if (!artifacts.length) errors.artifacts = "하나 이상 필요"
    artifacts.forEach((a, i) => {
      const p = `artifacts.${i}`
      if (blank(a.target.model)) errors[`${p}.model`] = "모델 선택"
      if (blank(a.target.artifact)) errors[`${p}.artifact`] = "모델 파일 선택"
      if (blank(a.source.image)) errors[`${p}.image`] = "주소 필요"
      if (blank(a.source.architecture)) errors[`${p}.architecture`] = "아키텍처 필요"
      if (blank(a.source.path)) errors[`${p}.path`] = "경로 필요"
    })
  } else {
    if (blank(draft.spec.source.image)) errors.image = "주소 필요"
    if (blank(draft.spec.source.path)) errors.path = "경로 필요"
    rawFields(errors, draft.spec.fields, "fields", true)
  }
  return errors
}

const keyLabels: Record<string, string> = {
  artifacts: "파일", executionPaths: "실행 경로", name: "이름", format: "형식", source: "보관", image: "이미지", architecture: "아키텍처", files: "경로", path: "경로",
  input: "입력", output: "출력", fields: "필드", tensors: "텐서", datatype: "자료형", sampleShape: "형태", maxBytes: "최대 크기", maxNewTokens: "최대 생성 토큰",
  derivation: "변환", sourceArtifact: "원본 파일", targetPlatform: "대상 플랫폼", declaredBatchSizes: "배치 크기", conversion: "변환 도구", tool: "도구", version: "버전",
  artifact: "파일", runtime: "", family: "계열", backend: "백엔드", settings: "", firstfloor: "", maxBatchSize: "최대 배치", maxQueueSize: "최대 대기열", deadlinePolicy: "기한 정책",
  target: "대상", model: "모델",
}

/** Leaf values of a spec as label/text rows, for before/after comparison. */
export function flatten(value: unknown, prefix: string[] = []): [string, string][] {
  const label = prefix.filter(Boolean).join(" ")
  if (Array.isArray(value)) {
    if (value.every(v => v === null || typeof v !== "object")) return [[label, value.join(", ")]]
    return value.flatMap((v, i) => flatten(v, [...prefix, String(i + 1)]))
  }
  if (value !== null && typeof value === "object") return Object.entries(value).flatMap(([k, v]) => flatten(v, [...prefix, keyLabels[k] ?? k]))
  return [[label, value === undefined ? "" : String(value)]]
}

/** Differences between two specs, in the order they appear (removed rows show "—" after). */
export function diff(before: unknown, after: unknown): { label: string; before: string; after: string }[] {
  const a = new Map(flatten(before)), b = new Map(flatten(after))
  const out: { label: string; before: string; after: string }[] = []
  for (const label of new Set([...a.keys(), ...b.keys()])) {
    const x = a.get(label), y = b.get(label)
    if (x !== y) out.push({ label, before: x ?? "—", after: y ?? "—" })
  }
  return out
}

const list = (value: unknown): Record<string, unknown>[] => Array.isArray(value) ? value.filter((v): v is Record<string, unknown> => v !== null && typeof v === "object") : []
const joined = (rows: Record<string, unknown>[], key: string) => rows.map(r => r[key]).filter(v => typeof v === "string").join(", ")

/** Short label/value summary of an imported spec; tolerant of any shape. */
export function summarize(kind: Kind, spec: Record<string, unknown>): [string, string][] {
  if (kind === "models") {
    const artifacts = list(spec.artifacts), paths = list(spec.executionPaths)
    return [["모델 파일", `${artifacts.length}개`], ["형식", [...new Set(artifacts.map(a => a.format))].join(", ")], ["실행 경로", `${paths.length}개`]]
  }
  if (kind === "preprocessors") {
    const input = list((spec.input as Record<string, unknown> | undefined)?.fields), output = list((spec.output as Record<string, unknown> | undefined)?.fields)
    return [["입력", joined(input, "name")], ["출력", joined(output, "name")], ["대상 모델", joined(list(spec.artifacts).map(a => (a.target ?? {}) as Record<string, unknown>), "model")]]
  }
  const source = (spec.source ?? {}) as Record<string, unknown>
  return [["필드", joined(list(spec.fields), "name")], ["경로", typeof source.path === "string" ? source.path : ""]]
}
