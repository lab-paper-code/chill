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

/** List summary columns per kind: header and one value each. */
export function catalogSummaryColumns(kind: CatalogKind): { key: string; header: string; numeric?: boolean; value: (resource: CatalogResource) => string | number | null }[] {
  if (kind === "models") return [
    { key: "formats", header: "파일 형식", value: r => [...new Set((r as ModelResource).spec.artifacts.map(a => a.format.toUpperCase()))].join(", ") || null },
    { key: "paths", header: "실행 경로", numeric: true, value: r => (r as ModelResource).spec.executionPaths.length },
  ]
  if (kind === "preprocessors") return [
    { key: "input", header: "입력", value: r => names((r as PreprocessorResource).spec.input.fields) || null },
    { key: "output", header: "출력", value: r => names((r as PreprocessorResource).spec.output.fields) || null },
  ]
  return [{ key: "fields", header: "필드", value: r => names((r as DatasetResource).spec.fields) || null }]
}
