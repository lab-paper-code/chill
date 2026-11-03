"use client"

import { useRouter, useSearchParams } from "next/navigation"
import Link from "next/link"
import { historyReturnTo } from "@/lib/scheduler-history"
import { SchedulerHistoryPage } from "./scheduler-history"
import { ObservationsPage } from "./observations"
import { SchedulerPowerPage, ComparisonEvidence } from "./scheduler-evidence"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { IncidentRecord } from "./live-board"

export function RecordsPage(){
  const params=useSearchParams(),router=useRouter()
  const returnTo=historyReturnTo(params.get("returnTo"))
  let content
  if(params.get("kind")==="requests"||params.get("scope")==="management")content=<ObservationsPage changes={params.get("scope")==="management"}/>
  else if(params.get("kind")==="power")content=<SchedulerPowerPage/>
  else if(params.get("open")?.startsWith("event:"))content=<><SchedulerHistoryPage/><Sheet open onOpenChange={open=>{if(!open){const next=new URLSearchParams(params.toString());next.delete("open");router.replace(`/records?${next}`,{scroll:false})}}}><SheetContent className="w-full overflow-y-auto p-6 sm:max-w-4xl"><SheetHeader><SheetTitle>선택 사건</SheetTitle></SheetHeader><div className="live-stage"><IncidentRecord identity={params.get("open")!.slice(6)}/></div>{returnTo&&<Link className="inline-flex min-h-11 items-center underline underline-offset-4" href={returnTo}>실험 구간으로 돌아가기</Link>}</SheetContent></Sheet></>
  else if(params.get("compare")==="1")content=<><SchedulerHistoryPage/><Sheet open onOpenChange={open=>{if(!open){const next=new URLSearchParams(params.toString());next.delete("compare");router.replace(`/records?${next}`,{scroll:false})}}}><SheetContent className="w-full overflow-y-auto p-6 sm:max-w-4xl"><SheetHeader><SheetTitle>판단 비교 근거</SheetTitle></SheetHeader><ComparisonEvidence/>{returnTo&&<Link className="inline-flex min-h-11 items-center underline underline-offset-4" href={returnTo}>실험 구간으로 돌아가기</Link>}</SheetContent></Sheet></>
  else content=<SchedulerHistoryPage settingsOnly={params.get("scope")==="scheduler"}/>
  return <>{returnTo&&<nav aria-label="실험 복귀"><Link className="inline-flex min-h-11 items-center px-4 underline underline-offset-4" href={returnTo}>실험 구간으로 돌아가기</Link></nav>}{content}</>
}
