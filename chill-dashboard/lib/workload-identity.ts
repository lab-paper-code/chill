import type { ObservationDefinition, SchedulerSnapshot } from "./read-types.ts"

export type WorkloadIdentity = {name:string;model:string;source:"current"|"definition"|"snapshot"|"unknown"}
export type IdentityWorkload = {desired:{metadata:{uid:string;name:string};spec?:{modelRef:{name:string}}}}

// UID summaries prefer the current registration. Request models must instead use their exact definitionID.
export function resolveWorkloadIdentities(
  workloads:readonly IdentityWorkload[] = [],
  definitions:readonly ObservationDefinition[] = [],
  snapshot:SchedulerSnapshot["workloads"] = [],
  uids:readonly string[] = [],
):Map<string,WorkloadIdentity> {
  const identities = new Map<string,WorkloadIdentity>()
  for (const uid of uids) identities.set(uid,{name:`이름 미확인 (${uid.slice(0,8)})`,model:"모델 미확인",source:"unknown"})
  for (const row of snapshot ?? []) if (row) identities.set(row.uid,{name:row.name,model:row.model.name,source:"snapshot"})
  for (const {definition} of definitions) identities.set(definition.uid,{name:definition.name,model:definition.model.name,source:"definition"})
  for (const {desired} of workloads) identities.set(desired.metadata.uid,{name:desired.metadata.name,model:desired.spec?.modelRef.name??"모델 미확인",source:"current"})
  return identities
}
