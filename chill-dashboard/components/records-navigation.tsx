"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { recordKind, recordKindParams } from "@/lib/records"

export function RecordsNavigation(){
  const params=useSearchParams(),kind=recordKind(params)
  return <nav aria-label="기록 종류" className="flex flex-wrap gap-2 border-b pb-3">{[["judgments","판단·변경"],["requests","서비스 요청"],["power","전력"]].map(([value,label])=><Link key={value} aria-current={kind===value?"page":undefined} className={`min-h-11 rounded-md px-4 py-3 text-sm ${kind===value?"bg-primary text-primary-foreground":"hover:bg-muted"}`} href={`/records?${recordKindParams(new URLSearchParams(params.toString()),value)}`}>{label}</Link>)}</nav>
}
export function RecordsRange({target,from,to,at}:{target:string;from?:string|null;to?:string|null;at?:string}){
  return <dl aria-label="조회 범위" className="grid gap-3 rounded-lg border p-4 text-sm sm:grid-cols-4">{[["대상",target],["시작 KST",from],["종료 KST",to],["기준 시각 KST",at]].map(([label,value])=><div key={label}><dt className="text-muted-foreground">{label}</dt><dd className="break-all tabular-nums">{label==="대상"?value:value&&Number.isFinite(Date.parse(value))?new Date(value).toLocaleString("ko-KR",{timeZone:"Asia/Seoul",hour12:false}):"미기록"}</dd></div>)}</dl>
}
