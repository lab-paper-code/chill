import { parse } from "yaml"

export const catalogManifestKinds = { models: "ModelSpec", preprocessors: "Preprocessor", datasets: "Dataset" } as const
export type CatalogKind = keyof typeof catalogManifestKinds
const apiVersion = "edge.dacs.io/v1alpha1"
export type CatalogRegistration = { name: string; spec: Record<string, unknown> }

export function buildCatalogManifest(kind: CatalogKind, name: string, spec: unknown) {
  return { apiVersion, kind: catalogManifestKinds[kind], metadata: { name }, spec }
}

function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

// YAML aliases and non-finite numbers must not change or disappear during JSON transport.
function checkJSON(value: unknown, ancestors = new Set<object>()): void {
  if (typeof value === "number" && Number.isInteger(value) && !Number.isSafeInteger(value)) throw new Error("정확히 전달할 수 없는 큰 정수가 있어요. 생산자가 해당 값의 표현을 확인해야 해요.")
  if (value === null || typeof value === "string" || typeof value === "boolean" || (typeof value === "number" && Number.isFinite(value))) return
  if (typeof value !== "object" || ancestors.has(value)) throw new Error("JSON으로 전달할 수 없는 값이나 순환 참조가 있어요.")
  ancestors.add(value)
  for (const child of Object.values(value)) checkJSON(child, ancestors)
  ancestors.delete(value)
}

export function parseCatalogManifest(kind: CatalogKind, text: string): CatalogRegistration {
  let document: unknown
  try { document = parse(text, { schema: "core" }) }
  catch (error) { throw new Error(`YAML/JSON 문서 하나를 확인해 주세요. ${(error as Error).message}`) }
  if (!object(document)) throw new Error("이름과 spec이 있는 YAML/JSON 문서 하나가 필요해요.")
  const native = ["apiVersion", "kind", "metadata"].some(key => Object.hasOwn(document, key))
  let name: unknown = document.name
  if (native) {
    if (document.apiVersion !== apiVersion) throw new Error(`apiVersion은 ${apiVersion}이어야 해요.`)
    if (document.kind !== catalogManifestKinds[kind]) throw new Error(`이 화면에서는 ${catalogManifestKinds[kind]} 문서만 등록할 수 있어요.`)
    name = object(document.metadata) ? document.metadata.name : undefined
  }
  if (typeof name !== "string" || !name.trim()) throw new Error(native ? "metadata.name에 등록 이름을 적어 주세요." : "name에 등록 이름을 적어 주세요.")
  if (!object(document.spec)) throw new Error("spec은 설정 항목을 담은 객체여야 해요.")
  checkJSON(document.spec)
  // Lifecycle and identity remain owned by the CHILL API, never by the source manifest.
  return { name, spec: document.spec }
}
