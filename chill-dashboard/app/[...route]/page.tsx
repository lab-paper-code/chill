import { Suspense } from "react"
import { notFound } from "next/navigation"
import { ServicesPage, ServicePage, ServiceSectionPage, ResourceEditor, ModelsPage, ModelPage, ModelSectionPage } from "@/features/management"
import { InferencePage } from "@/features/inference"
import { ObservationsPage } from "@/features/observations"
import { MeasurementRunsPage, MeasurementGroupPage, MeasurementCheckPage, MeasurementDetail, ExperimentsPage } from "@/features/read-pages"
import { ResourcesPage, PoolsPage, PoolPage } from "@/features/resources"
import { SchedulerPage, SchedulerCandidatesPage, SchedulerEstimatesPage, SchedulerLoadPage } from "@/features/scheduler"
import { SchedulerSettingsPage } from "@/features/scheduler-settings"
import { SchedulerHistoryPage } from "@/features/scheduler-history"
import { SchedulerTransitionsPage } from "@/features/scheduler-transitions"
import { SchedulerPowerPage, SchedulerComparePage } from "@/features/scheduler-evidence"
import { RecordsPage, SettingsPage } from "@/features/hubs"
import { SchedulerProfilesPage } from "@/features/scheduler-profiles"
import { MeasurementPlansPage, MeasurementPlanPage } from "@/features/measurement-plans"
import { MeasurementRequestPage } from "@/features/measurement-request"

const serviceSections = ["definition", "evidence", "placement", "operation", "changes"] as const
const modelSections = ["files", "services"] as const
type ServiceSection = typeof serviceSections[number]
type ModelSection = typeof modelSections[number]

// One route, one screen. Each screen does exactly one job; dependent detail opens in a dialog.
export default async function Page({params,searchParams}:{params:Promise<{route:string[]}>;searchParams:Promise<Record<string,string|string[]|undefined>>}) {
  const {route} = await params
  const search = await searchParams
  const [head, name, sub] = route
  const create = name === "new" && !search.uid
  const key = `${route.join("/")}/${search.uid ?? ""}`
  let page:React.ReactNode
  if (head === "workloads" && route.length === 1) page = <ServicesPage />
  else if ((head === "workloads" || head === "models") && create && route.length === 2) page = <ResourceEditor key={key} kind={head} />
  else if ((head === "workloads" || head === "models") && route.length === 3 && sub === "edit") page = <ResourceEditor key={key} kind={head} name={name} />
  else if (head === "workloads" && route.length === 2) page = <ServicePage key={key} name={name} />
  else if (head === "workloads" && route.length === 3 && sub === "infer") page = <InferencePage name={name} />
  else if (head === "workloads" && route.length === 3 && serviceSections.includes(sub as ServiceSection)) page = <ServiceSectionPage key={key} name={name} section={sub as ServiceSection} />
  else if (head === "models" && route.length === 1) page = <ModelsPage />
  else if (head === "models" && route.length === 2) page = <ModelPage key={key} name={name} />
  else if (head === "models" && route.length === 3 && modelSections.includes(sub as ModelSection)) page = <ModelSectionPage key={key} name={name} section={sub as ModelSection} />
  else if (head === "resources" && route.length === 1) page = <ResourcesPage />
  else if (head === "pools" && route.length === 1) page = <PoolsPage />
  else if (head === "pools" && route.length === 2) page = <PoolPage id={name} />
  else if (head === "measurements" && route.length === 1) page = <MeasurementPlansPage />
  else if (head === "measurements" && route.length === 2 && name === "runs") page = <MeasurementRunsPage />
  else if (head === "measurements" && route.length === 2 && name === "new") page = <MeasurementRequestPage />
  else if (head === "measurements" && route.length === 2 && name === "check") page = <MeasurementCheckPage />
  else if (head === "measurements" && route.length === 2 && name === "group") page = <MeasurementGroupPage />
  else if (head === "measurements" && route.length === 4 && route[1] === "batch-energy-optimizations") page = <MeasurementPlanPage key={`${route[2]}/${route[3]}`} namespace={route[2]} name={route[3]} />
  else if (head === "measurements" && route.length === 4) page = <MeasurementDetail kind={route[1]} namespace={route[2]} name={route[3]} />
  else if (head === "observations" && route.length === 1) page = <ObservationsPage />
  else if (head === "changes" && route.length === 1) page = search.scope === "scheduler" ? <SchedulerHistoryPage settingsOnly /> : <ObservationsPage changes />
  else if (head === "scheduler" && route.length === 1) page = <SchedulerPage />
  else if (head === "scheduler" && route.length === 2 && name === "candidates") page = <SchedulerCandidatesPage />
  else if (head === "scheduler" && route.length === 2 && name === "estimates") page = <SchedulerEstimatesPage />
  else if (head === "scheduler" && route.length === 2 && name === "load") page = <SchedulerLoadPage />
  else if (head === "scheduler" && route.length === 2 && name === "profiles") page = <SchedulerProfilesPage />
  else if (head === "scheduler" && route.length === 2 && name === "history") page = <SchedulerHistoryPage />
  else if (head === "scheduler" && route.length === 2 && name === "power") page = <SchedulerPowerPage />
  else if (head === "scheduler" && route.length === 2 && name === "compare") page = <SchedulerComparePage />
  else if (head === "transitions" && route.length <= 2) page = <SchedulerTransitionsPage id={name} />
  else if (head === "configuration" && route.length === 1) page = <SchedulerSettingsPage />
  else if (head === "records" && route.length === 1) page = <RecordsPage />
  else if (head === "settings" && route.length === 1) page = <SettingsPage />
  else if (head === "experiments" && route.length <= 2) page = <ExperimentsPage />
  else notFound()
  return <Suspense fallback={<p className="screen" role="status">화면 여는 중</p>}>{page}</Suspense>
}
