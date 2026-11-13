import type { DatasetResource, ModelResource, PreprocessorResource, WorkloadSummary } from "./domain.ts"
import type { CatalogKind } from "./routes.ts"

export type CatalogResource = ModelResource | PreprocessorResource | DatasetResource

// Services that reference a catalog object by name, by applied definition, or as an in-flight change target.
export function servicesUsing(list: readonly WorkloadSummary[] | undefined, kind: CatalogKind, name: string, uid?: string) {
  return (list ?? []).filter(w => {
    const spec = w.desired.spec, applied = w.appliedDefinition, status = w.desired.status?.appliedDefinition
    const [desired, ...uids] = kind === "models" ? [spec.modelRef.name, applied?.model.uid, status?.modelRef.uid]
      : kind === "preprocessors" ? [spec.preprocessorRef.name, applied?.preprocessor.uid, status?.preprocessorRef.uid]
      : [spec.datasetRef?.name, applied?.dataset?.uid, status?.datasetRef?.uid]
    return desired === name || (!!uid && [...uids, w.operation?.targetCatalogUID, w.desired.status?.operation?.targetCatalogUID].includes(uid))
  })
}

const names = (items: { name: string }[]) => items.map(item => item.name).join(", ")

// One-line list summary per kind.
export function catalogSummary(kind: CatalogKind, resource: CatalogResource): string {
  if (kind === "models") {
    const spec = (resource as ModelResource).spec
    const formats = [...new Set(spec.artifacts.map(a => a.format.toUpperCase()))].join(", ")
    return `${formats || "—"} 실행 경로 ${spec.executionPaths.length}개`
  }
  if (kind === "preprocessors") {
    const spec = (resource as PreprocessorResource).spec
    return `입력 ${names(spec.input.fields) || "—"} 출력 ${names(spec.output.fields) || "—"}`
  }
  return names((resource as DatasetResource).spec.fields) || "—"
}
