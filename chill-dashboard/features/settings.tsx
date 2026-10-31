"use client"

import { useSearchParams } from "next/navigation"
import { CatalogSettingsPage } from "@/features/management"
import { SchedulerSettingsPage } from "@/features/scheduler-settings"

export function SettingsPage() {
  const tab=useSearchParams().get("tab")
  return tab==="models"||tab==="preprocessors"||tab==="datasets"?<CatalogSettingsPage key={tab} kind={tab}/>:<SchedulerSettingsPage/>
}
