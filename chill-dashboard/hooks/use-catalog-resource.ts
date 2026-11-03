"use client"

import { useState } from "react"
import { useSearchParams } from "next/navigation"
import { useQueryClient, type UseQueryResult, type UseMutationResult } from "@tanstack/react-query"
import { ApiError, useChillMutation, useChillQuery, type ChillMutationInput, type RelayResponse } from "@/lib/client"
import type { ModelResource, ModelSpec, PreprocessorResource, PreprocessorSpec, DatasetResource, DatasetSpec, WorkloadResource, WorkloadSpec, WorkloadView, WorkloadSummary } from "@/lib/domain"

export type Kind = "models" | "workloads" | "preprocessors" | "datasets"
export type Catalog = PreprocessorResource | DatasetResource
export type Resource = ModelResource | WorkloadView | Catalog
export type Spec = ModelSpec | WorkloadSpec | PreprocessorSpec | DatasetSpec
export const unwrap = (resource: Resource): ModelResource | WorkloadResource | Catalog => "desired" in resource ? resource.desired : resource
export type Referenced = Exclude<Kind, "workloads">
// Services that use a catalog object by name, by applied definition, or as an in-flight change target.
export const connected = (list: WorkloadSummary[] | undefined, kind: Referenced, name: string, uid?: string) => list?.filter(w => {
  const spec = w.desired.spec, applied = w.appliedDefinition, status = w.desired.status?.appliedDefinition
  const [desired, ...uids] = kind === "models" ? [spec.modelRef.name, applied?.model.uid, status?.modelRef.uid]
    : kind === "preprocessors" ? [spec.preprocessorRef.name, applied?.preprocessor.uid, status?.preprocessorRef.uid]
    : [spec.datasetRef?.name, applied?.dataset?.uid, status?.datasetRef?.uid]
  return desired === name || (!!uid && [...uids, w.operation?.targetCatalogUID, w.desired.status?.operation?.targetCatalogUID].includes(uid))
})
// One registration: ChillServer read, If-Match version, conflict, unknown outcome and deletion tracking.
export function useResource(kind: Kind, name?: string, pinnedUID?:string): ResourceState {
  const queryClient = useQueryClient()
  const params=useSearchParams(),expectedUID = pinnedUID??params.get("uid")
  const path = `v1/${kind}${name ? `/${encodeURIComponent(name)}` : ""}`
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary", { poll: !!name && kind !== "workloads" })
  const detail = useChillQuery<Resource>(path, { enabled: !!name, poll: !!name })
  const mutation = useChillMutation()
  const [baseline, setBaseline] = useState<Resource | undefined>()
  const [version, setVersion] = useState<string | undefined>()
  const [conflict, setConflict] = useState(false)
  const [deletedUID, setDeletedUID] = useState<string | null>(null)
  const [result, setResult] = useState<RelayResponse | null>(null)
  if (name && detail.data && !baseline && ![401, 403].includes(detail.error?.status ?? 0)) {
    setBaseline(detail.data); setVersion(unwrap(detail.data).metadata.resourceVersion)
  }
  const source = baseline && unwrap(baseline)
  const current = name && detail.data ? unwrap(detail.data) : undefined
  const identityChanged = !!source && !!current && source.metadata.uid !== current.metadata.uid
  const wrongUID = !!expectedUID && !!current && expectedUID !== current.metadata.uid
  const missing = detail.error?.status === 404
  const denied = !!name && [401, 403].includes(detail.error?.status ?? 0)
  const blocked = wrongUID || identityChanged || missing || !!deletedUID || denied || mutation.isPending
  const affected = workloads.error || kind === "workloads" ? undefined : connected(workloads.data, kind, name ?? "", source?.metadata.uid)
  async function submit(method: "POST" | "PUT" | "DELETE", body?: unknown) {
    if (blocked || conflict) return
    setResult(null)
    try {
      const response = await mutation.mutateAsync({ path: method === "POST" ? `v1/${kind}` : path, method, body, version: method === "POST" ? undefined : version })
      setResult(response); setConflict(false)
      if (method === "DELETE") setDeletedUID(source?.metadata.uid ?? null)
      if (method === "PUT" && response.data && typeof response.data === "object") {
        const metadata = unwrap(response.data as Resource).metadata
        if (metadata?.uid === source?.metadata.uid) { setBaseline(response.data as Resource); setVersion(metadata.resourceVersion) }
      }
      await Promise.all([queryClient.invalidateQueries({ queryKey: ["chill", "v1/models"] }), queryClient.invalidateQueries({ queryKey: ["chill", "v1/workloads?view=summary"] }), queryClient.invalidateQueries({ queryKey: ["chill", "v1/preprocessors"] }), queryClient.invalidateQueries({ queryKey: ["chill", "v1/datasets"] }), name ? detail.refetch() : Promise.resolve()])
    } catch (error) {
      if (error instanceof ApiError && error.unknownOutcome && method === "DELETE") setDeletedUID(source?.metadata.uid ?? null)
      if (error instanceof ApiError && error.status === 409 && (error.data as { code?: string } | null)?.code === "ResourceVersionConflict") { setConflict(true); await detail.refetch() }
    }
  }
  function adopt() {
    if (blocked || !detail.data || !current || current.metadata.uid !== source?.metadata.uid) return
    setBaseline(detail.data); setVersion(current.metadata.resourceVersion); setConflict(false); mutation.reset()
  }
  function reviewDelete() {
    if (!current) return
    setDeletedUID(null); setVersion(current.metadata.resourceVersion); mutation.reset(); setResult(null)
  }
  return { name, expectedUID, detail, workloads, mutation, baseline, source, current, identityChanged, wrongUID, missing, denied, blocked, conflict, deletedUID, result, affected, submit, adopt, reviewDelete, clearResult: () => setResult(null) }
}
export type ResourceState = {
  name: string | undefined
  expectedUID: string | null | undefined
  detail: Omit<UseQueryResult<Resource, ApiError>, "data"> & { data: Resource | undefined }
  workloads: Omit<UseQueryResult<WorkloadSummary[], ApiError>, "data"> & { data: WorkloadSummary[] | undefined }
  mutation: UseMutationResult<RelayResponse, ApiError, ChillMutationInput>
  baseline: Resource | undefined
  source: ModelResource | WorkloadResource | Catalog | undefined
  current: ModelResource | WorkloadResource | Catalog | undefined
  identityChanged: boolean
  wrongUID: boolean
  missing: boolean
  denied: boolean
  blocked: boolean
  conflict: boolean
  deletedUID: string | null
  result: RelayResponse | null
  affected: WorkloadSummary[] | undefined
  submit: (method: "POST" | "PUT" | "DELETE", body?: unknown) => Promise<void>
  adopt: () => void
  reviewDelete: () => void
  clearResult: () => void
}
