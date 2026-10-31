"use client"

import Link from "next/link"
import { usePathname, useSearchParams } from "next/navigation"
import type { MeasurementAssessment, ResultRow } from "@/lib/read-types"
import { measurementUsage, measurementEvidenceChecks } from "@/lib/measurement-assessment"

const tabs = [["coverage","/measurements","커버리지"],["requests","/measurements/requests","요청"],["runs","/measurements/runs","회차"],["profiles","/scheduler/profiles","프로파일"],["operation-cost-measurements","/measurements/operation-cost-measurements","동작 비용"],["dispatch-occupancy-measurements","/measurements/dispatch-occupancy-measurements","분배 점유"]] as const
export function MeasurementNavigation({active,namespace}:{active?:string;namespace?:string}) {
  const pathname=usePathname(),params=useSearchParams()
  const ns=namespace??params.get("namespace")
  return <nav aria-label="측정" className="measurement-tabs">{tabs.map(([key,href,label])=>{const selected=active?active===key:pathname===href||(key==="coverage"&&pathname==="/measurements/coverage");return <Link key={key} href={`${href}${ns?`?${new URLSearchParams({namespace:ns})}`:""}`} aria-current={selected?"page":undefined}>{label}</Link>})}</nav>
}
export const measurementStates = {usable:"측정 충족",review:"검토 필요",insufficient:"부족",problem:"문제",running:"진행",unmeasured:"미측정",unknown:"확인 불가",unsupported:"지원 안 함"} as const
export function MeasurementStatus({assessment}:{assessment?:MeasurementAssessment|null}) {
  const status=assessment?.displayStatus??"unknown"
  return <span className="inline-grid justify-items-start gap-1"><span className="measurement-status bg-muted text-foreground">{measurementUsage(assessment)}</span><span className={`text-xs ${status==="usable"?"text-muted-foreground":`status-${status}`}`}>측정: {measurementStates[status]}</span></span>
}
export function MeasurementEvidenceChecks({assessment,rows=[]}:{assessment?:MeasurementAssessment|null;rows?:ResultRow[]}) {
  const checks=measurementEvidenceChecks(assessment,rows)
  return <details className="text-foreground"><summary className="min-h-11 cursor-pointer py-2 text-xs"><span role="table"><span role="row" className="grid grid-cols-[1fr_auto] gap-2"><span role="columnheader">근거 확인 항목 (개)</span><span role="cell">{checks.length}</span></span></span></summary><table className="w-full text-left text-xs"><thead><tr><th className="py-1">확인 항목</th><th className="py-1 text-right">값</th><th className="py-1 pl-2">단위</th></tr></thead><tbody>{checks.map(check=><tr key={check.label}><th className="py-1 font-normal">{check.label}</th><td className="py-1 text-right tabular-nums">{typeof check.value==="number"?check.value.toLocaleString("ko-KR",{maximumFractionDigits:2}):check.value}</td><td className="pl-2">{check.unit}</td></tr>)}</tbody></table></details>
}
