"use client"

import Link from "next/link"
import { usePathname, useSearchParams } from "next/navigation"
import type { MeasurementAssessment } from "@/lib/read-types"

const tabs = [["coverage","/measurements","커버리지"],["requests","/measurements/requests","요청"],["runs","/measurements/runs","회차"],["profiles","/scheduler/profiles","프로파일"],["operation-cost-measurements","/measurements/operation-cost-measurements","동작 비용"],["dispatch-occupancy-measurements","/measurements/dispatch-occupancy-measurements","분배 점유"]] as const
export function MeasurementNavigation({active,namespace}:{active?:string;namespace?:string}) {
  const pathname=usePathname(),params=useSearchParams()
  const ns=namespace??params.get("namespace")
  return <nav aria-label="측정" className="measurement-tabs">{tabs.map(([key,href,label])=>{const selected=active?active===key:pathname===href||(key==="coverage"&&pathname==="/measurements/coverage");return <Link key={key} href={`${href}${ns?`?${new URLSearchParams({namespace:ns})}`:""}`} aria-current={selected?"page":undefined}>{label}</Link>})}</nav>
}
export function MeasurementStatus({assessment}:{assessment?:MeasurementAssessment|null}) {
  return <span className={`measurement-status status-${assessment?.displayStatus??"unknown"}`}>{assessment?.label??"확인 불가"}</span>
}
