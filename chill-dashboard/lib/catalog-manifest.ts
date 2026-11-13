import { checkJSON, documentObject as object, parseYAMLDocument } from "./yaml-document.ts"

export const catalogManifestKinds = { models: "ModelSpec", preprocessors: "Preprocessor", datasets: "Dataset" } as const
export type CatalogKind = keyof typeof catalogManifestKinds
const apiVersion = "edge.dacs.io/v1alpha1"
export type CatalogRegistration = { name: string; spec: Record<string, unknown> }

export function buildCatalogManifest(kind: CatalogKind, name: string, spec: unknown) {
  return { apiVersion, kind: catalogManifestKinds[kind], metadata: { name }, spec }
}

export function parseCatalogManifest(kind: CatalogKind, text: string): CatalogRegistration {
  const document = parseYAMLDocument(text)
  if (!object(document)) throw new Error("name과 spec이 있는 문서 하나 필요")
  const native = ["apiVersion", "kind", "metadata"].some(key => Object.hasOwn(document, key))
  let name: unknown = document.name
  if (native) {
    if (document.apiVersion !== apiVersion) throw new Error(`apiVersion ${apiVersion} 필요`)
    if (document.kind !== catalogManifestKinds[kind]) throw new Error(`kind ${catalogManifestKinds[kind]} 문서만 가능`)
    name = object(document.metadata) ? document.metadata.name : undefined
  }
  if (typeof name !== "string" || !name.trim()) throw new Error(native ? "metadata.name 필요" : "name 필요")
  if (!object(document.spec)) throw new Error("spec 객체 필요")
  checkJSON(document.spec)
  // Lifecycle and identity remain owned by the CHILL API, never by the source manifest.
  return { name, spec: document.spec }
}
