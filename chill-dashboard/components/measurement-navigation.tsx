"use client"

import Link from "next/link"
import { usePathname, useSearchParams } from "next/navigation"
import type { MeasurementAssessment, ResultRow } from "@/lib/read-types"
import { measurementSchedulerUsed, measurementQuality, measurementEvidenceChecks } from "@/lib/measurement-assessment"

const tabs = [["coverage","/measurements","범위"],["requests","/measurements/requests","요청"],["results","/measurements/results","결과"]] as const
export function MeasurementNavigation({active,namespace}:{active?:"coverage"|"requests"|"results";namespace?:string}) {
  const pathname=usePathname(),params=useSearchParams()
  const ns=namespace??params.get("namespace")
  return <nav aria-label="측정" className="measurement-tabs">{tabs.map(([key,href,label])=>{const selected=active?active===key:pathname===href;return <Link key={key} href={`${href}${ns?`?${new URLSearchParams({namespace:ns})}`:""}`} aria-current={selected?"page":undefined}>{label}</Link>})}</nav>
}
export const measurementStates = {usable:"측정 있음",review:"검토 필요",insufficient:"일부 완료",problem:"실패",running:"진행",unmeasured:"미측정",unknown:"미확인",unsupported:"지원 안 함"} as const
export function MeasurementStatus({assessment}:{assessment?:MeasurementAssessment|null}) {
  const status=assessment?.displayStatus??"unknown",used=measurementSchedulerUsed(assessment),quality=measurementQuality([assessment])
  return <table className="text-left text-xs"><thead><tr><th className="pr-3">측정 상태</th><th className="pr-3">현재 사용</th><th>품질 판정</th></tr></thead><tbody><tr><td className="pr-3">{measurementStates[status]}</td><td className="pr-3">{used===true?"사용 중":used===false?"미사용":"미확인"}</td><td>{quality.label}</td></tr></tbody></table>
}
export function MeasurementEvidenceChecks({assessment,rows=[]}:{assessment?:MeasurementAssessment|null;rows?:ResultRow[]}) {
  const checks=measurementEvidenceChecks(assessment,rows)
  return <details className="text-foreground"><summary className="min-h-11 cursor-pointer py-2 text-xs"><span role="table"><span role="row" className="grid grid-cols-[1fr_auto] gap-2"><span role="columnheader">근거 확인 항목 (개)</span><span role="cell">{checks.length}</span></span></span></summary><table className="w-full text-left text-xs"><thead><tr><th className="py-1">확인 항목</th><th className="py-1 text-right">값</th><th className="py-1 pl-2">단위</th></tr></thead><tbody>{checks.map(check=><tr key={check.label}><th className="py-1 font-normal">{check.label}</th><td className="py-1 text-right tabular-nums">{typeof check.value==="number"?check.value.toLocaleString("ko-KR",{maximumFractionDigits:2}):check.value}</td><td className="pl-2">{check.unit}</td></tr>)}</tbody></table></details>
}
