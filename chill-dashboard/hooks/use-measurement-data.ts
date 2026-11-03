"use client"
import { useSearchParams } from "next/navigation"
import { useQuery } from "@tanstack/react-query"
import { ApiError, decodeResponse, relay, warmingRetry, warmingDelay, useChillQuery, useSession } from "@/lib/client"
import { coverageProfileQuery, coverageMeasurementStatus, type CompatibilityRead } from "@/lib/measurement-coverage"
import type { InventoryView, MeasurementPlanList, ModelCompatibility, MeasurementCoverageSummaryResponse as MeasurementCoverageResponse, MeasurementCoverageCell, ProfilesView, RunsView } from "@/lib/read-types"
import { parseMeasurementJSON } from "@/lib/measurement-summary"
async function readPage<T>(path:string,token:string,signal:AbortSignal):Promise<T> {
  const response=await relay(path,token,signal)
  return decodeResponse(response.body,parseMeasurementJSON) as T
}
export function useMeasurementPlans(namespace:string,enabled=true) {
  const {token}=useSession()
  const path=`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
  const query=useQuery<MeasurementPlanList,ApiError>({queryKey:["chill",path,"pages"],enabled:!!token&&enabled,retry:warmingRetry,retryDelay:warmingDelay,queryFn:async({signal})=>{
    const items:NonNullable<MeasurementPlanList["items"]>=[]
    let offset=0
    for(;;) {
      const page=await readPage<MeasurementPlanList>(`${path}?${new URLSearchParams({view:"summary",summary:"v2",limit:"50",offset:String(offset)})}`,token,signal)
      if(!page.page)return page
      items.push(...page.items??[])
      if(page.page?.nextOffset===undefined)return {...page,items}
      if(page.page.nextOffset<=offset)throw new ApiError(0,"측정 요청 페이지가 전진하지 않아요",null)
      offset=page.page.nextOffset
    }
  }})
  return {...query,data:query.error&&[401,403].includes(query.error.status)?undefined:query.data}
}
export function useCoverageInventory() {
  const {token,config}=useSession()
  const query=useQuery<InventoryView,ApiError>({queryKey:["chill","coverage-inventory"],queryFn:async({signal})=>{
    const classes=new Map<string,InventoryView["classes"][number]>(),models=new Map<string,InventoryView["models"][number]>(),seen=new Set<number>(),pending=[0],pages:InventoryView[]=[]
    while(pending.length) {
      const offset=pending.shift()!
      if(seen.has(offset)) continue
      seen.add(offset)
      const page=await readPage<InventoryView>(`v1/inventory?${new URLSearchParams({limit:String(config.maxPageSize),offset:String(offset)})}`,token,signal)
      pages.push(page)
      for(const cls of page.classes) classes.set(cls.uid,cls)
      for(const model of page.models) models.set(model.uid,model)
      for(const next of [page.classPage?.nextOffset,page.modelPage?.nextOffset]) if(next!==undefined) {if(next<=offset) throw new ApiError(0,"inventory 페이지가 전진하지 않아요",null);pending.push(next)}
    }
    return {...pages[0],classes:[...classes.values()],models:[...models.values()],classPage:undefined,modelPage:undefined,sourcePages:pages.map(({classes:_classes,models:_models,...page})=>{void _classes;void _models;return page})}
  }})
  return {...query,data:query.error&&[401,403].includes(query.error.status)?undefined:query.data}
}
export function useModelCompatibility(modelSpec:string,executionPath:string,deviceClass:string,nodes:{name:string;uid:string;resourceVersion:string}[],modelUID?:string,classUID?:string) {
  const {token}=useSession()
  return useQuery({queryKey:["chill","model-compatibility",modelSpec,modelUID,executionPath,deviceClass,classUID,nodes],enabled:!!token&&!!modelSpec&&!!executionPath&&!!deviceClass&&nodes.length>0,retry:false,queryFn:async({signal})=>{
    const reads:CompatibilityRead[]=[]
    // Only an opened cell's Nodes (or the single request Node), one GET at a time; power modes share this query.
    for (const node of nodes) {
      const selection={modelSpec,executionPath,deviceClass,node:node.name}
      try {
        const data=await readPage<ModelCompatibility>(`v1/model-compatibility?${new URLSearchParams(selection)}`,token,signal)
        if (Object.entries(selection).some(([key,value])=>data.selection[key as keyof typeof selection]!==value)||data.inputs.node.uid!==node.uid||(modelUID&&data.inputs.modelSpec.uid!==modelUID)||(classUID&&data.inputs.deviceClass.uid!==classUID)) throw new Error("현재 선택 identity와 응답이 달라요")
        reads.push({node:node.name,selection,data})
      } catch(error) {
        if(signal.aborted) throw error
        reads.push({node:node.name,selection,error:error instanceof Error?error.message:"호환성 조회 실패"})
      }
    }
    return reads
  }})
}

export function useMeasurementCoverage(namespace:string,enabled=true) {
  const {token,config}=useSession(),params=useSearchParams()
  const query=new URLSearchParams({view:"summary",summary:"v2"})
  for(const key of ["batches","node"]) {const value=params.get(key);if(value)query.set(key,value)}
  const path=`v1/namespaces/${encodeURIComponent(namespace)}/measurement-coverage${query.size?`?${query}`:""}`
  const result=useQuery<MeasurementCoverageResponse,ApiError>({queryKey:["chill",path],enabled,retry:warmingRetry,retryDelay:warmingDelay,refetchOnMount:"always",refetchInterval:query=>query.state.error&&[401,403].includes(query.state.error.status)?false:query.state.data?.cells.some(cell=>cell.occupancy?.some(row=>row.state==="measuring"))?config.pollIntervalMs:false,queryFn:({signal})=>readPage<MeasurementCoverageResponse>(path,token,signal)})
  return {...result,data:result.error?undefined:result.data}
}

export function useCoverageProfiles(cell:MeasurementCoverageCell,enabled:boolean) {
  const {token}=useSession()
  const filters=coverageProfileQuery(cell)
  return useQuery<ProfilesView,ApiError>({queryKey:["chill","coverage-profile-times",filters.toString()],enabled:!!token&&enabled,retry:warmingRetry,retryDelay:warmingDelay,queryFn:async({signal})=>{
    const all:NonNullable<ProfilesView["profiles"]>=[]
    let offset=0
    for(;;) {
      filters.set("offset",String(offset))
      const page=await readPage<ProfilesView>(`v1/scheduler/profiles?${filters}`,token,signal)
      all.push(...page.profiles??[])
      if(page.page?.nextOffset===undefined)return {...page,profiles:all}
      if(page.page.nextOffset<=offset)throw new ApiError(0,"프로파일 페이지가 전진하지 않습니다",null)
      offset=page.page.nextOffset
    }
  }})
}

export function useCoverageRuns(cell:MeasurementCoverageCell,namespace:string) {
  const running=coverageMeasurementStatus(cell)==="running"
  return useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${new URLSearchParams({model:cell.model.name,deviceClass:cell.deviceClass.name,path:cell.executionPath,powerMode:cell.powerMode,limit:"200",...(running?{}:{status:"problem"})})}`,{enabled:running||cell.failureSummary.length>0})
}
