import { Suspense } from "react"
import { notFound } from "next/navigation"
import { ManagementPage } from "@/features/management"
import { InferencePage } from "@/features/inference"
import { ObservationsPage } from "@/features/observations"
import { ResourcesPage, PoolsPage, PoolPage, MeasurementsPage, MeasurementCheckPage, MeasurementDetail, ProducerPage } from "@/features/read-pages"
import { SchedulerPage, SchedulerCandidatesPage, SchedulerLoadPage } from "@/features/scheduler"

export default async function Page({params,searchParams}:{params:Promise<{route:string[]}>;searchParams:Promise<Record<string,string|string[]|undefined>>}) {
  const {route} = await params
  const search = await searchParams
  const create = route[1] === "new" && !search.uid
  let page:React.ReactNode
  if ((route[0]==="models" || route[0]==="workloads") && route.length<=2) page=<ManagementPage key={`${route.join("/")}/${search.uid ?? ""}`} kind={route[0]} name={create ? undefined : route[1]} newResource={create} />
  else if((route[0]==="models"||route[0]==="workloads")&&route.length===3&&route[2]==="edit")page=<ManagementPage key={`${route.join("/")}/${search.uid ?? ""}`} kind={route[0]} name={route[1]} editResource />
  else if(route[0]==="workloads"&&route.length===3&&route[2]==="infer") page=<InferencePage name={route[1]} />
  else if(route[0]==="resources"&&route.length===1)page=<ResourcesPage/>
  else if(route[0]==="pools"&&route.length===1)page=<PoolsPage/>
  else if(route[0]==="pools"&&route.length===2)page=<PoolPage id={route[1]}/>
  else if(route[0]==="measurements"&&route.length===1)page=<MeasurementsPage/>
  else if(route[0]==="measurements"&&route.length===2&&route[1]==="check")page=<MeasurementCheckPage/>
  else if(route[0]==="measurements"&&route.length===4)page=<MeasurementDetail kind={route[1]} namespace={route[2]} name={route[3]}/>
  else if(route[0]==="observations"&&route.length===1)page=<ObservationsPage/>
  else if(route[0]==="scheduler"&&route.length===1)page=<SchedulerPage/>
  else if(route[0]==="scheduler"&&route.length===2&&route[1]==="candidates")page=<SchedulerCandidatesPage/>
  else if(route[0]==="scheduler"&&route.length===2&&route[1]==="load")page=<SchedulerLoadPage/>
  else if(route[0]==="changes"&&route.length===1)page=<ObservationsPage changes/>
  else if(route[0]==="configuration"&&route.length===1)page=<ProducerPage area="configuration"/>
  else if((route[0]==="transitions"||route[0]==="experiments")&&route.length<=2)page=<ProducerPage area={route[0]} id={route[1]}/>
  else notFound()
  return <Suspense fallback={<p className="page" role="status">화면 여는 중</p>}>{page}</Suspense>
}
