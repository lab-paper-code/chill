import { Suspense } from "react"
import { notFound, redirect } from "next/navigation"
import { ServicesPage, ServicePage, ResourceEditor, ModelPage, ModelSectionPage, CatalogPage } from "@/features/management"
import { InferencePage } from "@/features/inference"
import { MeasurementCheckPage, MeasurementDetail } from "@/features/read-pages"
import { MeasurementRunsPage } from "@/features/measurement-runs"
import { ExperimentsPage, PublishedExperimentsPage } from "@/features/experiments"
import { EvaluationsPage, EvaluationRunPage } from "@/features/evaluation-request"
import { EvaluationAuthorPage } from "@/features/evaluation-author"
import { LoadScenarioPage, LoadScenarioAuthorPage } from "@/features/load-scenario-author"
import { DevicesPage } from "@/features/resources"
import { MeasurementCoveragePage } from "@/features/measurement-coverage"
import { MeasurementRequestsPage } from "@/features/measurement-requests"
import { MeasurementRequestPage } from "@/features/measurement-request"
import { DispatchOccupancyRequestPage } from "@/features/dispatch-occupancy-request"
import { MeasurementResultsPage, UnsupportedTransitionMeasurement } from "@/features/measurement-projections"
import { dashboardDestination } from "@/lib/dashboard-routes"
import { RecordsPage } from "@/features/records"
import { SettingsPage } from "@/features/settings"

const catalog = ["workloads", "models", "preprocessors", "datasets"] as const
type CatalogKind = typeof catalog[number]
const modelSections = ["files", "services"] as const
type ModelSection = typeof modelSections[number]

// Canonical screens only; legacy selections are redirected before rendering.
export default async function Page({params,searchParams}:{params:Promise<{route:string[]}>;searchParams:Promise<Record<string,string|string[]|undefined>>}) {
  // Next keeps catch-all segments percent-encoded (`a%3Ab`); screens compare and re-encode plain names.
  let route:string[]
  try { route = (await params).route.map(decodeURIComponent) } catch { notFound() }
  const search = await searchParams
  const query=new URLSearchParams()
  for(const [key,value] of Object.entries(search))for(const item of Array.isArray(value)?value:value===undefined?[]:[value])query.append(key,item)
  const destination=dashboardDestination(`/${(await params).route.join("/")}`,query)
  if(destination)redirect(destination)
  const [head, name, sub] = route
  const create = name === "new" && !search.uid
  const key = `${route.join("/")}/${search.uid ?? ""}`
  let page:React.ReactNode
  if (head === "workloads" && route.length === 1) page = <ServicesPage />
  else if (catalog.includes(head as CatalogKind) && create && route.length === 2) page = <ResourceEditor key={key} kind={head as CatalogKind} />
  else if (catalog.includes(head as CatalogKind) && route.length === 3 && sub === "edit") page = <ResourceEditor key={key} kind={head as CatalogKind} name={name} />
  else if (head === "workloads" && route.length === 2) page = <ServicePage key={key} name={name} />
  else if (head === "workloads" && route.length === 3 && sub === "infer") page = <InferencePage name={name} />
  else if (head === "models" && route.length === 2) page = <ModelPage key={key} name={name} />
  else if (head === "models" && route.length === 3 && modelSections.includes(sub as ModelSection)) page = <ModelSectionPage key={key} name={name} section={sub as ModelSection} />
  else if ((head === "preprocessors" || head === "datasets") && route.length === 2) page = <CatalogPage key={key} kind={head} name={name} />
  else if (head === "operations" && name === "devices" && route.length === 2) page = <DevicesPage />
  else if (head === "measurements" && route.length === 1) page = <MeasurementCoveragePage />
  else if (head === "measurements" && name === "requests" && route.length === 2) page = typeof search.run === "string" ? <MeasurementDetail namespace={typeof search.namespace === "string" ? search.namespace : undefined} name={search.run} /> : search.view === "runs" ? <MeasurementRunsPage /> : <MeasurementRequestsPage />
  else if (head === "measurements" && name === "results" && route.length === 2) page = <MeasurementResultsPage />
  else if (head === "measurements" && name === "requests" && sub === "new" && route.length === 3) page = search.kind === "dispatch-occupancy" ? <DispatchOccupancyRequestPage key={query.toString()} /> : <MeasurementRequestPage />
  else if (head === "measurements" && name === "requests" && sub === "check" && route.length === 3) page = <MeasurementCheckPage />
  else if (head === "measurements" && name === "requests" && route.length === 3) page = <MeasurementRequestsPage name={sub} />
  else if (head === "measurements" && name === "requests" && route[3] === "runs" && route.length === 5) page = <MeasurementDetail namespace={typeof search.namespace === "string" ? search.namespace : undefined} name={route[4]} />
  else if (head === "measurements" && route.length === 4 && route[1] === "power-mode-transition-costs") page = <UnsupportedTransitionMeasurement />
  else if (head === "records" && route.length === 1) page = <RecordsPage />
  else if (head === "settings" && route.length === 1) page = <SettingsPage />
  else if (head === "experiments" && route.length === 1) page = search.view === "published" ? <PublishedExperimentsPage /> : <EvaluationsPage />
  else if (head === "experiments" && name === "results" && route.length === 2) page = <ExperimentsPage />
  else if (head === "experiments" && name === "runs" && route.length === 3) page = <EvaluationRunPage key={key} name={sub} />
  else if (head === "experiments" && name === "new" && route.length === 2) page = <EvaluationAuthorPage />
  else if (head === "experiments" && name === "runs" && route[3] === "edit" && route.length === 4) page = <EvaluationAuthorPage key={key} name={sub} />
  else if (head === "experiments" && name === "load-scenarios" && route.length === 2) page = <LoadScenarioPage />
  else if (head === "experiments" && name === "load-scenarios" && sub === "new" && route.length === 3) page = <LoadScenarioAuthorPage />
  else if (head === "experiments" && name === "load-scenarios" && route[3] === "edit" && route.length === 4) page = <LoadScenarioAuthorPage name={sub} />
  else notFound()
  return <Suspense fallback={<p className="screen" role="status">화면 여는 중</p>}>{page}</Suspense>
}
