import type { ObservationDefinition, SchedulerSnapshot } from "./read-types.ts"

export type IdentityWorkload = {desired:{metadata:{uid:string;name:string}}}

// UID summaries prefer the current registration. Request models must instead use their exact definitionID.
export function resolveWorkloadIdentities(
  workloads:readonly IdentityWorkload[] = [],
  definitions:readonly ObservationDefinition[] = [],
  snapshot:SchedulerSnapshot["workloads"] = [],
  uids:readonly string[] = [],
):Map<string,string> {
  const identities = new Map<string,string>()
  for (const uid of uids) identities.set(uid,`이름 미확인 (${uid.slice(0,8)})`)
  for (const row of snapshot ?? []) if (row) identities.set(row.uid,row.name)
  for (const {definition} of definitions) identities.set(definition.uid,definition.name)
  for (const {desired} of workloads) identities.set(desired.metadata.uid,desired.metadata.name)
  return identities
}
