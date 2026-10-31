"use client"

import { useRouter, useSearchParams } from "next/navigation"
import { SchedulerHistoryPage } from "./scheduler-history"
import { ObservationsPage } from "./observations"
import { SchedulerPowerPage, ComparisonEvidence } from "./scheduler-evidence"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"

export function RecordsPage(){
  const params=useSearchParams(),router=useRouter()
  if(params.get("kind")==="requests"||params.get("scope")==="management")return <ObservationsPage changes={params.get("scope")==="management"}/>
  if(params.get("kind")==="power")return <SchedulerPowerPage/>
  if(params.get("compare")==="1")return <><SchedulerHistoryPage/><Sheet open onOpenChange={open=>{if(!open){const next=new URLSearchParams(params.toString());next.delete("compare");router.replace(`/records?${next}`,{scroll:false})}}}><SheetContent className="w-full overflow-y-auto p-6 sm:max-w-4xl"><SheetHeader><SheetTitle>판단 비교 근거</SheetTitle></SheetHeader><ComparisonEvidence/></SheetContent></Sheet></>
  return <SchedulerHistoryPage settingsOnly={params.get("scope")==="scheduler"}/>
}
