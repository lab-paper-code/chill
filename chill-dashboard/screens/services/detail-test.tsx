"use client"

import { NotReady } from "@/components/kit"
import type { WorkloadView } from "@/lib/domain"

export type ServiceTabProps = { name: string; uid: string | undefined; workload: WorkloadView | undefined }

// Placeholder: the tab owner replaces the body (design section 7.2).
export function ServiceTestTab(_props: ServiceTabProps) {
  return <NotReady />
}
